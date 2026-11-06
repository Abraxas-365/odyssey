package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// fakeConnectedApps is IAMKit's /identity/v1/me/connected-apps for one
// organization's user: Claude Code connected to Odicea's MCP server, the
// console (Odicea's own client, IAMKIT_WEB_CLIENT_ID) and an assistant of
// another server.
type fakeConnectedApps struct {
	org, audience string
	mu            sync.Mutex
	tokens        []string
	deleted       []string
}

func (f *fakeConnectedApps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	if r.URL.Query().Get("organization_id") != f.org {
		_, _ = w.Write([]byte(`{"items":[]}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/identity/v1/me/connected-apps":
		_, _ = w.Write([]byte(`{"items":[
		 {"client":{"id":"c1","client_id":"https://claude.ai/oauth/claude-code-client-metadata"},"resource":"` + f.audience + `",
		  "consented_at":"2026-11-04T00:35:56Z","last_used_at":"2026-11-05T10:00:00Z","active_sessions":1},
		 {"client":{"id":"console","client_id":"web"},"resource":"` + f.audience + `","consented_at":null,"active_sessions":1},
		 {"client":{"id":"c9","client_id":"x"},"resource":"https://elsewhere.example.com/mcp","consented_at":"2026-11-04T00:00:00Z"}]}`))
	case r.Method == http.MethodDelete && r.URL.Path == "/identity/v1/me/connected-apps/c1":
		f.deleted = append(f.deleted, "c1")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"not found","http_status":404}`))
	}
}

func TestMe(t *testing.T) {
	platform := mcpPlatform(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	const audience = "https://odicea.example.com/mcp"
	iam := &fakeConnectedApps{org: org.String(), audience: audience}
	srv := httptest.NewServer(iam)
	t.Cleanup(srv.Close)

	cfg := config.Config{}
	cfg.IAM.URL, cfg.IAM.Audience, cfg.IAM.Environment, cfg.IAM.WebClientID = srv.URL, audience, "env", "web"
	cfg.MCP = config.MCP{IntrospectionClientID: "rs", IntrospectionClientSecret: "s"}
	app := bootstrap.NewServer(platform, cfg, httpxtest.Auth(), nil)

	type meBody struct {
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
		Assistant   *struct {
			Connected []struct{ ID, Key, Name string }
			Can       []string `json:"can"`
			Cannot    []string `json:"cannot"`
		} `json:"assistant"`
	}

	var lector []string
	for _, r := range permission.Roles {
		if r.Name == "Lector" {
			lector = r.Permissions
		}
	}
	token := httpxtest.Token(org, lector...)
	res := checked(t, app, http.MethodGet, "/api/v1/me", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	var me meBody
	if err := json.Unmarshal(res.Body, &me); err != nil {
		t.Fatal(err)
	}
	if me.Role != "Lector" || me.Assistant == nil || len(me.Assistant.Connected) != 1 ||
		me.Assistant.Connected[0].ID != "c1" || me.Assistant.Connected[0].Key != assistant.ClaudeCode {
		t.Fatalf("me: %s", res.Body)
	}
	// a reader's assistant reads: it is told it cannot publish
	if !slices.Contains(me.Assistant.Can, assistant.Capabilities[0].Can) || !slices.Contains(me.Assistant.Cannot, assistant.Capabilities[4].Can) {
		t.Fatalf("abilities: %s", res.Body)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control %q", res.Header.Get("Cache-Control"))
	}
	if iam.tokens[0] != "Bearer "+token {
		t.Fatalf("IAMKit is asked with the user's own token: %q", iam.tokens[0])
	}

	// any user, even with no permission, asks about themselves
	res = checked(t, app, http.MethodGet, "/api/v1/me", httpxtest.Token(org), nil)
	httpxtest.Want(t, res, http.StatusOK)
	if !strings.Contains(string(res.Body), `"role":""`) || !strings.Contains(string(res.Body), `"permissions":[]`) {
		t.Fatalf("no permissions: %s", res.Body)
	}

	// tenant isolation: another organization's user sees its own (none here)
	res = checked(t, app, http.MethodGet, "/api/v1/me", httpxtest.Token(other, permission.ProjectsRead), nil)
	httpxtest.Want(t, res, http.StatusOK)
	if !strings.Contains(string(res.Body), `"connected":[]`) {
		t.Fatalf("other org: %s", res.Body)
	}
	// ...and cannot disconnect this organization's assistant
	res = checked(t, app, http.MethodDelete, "/api/v1/me/assistants/c1", httpxtest.Token(other), nil)
	httpxtest.Want(t, res, http.StatusNotFound)

	// disconnect: only Odicea's assistants
	for _, id := range []string{"console", "c9"} {
		res = checked(t, app, http.MethodDelete, "/api/v1/me/assistants/"+id, token, nil)
		httpxtest.Want(t, res, http.StatusNotFound)
	}
	res = checked(t, app, http.MethodDelete, "/api/v1/me/assistants/c1", token, nil)
	httpxtest.Want(t, res, http.StatusNoContent)
	if !slices.Equal(iam.deleted, []string{"c1"}) {
		t.Fatalf("deleted %v", iam.deleted)
	}

	// IAMKit down: /me still answers, saying nothing about assistants
	srv.Close()
	res = checked(t, app, http.MethodGet, "/api/v1/me", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if !strings.Contains(string(res.Body), `"assistant":null`) || !strings.Contains(string(res.Body), `"role":"Lector"`) {
		t.Fatalf("IAMKit down: %s", res.Body)
	}
	res = checked(t, app, http.MethodDelete, "/api/v1/me/assistants/c1", token, nil)
	httpxtest.Want(t, res, http.StatusBadGateway)

	// MCP off: no assistant part at all
	off := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	res = checked(t, off, http.MethodGet, "/api/v1/me", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if !strings.Contains(string(res.Body), `"assistant":null`) {
		t.Fatalf("MCP off: %s", res.Body)
	}
}
