package accountiam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/account/adapters/accountiam"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

const mcp = "https://odicea.example.com/mcp"

// iamkit answers /identity/v1/me/connected-apps as IAMKit does, for the
// user whose token is "tok", in the organization asked.
func iamkit(t *testing.T, org identity.OrganizationID, deleted *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	check := func(w http.ResponseWriter, r *http.Request) bool {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		if q.Get("environment_id") != "env" || q.Get("audience") != mcp || q.Get("organization_id") != org.String() {
			t.Errorf("query %v", q)
		}
		return true
	}
	mux.HandleFunc("GET /identity/v1/me/connected-apps", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"items":[
		 {"client":{"id":"c1","client_id":"https://claude.ai/oauth/claude-code-client-metadata"},"resource":"` + mcp + `",
		  "consented_at":"2026-11-04T00:35:56Z","last_used_at":"2026-11-05T10:00:00Z"},
		 {"client":{"id":"console","client_id":"web"},"resource":"` + mcp + `","consented_at":null,"active_sessions":2},
		 {"client":{"id":"c2","client_id":"cursor"},"resource":"` + mcp + `","consented_at":null,"active_sessions":1,"last_used_at":"2026-11-05T09:00:00Z"},
		 {"client":{"id":"c3","client_id":"x"},"resource":"https://other.example.com/mcp","consented_at":"2026-11-04T00:00:00Z"}
		]}`))
	})
	mux.HandleFunc("DELETE /identity/v1/me/connected-apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		switch r.PathValue("id") {
		case "c1":
		case "impersonated":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "FORBIDDEN", "message": "impersonated token (internal detail)", "http_status": 403})
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found", "http_status": 404})
			return
		}
		*deleted = append(*deleted, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConnections(t *testing.T) {
	ctx := context.Background()
	org := identity.NewOrganizationID()
	var deleted []string
	srv := iamkit(t, org, &deleted)
	c := accountiam.Connections{API: authclient.New(srv.URL), Environment: "env", Audience: mcp, Resource: mcp, FirstParty: []string{"web"}}

	got, err := c.List(ctx, org, "tok")
	if err != nil {
		t.Fatal(err)
	}
	// Odicea's MCP server only, and not the console; an assistant the
	// administrator granted (no consent of the user's) is connected too
	if len(got) != 2 || got[0].ID != "c1" || got[0].ClientID != "https://claude.ai/oauth/claude-code-client-metadata" ||
		got[0].ConsentedAt == nil || got[0].LastUsedAt == nil || got[1].ID != "c2" || got[1].ConsentedAt != nil {
		t.Fatalf("list %+v", got)
	}
	if err := c.Disconnect(ctx, org, "tok", "c1"); err != nil || len(deleted) != 1 {
		t.Fatalf("disconnect %v %v", err, deleted)
	}
	if err := c.Disconnect(ctx, org, "tok", "other"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// IAMKit's refusal is kept, without its own words
	if err := c.Disconnect(ctx, org, "tok", "impersonated"); code(err) != errx.CodeForbidden || strings.Contains(err.Error(), "internal detail") {
		t.Fatalf("forbidden: %v", err)
	}
	// an expired session is the user's to fix (sign in), not IAMKit failing
	if err := c.Disconnect(ctx, org, "bad", "c1"); code(err) != errx.CodeUnauthorized {
		t.Fatalf("expired: %v", err)
	}
	// IAMKit refusing or down is IAMKit failing, never an empty list
	if _, err := c.List(ctx, org, "bad"); err == nil {
		t.Fatal("bad token listed")
	}
	srv.Close()
	if _, err := c.List(ctx, org, "tok"); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("down: %v", err)
	}
}

func code(err error) string {
	var e *errx.Error
	if !errors.As(err, &e) {
		return ""
	}
	return e.Code
}
