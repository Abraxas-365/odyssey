package httpx_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/gofiber/fiber/v2"
)

const (
	issuer   = "http://iam.public.test" // what tokens say; never served
	resource = "http://localhost:8095/mcp"
	orgID    = "0f8e6c1a-6b52-4b8e-9d55-4a0c5b6d7e01"
	userID   = "11111111-1111-4111-8111-111111111111"
	client   = "https://claude.ai/oauth/claude-code-client-metadata"
)

// fakeIAM serves IAMKit's JWKS and introspection at its own URL (the
// "internal" name): the issuer's requests must be sent there.
type fakeIAM struct {
	key      *rsa.PrivateKey
	srv      *httptest.Server
	down     atomic.Bool // introspection answers 503
	inactive atomic.Bool
	calls    atomic.Int32
}

func newFakeIAM(t *testing.T) *fakeIAM {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIAM{key: key}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
				"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
		case "/oauth/introspect":
			f.calls.Add(1)
			id, secret, ok := r.BasicAuth()
			if !ok || id != "rs-client" || secret != "rs-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if f.down.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			claims := parse(t, r.FormValue("token"))
			json.NewEncoder(w).Encode(map[string]any{"active": !f.inactive.Load(), "token_use": "access_token", "sub": claims["sub"],
				"sid": claims["sid"], "client_id": claims["client_id"], "scope": claims["scope"], "aud": claims["aud"]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// token signs an access token for the resource, edited.
func (f *fakeIAM) token(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	c := map[string]any{"iss": issuer, "sub": userID, "aud": []string{resource}, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"environment_id": "env-1", "organization_id": orgID, "application_id": "app-1", "resource_id": "res-1", "sid": "s-1",
		"purpose": "application", "client_id": client, "oauth_client_id": client,
		"scp": []string{"odyssey:projects:read", "offline_access"}, "scope": "odyssey:projects:read offline_access", "resource": resource}
	if edit != nil {
		edit(c)
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, _ := json.Marshal(c)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func parse(t *testing.T, token string) map[string]any {
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Error(err)
	}
	return out
}

func iamConfig(f *fakeIAM) config.IAM {
	return config.IAM{URL: f.srv.URL, Issuer: issuer, Audience: resource, Environment: "env-1", Application: "app-1", Resource: "res-1"}
}

// mcpApp mounts MCPAuth like server.New, in front of a handler that records
// what reached it.
func mcpApp(t *testing.T, f *fakeIAM) (*fiber.App, *atomic.Pointer[mcpx.Principal], *atomic.Pointer[event.Meta]) {
	t.Helper()
	auth, err := httpx.NewMCPAuth(iamConfig(f), config.MCP{IntrospectionClientID: "rs-client", IntrospectionClientSecret: "rs-secret"},
		[]string{"odyssey:projects:read"})
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Pointer[mcpx.Principal]
	var meta atomic.Pointer[event.Meta]
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := mcpx.FromContext(r.Context()); ok {
			seen.Store(&p)
		}
		m := event.MetaFrom(r.Context())
		meta.Store(&m)
		if r.Header.Get("Authorization") != "Bearer "+mcpx.VerifiedBearer {
			t.Error("the agent's token reached the tools")
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(httpx.RequestID)
	for _, p := range auth.MetadataPaths() {
		app.All(p, auth.Metadata())
	}
	app.All(httpx.MCPPath, auth.Protect(next))
	return app, &seen, &meta
}

func call(t *testing.T, app *fiber.App, method, path, token string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-7")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := app.Test(req, 5000)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMCPAuthAdmitsTheResourcesTokens(t *testing.T) {
	f := newFakeIAM(t)
	app, seen, meta := mcpApp(t, f)
	token := f.token(t, nil)
	res := call(t, app, http.MethodPost, "/mcp", token)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ping"`) {
		t.Fatalf("valid token: %d %s", res.StatusCode, body)
	}
	p := seen.Load()
	if p == nil || p.Organization.String() != orgID || p.User.String() != userID || p.Client != client ||
		len(p.Permissions) != 1 || p.Permissions[0] != "odyssey:projects:read" {
		t.Fatalf("principal: %+v", p)
	}
	if m := meta.Load(); m.CorrelationID != "req-7" || m.Client != "mcp:"+client {
		t.Fatalf("meta: %+v", m)
	}
	// The introspection answer is cached: a burst costs one call.
	call(t, app, http.MethodPost, "/mcp", token)
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("introspected %d times for one token", n)
	}
}

func TestMCPAuthRefuses(t *testing.T) {
	f := newFakeIAM(t)
	app, seen, _ := mcpApp(t, f)
	for name, edit := range map[string]func(map[string]any){
		"no resource claim (console token)": func(c map[string]any) { delete(c, "resource") },
		"other audience": func(c map[string]any) {
			c["aud"] = []string{"http://localhost:8095"}
			c["resource"] = "http://localhost:8095"
		},
		"other application":     func(c map[string]any) { c["application_id"] = "app-2" },
		"other resource":        func(c map[string]any) { c["resource_id"] = "res-2" },
		"other environment":     func(c map[string]any) { c["environment_id"] = "env-2" },
		"expired":               func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"subject not a user id": func(c map[string]any) { c["sub"] = "svc-1" },
	} {
		res := call(t, app, http.MethodPost, "/mcp", f.token(t, edit))
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, res.StatusCode)
		}
	}
	res := call(t, app, http.MethodPost, "/mcp", "")
	challenge := res.Header.Get("WWW-Authenticate")
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(challenge, `resource_metadata="http://localhost:8095/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("no token: %d %q", res.StatusCode, challenge)
	}
	f.inactive.Store(true)
	if res := call(t, app, http.MethodPost, "/mcp", f.token(t, func(c map[string]any) { c["sid"] = "s-revoked" })); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked: %d", res.StatusCode)
	}
	f.inactive.Store(false)
	f.down.Store(true)
	res = call(t, app, http.MethodPost, "/mcp", f.token(t, func(c map[string]any) { c["sid"] = "s-down" }))
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Fatalf("IAMKit down: %d retry-after %q (fail closed)", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res := call(t, app, http.MethodGet, "/mcp", f.token(t, nil)); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", res.StatusCode)
	}
	if seen.Load() != nil {
		t.Fatal("a refused request reached the tools")
	}
}

// The resource must be the endpoint's URL: metadata naming another one
// would make agents refuse the server, with no hint why.
func TestMCPAuthNeedsTheEndpointAsAudience(t *testing.T) {
	f := newFakeIAM(t)
	m := config.MCP{IntrospectionClientID: "rs-client", IntrospectionClientSecret: "rs-secret"}
	for _, aud := range []string{"http://localhost:8095", "http://localhost:8095/", "http://localhost:8095/api/mcp"} {
		b := iamConfig(f)
		b.Audience = aud
		if _, err := httpx.NewMCPAuth(b, m, nil); err == nil {
			t.Errorf("audience %q accepted", aud)
		}
	}
	if _, err := httpx.NewMCPAuth(iamConfig(f), config.MCP{}, nil); err == nil {
		t.Error("no introspection client accepted")
	}
}

func TestMCPMetadata(t *testing.T) {
	f := newFakeIAM(t)
	app, _, _ := mcpApp(t, f)
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		res := call(t, app, http.MethodGet, path, "")
		var m struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			Scopes               []string `json:"scopes_supported"`
		}
		json.NewDecoder(res.Body).Decode(&m)
		if res.StatusCode != http.StatusOK || m.Resource != resource || len(m.AuthorizationServers) != 1 || m.AuthorizationServers[0] != issuer ||
			len(m.Scopes) != 1 || res.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s: %d %+v", path, res.StatusCode, m)
		}
	}
}

// introspector answers like a live IAMKit: the token's client, any token.
type introspector struct{}

func (introspector) Introspect(_ context.Context, token, _, _, _, _, _ string) (*authclient.Claims, error) {
	c := &authclient.Claims{OrganizationID: orgID, Purpose: "application", Permissions: []string{"odyssey:projects:read"}}
	c.Subject = userID
	if id, ok := parseRaw(token)["oauth_client_id"].(string); ok {
		c.OAuthClientID = id
	}
	return c, nil
}

func parseRaw(token string) map[string]any {
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

const webClient = "91d14ace-8dee-474c-ba8e-00953161c3da"

// The REST API takes only the console's tokens: an agent's /mcp token (same
// audience) is refused, and so is a token of an agent's registered client
// that left out the resource (no claim to tell it apart).
func TestRESTRefusesMCPTokens(t *testing.T) {
	f := newFakeIAM(t)
	b := iamConfig(f)
	b.WebClientID = webClient
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/x", httpx.Authenticate(introspector{}, b), func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	console := func(c map[string]any) {
		delete(c, "resource")
		c["oauth_client_id"], c["client_id"] = webClient, webClient
	}
	for name, tc := range map[string]struct {
		token string
		want  int
	}{
		"console token":          {f.token(t, console), http.StatusNoContent},
		"direct sign-in":         {f.token(t, func(c map[string]any) { delete(c, "resource"); delete(c, "oauth_client_id") }), http.StatusNoContent},
		"console, null resource": {f.token(t, func(c map[string]any) { console(c); c["resource"] = nil }), http.StatusNoContent},
		"agent (MCP) token":      {f.token(t, nil), http.StatusUnauthorized},
		"agent, console's id":    {f.token(t, func(c map[string]any) { c["oauth_client_id"] = webClient }), http.StatusUnauthorized},
		"agent without resource": {f.token(t, func(c map[string]any) { delete(c, "resource"); c["oauth_client_id"] = "0b5c6a8e-template-client" }), http.StatusUnauthorized},
		"resource as a list":     {f.token(t, func(c map[string]any) { console(c); c["resource"] = []string{resource} }), http.StatusUnauthorized},
	} {
		if res := call(t, app, http.MethodGet, "/x", tc.token); res.StatusCode != tc.want {
			t.Errorf("%s: %d, want %d", name, res.StatusCode, tc.want)
		}
	}
	// Without a configured console client only direct sign-ins pass.
	b.WebClientID = ""
	app = fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/x", httpx.Authenticate(introspector{}, b), func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	if res := call(t, app, http.MethodGet, "/x", f.token(t, console)); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unconfigured console client: %d", res.StatusCode)
	}
}
