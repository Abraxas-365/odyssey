package sourcegithub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

const (
	appID        = 12345
	installation = 7001
	repoID       = 424242
	sha          = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	webhookKey   = "whsec-test"
	clientSecret = "client-secret-test"
)

// fakeGitHub serves the slice of the GitHub API the provider uses, under
// /api/v3 (as an Enterprise Server does), and OAuth under the root.
type fakeGitHub struct {
	t        *testing.T
	pub      *rsa.PublicKey
	srv      *httptest.Server
	codeload *httptest.Server

	mu            sync.Mutex
	tokenRequests []tokenRequest
	tokenCount    atomic.Int32
	tokenExpiry   time.Time
	gotStatus     map[string]any
	commitETag    string
	codeloadAuth  *string // Authorization header codeload received
	suspended     bool
	tokenStatus   int
	login         string                // GET /user
	userStatus    int                   // forces GET /user to answer this status
	memberships   map[string]membership // GET /user/memberships/orgs/{org}; missing: 404
}

type membership struct {
	state, role string
	status      int // forces this status
}

type tokenRequest struct {
	Installation int64
	Body         map[string]any
}

func newFakeGitHub(t *testing.T, pub *rsa.PublicKey) *fakeGitHub {
	f := &fakeGitHub{t: t, pub: pub, commitETag: `"etag-1"`, tokenExpiry: time.Now().Add(time.Hour)}
	f.codeload = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		f.mu.Lock()
		f.codeloadAuth = &auth
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = io.WriteString(w, "TARBALL-BYTES")
	}))
	t.Cleanup(f.codeload.Close)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// checkApp asserts the bearer is a JWT signed by the test key with iss = app id.
func (f *fakeGitHub) checkApp(r *http.Request) {
	f.t.Helper()
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var claims jwt.RegisteredClaims
	tok, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return f.pub, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(fmt.Sprint(appID)))
	if err != nil || !tok.Valid {
		f.t.Errorf("%s: App JWT invalid: %v", r.URL.Path, err)
		return
	}
	if d := claims.ExpiresAt.Sub(claims.IssuedAt.Time); d != 10*time.Minute {
		f.t.Errorf("JWT exp-iat = %v, want 10m (iat now-60s, exp now+9m)", d)
	}
}

