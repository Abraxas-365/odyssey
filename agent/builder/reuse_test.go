package builder

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// registryHost is an in-memory registry's host:port.
func registryHost(t *testing.T) string {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func ref(s string, _ ...name.Option) name.Reference {
	r, err := name.ParseReference(s, name.Insecure)
	if err != nil {
		panic(err)
	}
	return r
}

func pushImage(t *testing.T, ref name.Reference) v1.Hash {
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	h, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestReuseTagsTheEarlierImage(t *testing.T) {
	host := registryHost(t)
	h := pushImage(t, ref(host+"/o/p:old", name.Insecure))
	var out bytes.Buffer
	o := ReuseOptions{Image: host + "/o/p@" + h.String(), Target: host + "/o/p:new", Insecure: true, Work: t.TempDir(), Out: &out}
	r := Reuse(context.Background(), o)
	if r.Digest != h.String() || !r.Reused || r.Reason != "" {
		t.Fatalf("result %+v; log: %s", r, out.String())
	}
	if got := read(t, filepath.Join(o.Work, ReusedFile)); got != h.String() {
		t.Fatalf("marker %q", got)
	}
	if !strings.Contains(out.String(), "Reusing the image of an earlier build: "+h.String()) {
		t.Fatalf("log: %s", out.String())
	}
	d, err := remote.Head(ref(host+"/o/p:new", name.Insecure))
	if err != nil || d.Digest != h {
		t.Fatalf("the new tag points at %v (%v), want %s", d, err, h)
	}
}

func TestReuseTagsAnImageIndex(t *testing.T) {
	host := registryHost(t)
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})
	if err := remote.WriteIndex(ref(host+"/o/p:old", name.Insecure).(name.Tag), idx); err != nil {
		t.Fatal(err)
	}
	h, _ := idx.Digest()
	o := ReuseOptions{Image: host + "/o/p@" + h.String(), Target: host + "/o/p:new", Insecure: true, Work: t.TempDir()}
	if r := Reuse(context.Background(), o); r.Digest != h.String() {
		t.Fatalf("result %+v", r)
	}
	if d, err := remote.Head(ref(host+"/o/p:new", name.Insecure)); err != nil || d.Digest != h {
		t.Fatalf("tag: %v %v", d, err)
	}
}

func TestReuseFallsBackToBuilding(t *testing.T) {
	host := registryHost(t)
	pushImage(t, ref(host+"/o/p:old", name.Insecure))
	gone := "sha256:" + strings.Repeat("e", 64)
	cases := []struct {
		name          string
		image, target string
		head          func(context.Context, name.Digest) (v1.Hash, error)
		want          string
	}{
		{"gone", host + "/o/p@" + gone, host + "/o/p:new", nil, "The earlier image is gone: building"},
		{"repository gone", host + "/o/q@" + gone, host + "/o/q:new", nil, "The earlier image is gone: building"},
		{"registry error", host + "/o/p@" + gone, host + "/o/p:new", func(context.Context, name.Digest) (v1.Hash, error) { return v1.Hash{}, errors.New("boom") }, "boom"},
		{"not a digest", host + "/o/p:old", host + "/o/p:new", nil, "not a valid reference"},
		{"other repository", host + "/other/p@" + gone, host + "/o/p:new", nil, "not in this build's repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			o := ReuseOptions{Image: c.image, Target: c.target, Insecure: true, Work: t.TempDir(), Out: &out, Head: c.head}
			if r := Reuse(context.Background(), o); r != (Result{}) {
				t.Fatalf("result %+v", r)
			}
			if _, err := os.Stat(filepath.Join(o.Work, ReusedFile)); !os.IsNotExist(err) {
				t.Fatalf("marker written: %v", err)
			}
			if !strings.Contains(out.String(), c.want) || !strings.Contains(out.String(), "building") {
				t.Fatalf("log: %s", out.String())
			}
			if _, err := remote.Head(ref(c.target, name.Insecure)); err == nil {
				t.Fatal("tagged")
			}
		})
	}
}

func TestReuseTagFailureBuilds(t *testing.T) {
	host := registryHost(t)
	h := pushImage(t, ref(host+"/o/p:old", name.Insecure))
	var out bytes.Buffer
	o := ReuseOptions{Image: host + "/o/p@" + h.String(), Target: host + "/o/p:new", Insecure: true, Work: t.TempDir(), Out: &out,
		Tag: func(context.Context, name.Digest, name.Tag) error { return errors.New("denied") }}
	if r := Reuse(context.Background(), o); r != (Result{}) || !strings.Contains(out.String(), "denied") {
		t.Fatalf("result %+v: %s", r, out.String())
	}
	if _, err := os.Stat(filepath.Join(o.Work, ReusedFile)); !os.IsNotExist(err) {
		t.Fatal("marker written")
	}
}

func TestReuseFailsWhenTheMarkerCannotBeWritten(t *testing.T) {
	host := registryHost(t)
	h := pushImage(t, ref(host+"/o/p:old", name.Insecure))
	o := ReuseOptions{Image: host + "/o/p@" + h.String(), Target: host + "/o/p:new", Insecure: true, Work: filepath.Join(t.TempDir(), "nope")}
	if r := Reuse(context.Background(), o); r.Reason != ReasonFailed {
		t.Fatalf("result %+v", r)
	}
}

func TestPrepareAndBuildSkipTheirWorkWhenReused(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	o := prepareOptions(t, "http://127.0.0.1:1", strings.Repeat("a", 64), 10)
	if err := os.WriteFile(filepath.Join(o.Work, ReusedFile), []byte(digest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := Prepare(context.Background(), o); r != (Result{}) {
		t.Fatalf("prepare %+v", r)
	}
	if read(t, filepath.Join(o.Work, BinDir, "odyssey-agent")) != "#!bin" {
		t.Fatal("the builder binary is still copied")
	}
	if es, _ := os.ReadDir(filepath.Join(o.Work, SourceDir)); len(es) != 0 {
		t.Fatal("the source was fetched")
	}
	var out bytes.Buffer
	r := Build(context.Background(), BuildOptions{Work: o.Work, Image: "r/o/p:b1", Out: &out, Command: "/nonexistent/buildctl"})
	if r.Digest != digest || !r.Reused || r.Reason != "" {
		t.Fatalf("build %+v; log: %s", r, out.String())
	}
	if got, ok := ParseResult(string(r.Encode())); !ok || !got.Reused || got.Digest != digest {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
}
