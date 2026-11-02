package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcegithub"
)

var testCreds = sourcegithub.AppCredentials{
	ID: 42, Slug: "odicea", ClientID: "Iv1.abc", ClientSecret: "client-s3cret",
	WebhookSecret: "hook-s3cret", PEM: "-----BEGIN RSA PRIVATE KEY-----\nxx\n-----END RSA PRIVATE KEY-----",
	HTMLURL: "https://github.com/apps/odicea",
}

func TestGitHubAppWriteFilesAndEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gh")
	files, err := writeGitHubAppFiles(dir, testCreds, defaultGitHubURL, defaultGitHubAPIURL, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files.all() {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v", p, st.Mode().Perm())
		}
		if !filepath.IsAbs(p) {
			t.Errorf("%s is not absolute", p)
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(files.WebhookSecret); string(b) != "hook-s3cret" {
		t.Errorf("webhook-secret = %q", b)
	}
	if b, _ := os.ReadFile(files.PrivateKey); !strings.HasPrefix(string(b), "-----BEGIN") || !strings.HasSuffix(string(b), "\n") {
		t.Errorf("private key = %q", b)
	}
	env, _ := os.ReadFile(files.Env)
	want := "GITHUB_APP_ID=42\nGITHUB_APP_SLUG=odicea\nGITHUB_CLIENT_ID=Iv1.abc\n" +
		"GITHUB_APP_PRIVATE_KEY_FILE=" + files.PrivateKey + "\n" +
		"GITHUB_WEBHOOK_SECRET_FILE=" + files.WebhookSecret + "\n" +
		"GITHUB_CLIENT_SECRET_FILE=" + files.ClientSecret + "\n"
	if string(env) != want {
		t.Errorf("github.env =\n%s\nwant\n%s", env, want)
	}
	if strings.Contains(string(env), "s3cret") {
		t.Error("github.env holds a secret value")
	}

	// A second run refuses to overwrite, --force replaces (and fixes the mode).
	if _, err := writeGitHubAppFiles(dir, testCreds, defaultGitHubURL, defaultGitHubAPIURL, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite err = %v", err)
	}
	if err := os.Chmod(files.WebhookSecret, 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := testCreds
	c2.WebhookSecret = "new"
	if _, err := writeGitHubAppFiles(dir, c2, "https://ghe.example", "https://ghe.example/api/v3", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(files.WebhookSecret); string(b) != "new" {
		t.Errorf("forced webhook-secret = %q", b)
	}
	if st, _ := os.Stat(files.WebhookSecret); st.Mode().Perm() != 0o600 {
		t.Errorf("forced mode = %v", st.Mode().Perm())
	}
	env, _ = os.ReadFile(files.Env)
	if !strings.Contains(string(env), "GITHUB_URL=https://ghe.example\n") || !strings.Contains(string(env), "GITHUB_API_URL=https://ghe.example/api/v3\n") {
		t.Errorf("GHES env =\n%s", env)
	}
}

func testHandler(t *testing.T, convert convertFunc) (*manifestHandler, string) {
	t.Helper()
	c := githubAppConfig{PublicURL: "http://localhost:8095", Out: filepath.Join(t.TempDir(), "out"), GitHubURL: defaultGitHubURL, APIURL: defaultGitHubAPIURL, Org: "acme"}
	h := newManifestHandler(c, "st4te", []byte(`{"name":"Odicea","k":"a\"b<"}`), convert)
	return h, c.Out
}

func TestGitHubAppFormPage(t *testing.T) {
	h, _ := testHandler(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `action="https://github.com/organizations/acme/settings/apps/new?state=st4te"`) {
		t.Fatalf("form = %d\n%s", rec.Code, body)
	}
	m := regexp.MustCompile(`name="manifest" value="([^"]*)"`).FindStringSubmatch(body)
	if m == nil || html.UnescapeString(m[1]) != `{"name":"Odicea","k":"a\"b<"}` {
		t.Fatalf("manifest input = %v", m)
	}
	if !strings.Contains(body, ".submit()") || !strings.Contains(body, "<button") {
		t.Error("form lacks auto-submit or button")
	}
}

func TestGitHubAppCallbackStateMismatch(t *testing.T) {
	called := false
	h, out := testHandler(t, func(context.Context, string) (sourcegithub.AppCredentials, error) {
		called = true
		return testCreds, nil
	})
	for _, target := range []string{"/callback?code=c&state=bad", "/callback?code=c", "/callback?state=st4te"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", target, rec.Code)
		}
	}
	if called {
		t.Error("converted without a valid state and code")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("files written")
	}
	select {
	case r := <-h.done:
		t.Errorf("finished: %+v", r)
	default:
	}
}

func TestGitHubAppCallbackOK(t *testing.T) {
	var gotCode string
	h, out := testHandler(t, func(_ context.Context, code string) (sourcegithub.AppCredentials, error) {
		gotCode = code
		return testCreds, nil
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/callback?code=abc&state=st4te", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Listo, vuelve a la terminal") {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body)
	}
	res := <-h.done
	if res.err != nil || gotCode != "abc" || res.creds.ID != 42 {
		t.Fatalf("result = %+v code=%q", res, gotCode)
	}
	if _, err := os.Stat(filepath.Join(out, "github.env")); err != nil {
		t.Error(err)
	}
	// A replay is refused.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/callback?code=abc&state=st4te", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("replay = %d", rec.Code)
	}
}

func TestGitHubAppCallbackConvertError(t *testing.T) {
	h, out := testHandler(t, func(context.Context, string) (sourcegithub.AppCredentials, error) {
		return sourcegithub.AppCredentials{}, errors.New("boom")
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/callback?code=abc&state=st4te", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d", rec.Code)
	}
	if res := <-h.done; res.err == nil || !strings.Contains(res.err.Error(), "boom") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("files written")
	}
}