func (f *fakeGitHub) wantToken(r *http.Request, token string) bool {
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		f.t.Errorf("%s %s: Authorization = %q, want bearer %s", r.Method, r.URL.Path, got, token)
		return false
	}
	return true
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/login/oauth/access_token":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Accept") != "application/json" || body["client_id"] != "Iv1.client" || body["client_secret"] != clientSecret {
			f.t.Errorf("oauth request wrong: %v %v", r.Header, body)
		}
		if body["code"] != "good-code" {
			f.json(w, 200, map[string]string{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."})
			return
		}
		f.json(w, 200, map[string]string{"access_token": "ghu_usertoken", "token_type": "bearer"})
		return
	case !strings.HasPrefix(path, "/api/v3/"):
		http.NotFound(w, r)
		return
	}
	path = strings.TrimPrefix(path, "/api/v3")
	switch {
	case path == fmt.Sprintf("/app/installations/%d/access_tokens", installation) && r.Method == "POST":
		f.checkApp(r)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tokenRequests = append(f.tokenRequests, tokenRequest{installation, body})
		status, expiry := f.tokenStatus, f.tokenExpiry
		f.mu.Unlock()
		n := f.tokenCount.Add(1)
		if status != 0 {
			f.json(w, status, map[string]string{"message": "nope"})
			return
		}
		f.json(w, 201, map[string]any{"token": fmt.Sprintf("ghs_token%d", n), "expires_at": expiry.UTC().Format(time.RFC3339)})
	case path == fmt.Sprintf("/app/installations/%d", installation):
		f.checkApp(r)
		inst := map[string]any{"id": installation, "repository_selection": "selected",
			"target_type": "Organization", "account": map[string]any{"login": "acme", "type": "Organization"}}
		f.mu.Lock()
		if f.suspended {
			inst["suspended_at"] = "2026-03-01T00:00:00Z"
			inst["repository_selection"] = "all"
		}
		f.mu.Unlock()
		f.json(w, 200, inst)
	case strings.HasPrefix(path, "/app/installations/"):
		f.checkApp(r)
		f.json(w, 404, map[string]string{"message": "Not Found"})
	case path == "/installation/repositories":
		if r.URL.Query().Get("per_page") != "100" {
			f.t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/installation/repositories?per_page=100&page=2>; rel="next"`, f.srv.URL))
			f.json(w, 200, map[string]any{"total_count": 2, "repositories": []map[string]any{
				{"id": repoID, "full_name": "acme/web", "default_branch": "main", "private": true}}})
			return
		}
		f.json(w, 200, map[string]any{"total_count": 2, "repositories": []map[string]any{
			{"id": 9, "full_name": "acme/api", "default_branch": "trunk", "private": false}}})
	case path == fmt.Sprintf("/repositories/%d", repoID):
		f.json(w, 200, map[string]any{"id": repoID, "full_name": "acme/web", "default_branch": "main", "private": true})
	case strings.HasPrefix(path, "/repositories/") && strings.Count(path, "/") == 2:
		f.json(w, 404, map[string]string{"message": "Not Found"})
	case path == "/repos/acme/web/branches":
		f.json(w, 200, []map[string]any{
			{"name": "main", "commit": map[string]any{"sha": sha}},
			{"name": "dev", "commit": map[string]any{"sha": "dead"}}})
	case path == fmt.Sprintf("/repositories/%d/commits/main", repoID):
		f.mu.Lock()
		etag := f.commitETag
		f.mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		f.json(w, 200, map[string]any{"sha": sha,
			"commit": map[string]any{"message": "Fix the total\n\nbody", "author": map[string]any{"name": "Ana Pérez"}},
			"author": map[string]any{"login": "anaperez"}})
	case path == fmt.Sprintf("/repositories/%d/commits/nologin", repoID):
		w.Header().Set("ETag", `"e"`)
		f.json(w, 200, map[string]any{"sha": sha,
			"commit": map[string]any{"message": "x", "author": map[string]any{"name": "Ana Pérez"}}, "author": nil})
	case path == fmt.Sprintf("/repositories/%d/commits/gone", repoID):
		f.json(w, 422, map[string]string{"message": "No commit found for SHA: gone"})
	case path == fmt.Sprintf("/repositories/%d/git/trees/main", repoID):
		if r.URL.Query().Get("recursive") != "1" {
			f.json(w, 400, map[string]string{"message": "want recursive"})
			return
		}
		f.json(w, 200, map[string]any{"sha": sha, "truncated": false, "tree": []map[string]any{
			{"path": "apps", "type": "tree"}, {"path": "apps/web/package.json", "type": "blob"},
			{"path": "apps/api/go.mod", "type": "blob"}, {"path": "vendor/lib", "type": "commit"}}})
	case path == fmt.Sprintf("/repositories/%d/git/trees/huge", repoID):
		f.json(w, 200, map[string]any{"truncated": true, "tree": []map[string]any{{"path": "go.mod", "type": "blob"}}})
	case path == fmt.Sprintf("/repositories/%d/git/trees/gone", repoID):
		f.json(w, 404, map[string]string{"message": "Not Found"})
	case path == fmt.Sprintf("/repositories/%d/git/trees/empty", repoID):
		f.json(w, 409, map[string]string{"message": "Git Repository is empty."})
	case path == fmt.Sprintf("/repositories/%d/tarball/%s", repoID, sha):
		http.Redirect(w, r, f.codeload.URL+"/legacy.tar.gz/refs/heads/main?token=secret", http.StatusFound)
	case path == fmt.Sprintf("/repositories/%d/tarball/missing", repoID):
		f.json(w, 404, map[string]string{"message": "Not Found"})
	case path == fmt.Sprintf("/repositories/%d/statuses/%s", repoID, sha) && r.Method == "POST":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.gotStatus = body
		f.mu.Unlock()
		f.json(w, 201, map[string]any{"id": 1})
	case path == "/user/installations":
		if !f.wantToken(r, "ghu_usertoken") {
			f.json(w, 401, map[string]string{"message": "Bad credentials"})
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/user/installations?per_page=100&page=2>; rel="next"`, f.srv.URL))
			f.json(w, 200, map[string]any{"total_count": 3, "installations": []map[string]any{{"id": installation}, {"id": 8}}})
			return
		}
		f.json(w, 200, map[string]any{"total_count": 3, "installations": []map[string]any{{"id": 9}}})
	case path == "/user":
		if !f.wantToken(r, "ghu_usertoken") {
			f.json(w, 401, map[string]string{"message": "Bad credentials"})
			return
		}
		f.mu.Lock()
		login, status := f.login, f.userStatus
		f.mu.Unlock()
		if status != 0 {
			f.json(w, status, map[string]string{"message": "nope"})
			return
		}
		f.json(w, 200, map[string]any{"login": login})
	case strings.HasPrefix(path, "/user/memberships/orgs/"):
		if !f.wantToken(r, "ghu_usertoken") {
			f.json(w, 401, map[string]string{"message": "Bad credentials"})
			return
		}
		f.mu.Lock()
		m, ok := f.memberships[strings.TrimPrefix(path, "/user/memberships/orgs/")]
		f.mu.Unlock()
		if !ok {
			f.json(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		if m.status != 0 {
			f.json(w, m.status, map[string]string{"message": "nope"})
			return
		}
		f.json(w, 200, map[string]any{"state": m.state, "role": m.role})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

type env struct {
	p   *Provider
	gh  *fakeGitHub
	key *rsa.PrivateKey
	now *time.Time
}

func setup(t *testing.T, pkcs8 bool) *env {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var der []byte
	var blockType string
	if pkcs8 {
		der, err = x509.MarshalPKCS8PrivateKey(key)
		blockType = "PRIVATE KEY"
		if err != nil {
			t.Fatal(err)
		}
	} else {
		der, blockType = x509.MarshalPKCS1PrivateKey(key), "RSA PRIVATE KEY"
	}
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	gh := newFakeGitHub(t, &key.PublicKey)
	now := time.Now()
	cfg := config.GitHub{
		AppID: appID, Slug: "odyssey-dev", ClientID: "Iv1.client",
		PrivateKeyFile:    write("key.pem", string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))),
		WebhookSecretFile: write("webhook", webhookKey+"\n"),
		ClientSecretFile:  write("client", clientSecret+"\r\n"),
		APIURL:            gh.srv.URL,
		WebURL:            gh.srv.URL,
	}
	e := &env{gh: gh, key: key, now: &now}
	e.p, err = New(cfg, Options{Now: func() time.Time { return *e.now }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var ctx = context.Background()

func code(t *testing.T, err error) string {
	t.Helper()
	var e *errx.Error
	if !errx.As(err, &e) {
		t.Fatalf("err = %v, want *errx.Error", err)
	}
	return e.Code
}

func TestNewRequiresApp(t *testing.T) {
	if _, err := New(config.GitHub{}, Options{}); err == nil || code(t, err) != source.CodeNotConfigured {
		t.Fatalf("err = %v", err)
	}
	if _, err := New(config.GitHub{AppID: 1, PrivateKeyFile: "/nonexistent"}, Options{}); err == nil {
		t.Fatal("missing key file accepted")
	}
}

func TestNewReadsPKCS1AndPKCS8(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		e := setup(t, pkcs8)
		if _, err := e.p.Installation(ctx, installation); err != nil { // verifies the JWT against the key
			t.Fatalf("pkcs8=%v: %v", pkcs8, err)
		}
	}
}

func TestInstallPageAndIdentity(t *testing.T) {
	e := setup(t, false)
	if !e.p.Enabled() || e.p.Slug() != "odyssey-dev" {
		t.Fatal("enabled/slug")
	}
	want := e.gh.srv.URL + "/apps/odyssey-dev/installations/new?state=a+b%26c%3D"
	if got := e.p.InstallPage("a b&c="); got != want {
		t.Errorf("InstallPage = %q, want %q", got, want)
	}
}

func TestInstallation(t *testing.T) {
	e := setup(t, false)
	info, err := e.p.Installation(ctx, installation)
	want := source.InstallationInfo{ID: installation, Account: "acme", AccountType: "Organization"}
	if err != nil || info != want {
		t.Fatalf("info = %+v, %v", info, err)
	}
	e.gh.mu.Lock()
	e.gh.suspended = true
	e.gh.mu.Unlock()
	info, _ = e.p.Installation(ctx, installation)
	if !info.Suspended || !info.AllRepositories {
		t.Errorf("suspended/all = %+v", info)
	}
	_, err = e.p.Installation(ctx, 1)
	if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeNoAccess {
		t.Errorf("gone installation err = %v", err)
	}
}

func TestJWTAndTokenCaching(t *testing.T) {
	e := setup(t, false)
	for i := 0; i < 3; i++ {
		if _, err := e.p.Repositories(ctx, installation); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.gh.tokenCount.Load(); n != 1 {
		t.Fatalf("token requests = %d, want 1 (cached)", n)
	}
	if body := e.gh.tokenRequests[0].Body; len(body) != 0 {
		t.Errorf("unscoped token body = %v", body)
	}
	// Inside the refresh margin (5 min before expiry) a new one is requested.
	e.gh.mu.Lock()
	e.gh.tokenExpiry = time.Now().Add(time.Hour)
	e.gh.mu.Unlock()
	*e.now = e.now.Add(56 * time.Minute)
	if _, err := e.p.Repositories(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if n := e.gh.tokenCount.Load(); n != 2 {
		t.Fatalf("token requests after expiry margin = %d, want 2", n)
	}
	// The JWT is reused until a minute before its exp, then signed again.
	j1, _ := e.p.appJWT()
	j2, _ := e.p.appJWT()
	if j1 != j2 {
		t.Error("JWT not reused")
	}
	*e.now = e.now.Add(8*time.Minute + 30*time.Second)
	if j3, _ := e.p.appJWT(); j3 == j1 {
		t.Error("JWT not renewed near expiry")
	}
}

func TestScopedTokens(t *testing.T) {
	e := setup(t, false)
	if _, err := e.p.Repository(ctx, installation, repoID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.p.Head(ctx, installation, repoID, "main", ""); err != nil { // same key: cached
		t.Fatal(err)
	}
	if err := e.p.SetStatus(ctx, installation, repoID, sha, source.CommitStatus{State: source.CommitSuccess}); err != nil {
		t.Fatal(err)
	}
	reqs := e.gh.tokenRequests
	if len(reqs) != 2 {
		t.Fatalf("token requests = %d, want 2 (read, status)", len(reqs))
	}
	for i, wantPerms := range []map[string]any{
		{"contents": "read", "metadata": "read"},
		{"statuses": "write"},
	} {
		body := reqs[i].Body
		ids, _ := body["repository_ids"].([]any)
		if len(ids) != 1 || ids[0].(float64) != repoID {
			t.Errorf("request %d repository_ids = %v", i, body["repository_ids"])
		}
		perms, _ := body["permissions"].(map[string]any)
		if fmt.Sprint(perms) != fmt.Sprint(wantPerms) {
			t.Errorf("request %d permissions = %v, want %v", i, perms, wantPerms)
		}
	}
}

func TestTokenDenied(t *testing.T) {
	e := setup(t, false)
	e.gh.tokenStatus = 404
	_, err := e.p.Repositories(ctx, installation)
	if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeNoAccess {
		t.Fatalf("err = %v", err)
	}
	e.gh.tokenStatus = 502
	_, err = e.p.Repositories(ctx, installation)
	if !errx.IsType(err, errx.TypeExternal) || code(t, err) != source.CodeProviderDown {
		t.Fatalf("err = %v", err)
	}
}

func TestRepositories(t *testing.T) {
	e := setup(t, false)
	repos, err := e.p.Repositories(ctx, installation)
	if err != nil {
		t.Fatal(err)
	}
	want := []source.Repo{
		{ID: repoID, FullName: "acme/web", DefaultBranch: "main", Private: true},
		{ID: 9, FullName: "acme/api", DefaultBranch: "trunk"},
	}
	if fmt.Sprint(repos) != fmt.Sprint(want) {
		t.Errorf("repos = %v, want %v", repos, want)
	}
}

func TestRepositoryAndBranches(t *testing.T) {
	e := setup(t, false)
	r, err := e.p.Repository(ctx, installation, repoID)
	if err != nil || r.FullName != "acme/web" || !r.Private {
		t.Fatalf("repo = %+v, %v", r, err)
	}
	_, err = e.p.Repository(ctx, installation, 1)
	if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeNoAccess {
		t.Errorf("unreachable repo err = %v", err)
	}
	bs, err := e.p.Branches(ctx, installation, repoID)
	if err != nil || len(bs) != 2 || bs[0] != (source.Branch{Name: "main", Commit: sha}) {
		t.Fatalf("branches = %v, %v", bs, err)
	}
}

func TestTree(t *testing.T) {
	e := setup(t, false)
	paths, truncated, err := e.p.Tree(ctx, installation, repoID, "main")
	if err != nil || truncated || strings.Join(paths, ",") != "apps/web/package.json,apps/api/go.mod" {
		t.Fatalf("tree = %v %v %v", paths, truncated, err)
	}
	if _, truncated, err := e.p.Tree(ctx, installation, repoID, "huge"); err != nil || !truncated {
		t.Errorf("huge tree truncated=%v err=%v", truncated, err)
	}
	for _, ref := range []string{"gone", "empty"} {
		_, _, err := e.p.Tree(ctx, installation, repoID, ref)
		if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeBranchMissing {
			t.Errorf("%s tree err = %v", ref, err)
		}
	}
}

func TestHead(t *testing.T) {
	e := setup(t, false)
	c, etag, changed, err := e.p.Head(ctx, installation, repoID, "main", "")
	want := source.Commit{SHA: sha, Title: "Fix the total", Author: "anaperez"}
	if err != nil || !changed || c != want || etag != `"etag-1"` {
		t.Fatalf("head = %+v %q %v %v", c, etag, changed, err)
	}
	c, same, changed, err := e.p.Head(ctx, installation, repoID, "main", etag)
	if err != nil || changed || same != etag || c != (source.Commit{}) {
		t.Fatalf("304 head = %+v %q %v %v", c, same, changed, err)
	}
	c, _, _, err = e.p.Head(ctx, installation, repoID, "nologin", "")
	if err != nil || c.Author != "Ana Pérez" {
		t.Errorf("author fallback = %+v %v", c, err)
	}
	_, _, _, err = e.p.Head(ctx, installation, repoID, "gone", "")
	if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeBranchMissing {
		t.Errorf("missing branch err = %v", err)
	}
}

func TestTarballDoesNotForwardAuthorization(t *testing.T) {
	e := setup(t, false)
	body, err := e.p.Tarball(ctx, installation, repoID, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "TARBALL-BYTES" {
		t.Errorf("body = %q", got)
	}
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	if e.gh.codeloadAuth == nil {
		t.Fatal("codeload was not reached")
	}
	if *e.gh.codeloadAuth != "" {
		t.Errorf("codeload received Authorization %q", *e.gh.codeloadAuth)
	}
}

func TestTarballErrors(t *testing.T) {
	e := setup(t, false)
	_, err := e.p.Tarball(ctx, installation, repoID, "missing")
	if !errx.IsType(err, errx.TypeNotFound) || code(t, err) != source.CodeNoAccess {
		t.Errorf("404 err = %v", err)
	}
	// A codeload that answers 500 is the provider being down; its token query never reaches the error.
	e.gh.codeload.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	_, err = e.p.Tarball(ctx, installation, repoID, sha)
	if code(t, err) != source.CodeProviderDown {
		t.Errorf("500 err = %v", err)
	}
	e.gh.codeload.Close()
	_, err = e.p.Tarball(ctx, installation, repoID, sha)
	if code(t, err) != source.CodeProviderDown {
		t.Fatalf("network err = %v", err)
	}
	if strings.Contains(err.Error(), "token=secret") {
		t.Errorf("error leaks the redirect token: %v", err)
	}
}

func TestSetStatus(t *testing.T) {
	e := setup(t, false)
	err := e.p.SetStatus(ctx, installation, repoID, sha, source.CommitStatus{
		State: source.CommitFailure, Description: strings.Repeat("é", 200), TargetURL: "https://console/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.gh.gotStatus
	if got["state"] != "failure" || got["context"] != source.StatusContext || got["target_url"] != "https://console/app" {
		t.Errorf("status body = %v", got)
	}
	if d, _ := got["description"].(string); len([]rune(d)) != 140 {
		t.Errorf("description length = %d, want 140", len([]rune(d)))
	}
}

func TestConfirmInstaller(t *testing.T) {
	user := func(id int64, account string) source.InstallationInfo {
		return source.InstallationInfo{ID: id, Account: account, AccountType: "User"}
	}
	org := func(id int64, account string) source.InstallationInfo {
		return source.InstallationInfo{ID: id, Account: account, AccountType: "Organization"}
	}
	cases := []struct {
		name    string
		code    string
		info    source.InstallationInfo
		arrange func(f *fakeGitHub)
		want    string // "" = confirmed; else the error code
	}{
		{"user account, same login", "good-code", user(installation, "Ana"), func(f *fakeGitHub) { f.login = "ana" }, ""},
		{"user account on a later page", "good-code", user(9, "ana"), func(f *fakeGitHub) { f.login = "ana" }, ""},
		{"user account, other login", "good-code", user(installation, "ana"), func(f *fakeGitHub) { f.login = "eve" }, source.CodeNotYours},
		{"user account, /user refused", "good-code", user(installation, "ana"), func(f *fakeGitHub) { f.userStatus = 403 }, source.CodeNotYours},
		{"org admin", "good-code", org(installation, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{state: "active", role: "admin"}
		}, ""},
		{"org plain member", "good-code", org(installation, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{state: "active", role: "member"}
		}, source.CodeNotYours},
		{"org admin but pending", "good-code", org(installation, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{state: "pending", role: "admin"}
		}, source.CodeNotYours},
		{"org membership 404", "good-code", org(installation, "acme"), func(f *fakeGitHub) {}, source.CodeNotYours},
		{"org membership 403", "good-code", org(installation, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{status: 403}
		}, source.CodeNotYours},
		{"org membership 5xx", "good-code", org(installation, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{status: 503}
		}, source.CodeProviderDown},
		{"user 5xx", "good-code", user(installation, "ana"), func(f *fakeGitHub) { f.userStatus = 500 }, source.CodeProviderDown},
		{"installation not listed", "good-code", org(55, "acme"), func(f *fakeGitHub) {
			f.memberships["acme"] = membership{state: "active", role: "admin"}
		}, source.CodeNotYours},
		{"unknown account type", "good-code", source.InstallationInfo{ID: installation, Account: "x"}, func(f *fakeGitHub) {}, source.CodeNotYours},
		{"bad code", "bad-code", org(installation, "acme"), func(f *fakeGitHub) {}, source.CodeNotYours},
		{"empty code", "", org(installation, "acme"), func(f *fakeGitHub) {}, source.CodeNotYours},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, false)
			e.gh.memberships = map[string]membership{}
			tc.arrange(e.gh)
			err := e.p.ConfirmInstaller(ctx, tc.code, tc.info)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || code(t, err) != tc.want {
				t.Fatalf("err = %v, want code %s", err, tc.want)
			}
			if tc.want == source.CodeNotYours && !errx.IsType(err, errx.TypeAuthorization) {
				t.Errorf("type of %v is not authorization", err)
			}
		})
	}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	e := setup(t, false)
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	if err := e.p.VerifyWebhook(sign(webhookKey, body), body); err != nil {
		t.Fatalf("good signature: %v", err)
	}
	for name, sig := range map[string]string{
		"empty":      "",
		"no prefix":  strings.TrimPrefix(sign(webhookKey, body), "sha256="),
		"sha1":       "sha1=" + strings.TrimPrefix(sign(webhookKey, body), "sha256="),
		"not hex":    "sha256=zzzz",
		"short":      "sha256=abcd",
		"wrong key":  sign("other", body),
		"other body": sign(webhookKey, []byte("x")),
	} {
		err := e.p.VerifyWebhook(sig, body)
		if !errx.IsType(err, errx.TypeAuthorization) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPushEvent(t *testing.T) {
	e := setup(t, false)
	ev, err := e.p.PushEvent(fixture(t, "push.json"))
	want := source.PushEvent{Installation: 7001, RepositoryID: repoID, Repository: "acme/web", Branch: "main",
		Head: source.Commit{SHA: sha, Title: "Fix the checkout total", Author: "anaperez"}, Sender: "anaperez"}
	if err != nil || ev != want {
		t.Fatalf("push = %+v, %v", ev, err)
	}

	ev, err = e.p.PushEvent(fixture(t, "push_tag.json"))
	if err != nil || ev.Branch != "" || ev.Head.SHA != "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00" || ev.Head.Author != "Bo Díaz" {
		t.Errorf("tag push = %+v, %v", ev, err)
	}

	ev, err = e.p.PushEvent(fixture(t, "push_deleted.json"))
	if err != nil || !ev.Deleted || ev.Branch != "feature/login" || ev.Head.SHA != "0000000000000000000000000000000000000000" {
		t.Errorf("deleted push = %+v, %v", ev, err)
	}

	for name, in := range map[string]string{"garbage": "{", "no repo": `{"ref":"refs/heads/x","installation":{"id":1}}`, "no installation": `{"repository":{"id":1}}`} {
		if _, err := e.p.PushEvent([]byte(in)); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInstallationEvent(t *testing.T) {
	e := setup(t, false)
	ev, err := e.p.InstallationEvent("installation", fixture(t, "installation_deleted.json"))
	if err != nil || ev.Installation != 7001 || ev.Action != "deleted" || len(ev.Removed) != 0 {
		t.Fatalf("installation = %+v, %v", ev, err)
	}
	ev, err = e.p.InstallationEvent("installation_repositories", fixture(t, "installation_repositories_removed.json"))
	if err != nil || ev.Installation != 7001 || ev.Action != "removed" || fmt.Sprint(ev.Removed) != "[424242 424243]" {
		t.Fatalf("installation_repositories = %+v, %v", ev, err)
	}
	if _, err := e.p.InstallationEvent("installation", []byte("{")); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("malformed err = %v", err)
	}
	if _, err := e.p.InstallationEvent("push", []byte("{}")); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("wrong event err = %v", err)
	}
}

func TestRateLimitAndServerErrorsAreProviderDown(t *testing.T) {
	e := setup(t, false)
	e.gh.codeload.Close()
	e.gh.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"token":"t","expires_at":"2099-01-01T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
		default:
			w.WriteHeader(503)
		}
	})
	_, err := e.p.Repositories(ctx, installation)
	if !errx.IsType(err, errx.TypeExternal) || code(t, err) != source.CodeProviderDown {
		t.Errorf("rate limit err = %v", err)
	}
	_, err = e.p.Repository(ctx, installation, repoID)
	if !errx.IsType(err, errx.TypeExternal) || code(t, err) != source.CodeProviderDown {
		t.Errorf("503 err = %v", err)
	}
}
