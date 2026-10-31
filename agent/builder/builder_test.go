package builder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railwayapp/railpack/core/mise"
)

func archive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// serve answers the source with the token "tok"; fail makes the first n
// requests answer 503.
func serve(t *testing.T, body []byte, fail int32) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if n <= fail {
			http.Error(w, "no tunnel", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func prepareOptions(t *testing.T, url, sha string, size int64) PrepareOptions {
	self := filepath.Join(t.TempDir(), "odyssey-agent")
	if err := os.WriteFile(self, []byte("#!bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return PrepareOptions{SourceURL: url, Token: "tok", SHA256: sha, Size: size, Work: t.TempDir(), Self: self,
		Out: &bytes.Buffer{}, RetryWait: time.Millisecond,
		Plan: func(string) ([]byte, []string, error) { return []byte(`{"steps":[]}`), []string{"Detected Node"}, nil }}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPrepareWithADockerfile(t *testing.T) {
	body, sha := archive(t, map[string]string{"Dockerfile": "FROM scratch\n", "main.go": "package main"})
	srv, _ := serve(t, body, 0)
	o := prepareOptions(t, srv.URL, sha, int64(len(body)))
	o.Plan = func(string) ([]byte, []string, error) {
		t.Fatal("a Dockerfile app is not planned")
		return nil, nil, nil
	}
	if r := Prepare(context.Background(), o); r != (Result{}) {
		t.Fatalf("result %+v", r)
	}
	if read(t, filepath.Join(o.Work, ModeFile)) != ModeDockerfile || read(t, filepath.Join(o.Work, SourceDir, "main.go")) != "package main" {
		t.Fatal("layout")
	}
	if read(t, filepath.Join(o.Work, BinDir, "odyssey-agent")) != "#!bin" {
		t.Fatal("the builder binary was not copied")
	}
}

func TestPrepareWithRailpackRetriesTheSource(t *testing.T) {
	body, sha := archive(t, map[string]string{"package.json": "{}"})
	srv, calls := serve(t, body, 2)
	o := prepareOptions(t, srv.URL, sha, int64(len(body)))
	if r := Prepare(context.Background(), o); r != (Result{}) {
		t.Fatalf("result %+v", r)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls %d", calls.Load())
	}
	if read(t, filepath.Join(o.Work, ModeFile)) != ModeRailpack || read(t, filepath.Join(o.Work, PlanDir, PlanFile)) != `{"steps":[]}` {
		t.Fatal("plan")
	}
	if !strings.Contains(o.Out.(*bytes.Buffer).String(), "Detected Node") {
		t.Fatal("the planner's lines go to the log")
	}
}

func TestPrepareFailures(t *testing.T) {
	body, sha := archive(t, map[string]string{"package.json": "{}"})
	srv, _ := serve(t, body, 0)
	cases := []struct {
		name   string
		mutate func(*PrepareOptions)
		reason string
		msg    string
	}{
		{"wrong digest", func(o *PrepareOptions) { o.SHA256 = strings.Repeat("0", 64) }, ReasonSource, "not the uploaded one"},
		{"wrong size", func(o *PrepareOptions) { o.Size-- }, ReasonSource, "not valid"},
		{"bad token", func(o *PrepareOptions) { o.Token = "nope" }, ReasonSource, "401"},
		{"unsupported", func(o *PrepareOptions) {
			o.Plan = func(string) ([]byte, []string, error) {
				return nil, []string{"ERROR: No start command was found"}, errUnsupported
			}
		}, ReasonUnsupported, "No start command"},
		{"planner down", func(o *PrepareOptions) {
			o.Plan = func(string) ([]byte, []string, error) { return nil, nil, errors.New("mise: timeout") }
		}, ReasonFailed, "mise: timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := prepareOptions(t, srv.URL, sha, int64(len(body)))
			c.mutate(&o)
			r := Prepare(context.Background(), o)
			if r.Reason != c.reason || !strings.Contains(r.Message, c.msg) {
				t.Fatalf("result %+v", r)
			}
		})
	}
}

func TestPrepareGivesUpOnAnUnreachableAgent(t *testing.T) {
	body, sha := archive(t, map[string]string{"package.json": "{}"})
	srv, calls := serve(t, body, 100)
	o := prepareOptions(t, srv.URL, sha, int64(len(body)))
	if r := Prepare(context.Background(), o); r.Reason != ReasonSource || !strings.Contains(r.Message, "503") {
		t.Fatalf("result %+v", r)
	}
	if calls.Load() != fetchAttempts {
		t.Fatalf("calls %d", calls.Load())
	}
}

func TestBuildArgs(t *testing.T) {
	o := BuildOptions{Work: "/work", Image: "r/o/p:b1", CacheRef: "r/o/p:buildcache", CacheKey: "p1",
		Frontend: "ghcr.io/railwayapp/railpack-frontend:v1", Platform: "linux/amd64", Insecure: true}
	rp := strings.Join(o.Args(ModeRailpack), " ")
	for _, want := range []string{
		"--local context=/work/src", "--frontend gateway.v0", "--opt source=ghcr.io/railwayapp/railpack-frontend:v1",
		"--local dockerfile=/work/plan", "--opt build-arg:cache-key=p1", "--opt platform=linux/amd64",
		"--output type=image,name=r/o/p:b1,push=true,registry.insecure=true", "--metadata-file /work/metadata.json",
		"--import-cache type=registry,ref=r/o/p:buildcache,registry.insecure=true",
		"--export-cache type=registry,ref=r/o/p:buildcache,mode=max,ignore-error=true,registry.insecure=true",
	} {
		if !strings.Contains(rp, want) {
			t.Errorf("railpack args lack %q:\n%s", want, rp)
		}
	}
	o.Insecure, o.CacheRef = false, ""
	df := strings.Join(o.Args(ModeDockerfile), " ")
	if !strings.Contains(df, "--frontend dockerfile.v0 --local dockerfile=/work/src") || strings.Contains(df, "insecure") || strings.Contains(df, "cache") {
		t.Fatalf("dockerfile args: %s", df)
	}
}

// fakeBuildctl writes a script standing in for buildctl-daemonless.sh.
func fakeBuildctl(t *testing.T, script string) string {
	p := filepath.Join(t.TempDir(), "buildctl")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuild(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ModeFile), []byte(ModeDockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, script string
		want         Result
		msg          string
	}{
		{"pushed", `echo "#1 building"; echo '{"containerimage.digest":"` + digest + `"}' > ` + work + "/metadata.json", Result{Digest: digest}, ""},
		{"build error", `rm -f ` + work + `/metadata.json; echo "#5 RUN npm ci"; echo "ERROR: process \"npm ci\" did not complete successfully: exit code: 1" >&2; exit 1`,
			Result{Reason: ReasonFailed}, "npm ci"},
		{"push error", `echo "error: failed to solve: failed to push r/o/p:b1: unauthorized: authentication required" >&2; exit 1`,
			Result{Reason: ReasonPush}, "unauthorized"},
		{"no metadata", `rm -f ` + work + `/metadata.json; exit 0`, Result{Reason: ReasonPush}, "did not say"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			r := Build(context.Background(), BuildOptions{Work: work, Image: "r/o/p:b1", Out: &out, Command: fakeBuildctl(t, c.script)})
			if r.Digest != c.want.Digest || r.Reason != c.want.Reason || !strings.Contains(r.Message, c.msg) {
				t.Fatalf("result %+v; log:\n%s", r, out.String())
			}
		})
	}
	if r := Build(context.Background(), BuildOptions{Work: t.TempDir(), Out: &bytes.Buffer{}}); r.Reason != ReasonFailed || !strings.Contains(r.Message, "not prepared") {
		t.Fatalf("unprepared: %+v", r)
	}
}

func TestResultFitsTheTerminationMessage(t *testing.T) {
	r := Result{Reason: ReasonFailed, Message: strings.Repeat("\"<ñ>\"\n", 5000) + "the end"}
	b := r.Encode()
	if len(b) > 4096 {
		t.Fatalf("%d bytes", len(b))
	}
	got, ok := ParseResult(string(b))
	if !ok || got.Reason != ReasonFailed || !strings.HasSuffix(got.Message, "the end") || !strings.HasPrefix(got.Message, "…") {
		t.Fatalf("%+v", got)
	}
	for _, s := range []string{"", "Error", "OOMKilled", `{}`} {
		if _, ok := ParseResult(s); ok {
			t.Fatalf("%q is not a Result", s)
		}
	}
}

func TestPlaceMise(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "railpack", "mise")
	if err := placeMise(from, to); err != nil { // nothing baked (a dev run)
		t.Fatal(err)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Fatal("nothing to place")
	}
	name := "mise-" + strings.TrimSpace(mise.Version)
	if err := os.WriteFile(filepath.Join(from, name), []byte("mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := placeMise(from, to); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(to, name)); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("placed: %v %v", fi, err)
	}
}