// TestGitHubAppRunEndToEnd drives the whole command against a fake GitHub:
// the "browser" follows the printed URL, reads the form and replays GitHub's
// redirect to the callback.
func TestGitHubAppRunEndToEnd(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app-manifests/thecode/conversions" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7,"slug":"odicea","client_id":"cid","client_secret":"cs","webhook_secret":"ws","pem":"PEM","html_url":"https://github.com/apps/odicea"}`))
	}))
	defer gh.Close()
	out := filepath.Join(t.TempDir(), "o")
	c := githubAppConfig{PublicURL: "http://localhost:8095", Out: out, GitHubURL: gh.URL, APIURL: gh.URL, Listen: "127.0.0.1:0", Name: "Odicea"}
	convert := func(ctx context.Context, code string) (sourcegithub.AppCredentials, error) {
		return sourcegithub.ConvertManifest(ctx, gh.Client(), gh.URL, code)
	}
	var buf bytes.Buffer
	opened := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- runGitHubApp(context.Background(), c, convert, &syncWriter{w: &buf}, func(u string) { opened <- u })
	}()
	var local string
	select {
	case local = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("never opened")
	}
	resp, err := http.Get(local + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	st := regexp.MustCompile(`\?state=([0-9a-f]+)"`).FindSubmatch(b)
	if st == nil {
		t.Fatalf("no state in form:\n%s", b)
	}
	manifest := regexp.MustCompile(`name="manifest" value="([^"]*)"`).FindSubmatch(b)
	if manifest == nil || !strings.Contains(html.UnescapeString(string(manifest[1])), `"redirect_url":"`+local+`/callback"`) {
		t.Fatalf("manifest = %s", manifest)
	}
	resp, err = http.Get(local + "/callback?code=thecode&state=" + string(st[1]))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("callback = %d", resp.StatusCode)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "client-secret")); string(b) != "cs" {
		t.Errorf("client-secret = %q", b)
	}
	if s := buf.String(); strings.Contains(s, "cs\n") || !strings.Contains(s, "https://github.com/apps/odicea") || !strings.Contains(s, "Instálala") {
		t.Errorf("output:\n%s", s)
	}
}

type syncWriter struct{ w io.Writer }

func (s *syncWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

func TestGitHubAppFlags(t *testing.T) {
	if err := CreateGitHubApp([]string{"--bogus"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if err := CreateGitHubApp([]string{"extra"}); err == nil {
		t.Error("positional argument accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "github.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreateGitHubApp([]string{"--out", dir}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("existing file err = %v", err)
	}
}
