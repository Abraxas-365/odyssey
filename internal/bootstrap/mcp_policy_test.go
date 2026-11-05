package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPToolsAnswerITsRules: an assistant that asks for something IT's rules
// refuse gets POLICY_DENIED / APPROVAL_REQUIRED as a tool error, with IT's
// message untouched and a next_step that says to show it and stop. The actor
// the rules see is the connection's consented permissions (not the user's).
func TestMCPToolsAnswerITsRules(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })

	org := identity.NewOrganizationID()
	user := identity.MustParseUserID("11111111-1111-4111-8111-111111111111")
	id, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.Projects.Commands.SetTier(ctx, org, id, project.SetTier{Tier: project.TierSandbox, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}

	perms := []string{permission.ProjectsRead, permission.ProjectsWrite, permission.AccessPublic}
	caller := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/oauth/claude-code-client-metadata", Permissions: perms}
	h := bootstrap.NewMCPHandler(platform, config.Config{ConsoleURL: "http://localhost:5178"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), caller)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "policy", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	setAccess := func(mode string) map[string]string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_access", Arguments: map[string]any{"app": "portal", "mode": mode}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			return nil
		}
		var body struct{ Error map[string]string }
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
			t.Fatal(err)
		}
		return body.Error
	}

	// A sandbox app cannot be public: denied, with the rule's name and a stop.
	got := setAccess("public")
	if got["code"] != gate.CodeDenied || !strings.Contains(got["message"], "«Sandbox no puede ser pública»") ||
		!strings.Contains(got["next_step"], "no reintentes") {
		t.Fatalf("denied: %v", got)
	}

	// An internal one asks for approval (the built-in), also as a tool error.
	if err := platform.Projects.Commands.SetTier(ctx, org, id, project.SetTier{Tier: project.TierInternal, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	got = setAccess("public")
	if got["code"] != gate.CodeApprovalRequired || !strings.Contains(got["next_step"], "aprobación de TI") {
		t.Fatalf("approval: %v", got)
	}

	// The rules read the connection's permissions: this one has no tier
	// permission, so a rule about it matches, and nothing else changes.
	_, err = platform.Policy.Commands.Create(ctx, org, policy.Create{CreatedBy: user, Draft: policy.Draft{
		Name:       "Solo quien cambia niveles abre la app a la empresa",
		Expression: "action == 'set_access' && access.mode == 'organization' && !('" + permission.ProjectsTier + "' in actor.permissions)",
		Effect:     policy.EffectDeny}})
	if err != nil {
		t.Fatal(err)
	}
	if got := setAccess("organization"); got["code"] != gate.CodeDenied || !strings.Contains(got["message"], "Solo quien cambia niveles") {
		t.Fatalf("actor permissions: %v", got)
	}
	if got := setAccess("team"); got != nil {
		t.Fatalf("a mode no rule matches must go through: %v", got)
	}
}
