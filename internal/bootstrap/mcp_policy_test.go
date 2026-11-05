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
	// call runs a tool: its error body (isError) or its structured result.
	call := func(name string, args map[string]any) (map[string]any, map[string]string) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !res.IsError {
			var out map[string]any
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatal(err)
			}
			return out, nil
		}
		var body struct{ Error map[string]string }
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			t.Fatal(err)
		}
		return nil, body.Error
	}
	// setAccess confirms what asks for it (the assistant already asked the
	// user); see TestMCPConfirmsWhatIsHardToUndo for the confirmation itself.
	setAccess := func(mode string) (map[string]any, map[string]string) {
		t.Helper()
		return call("set_access", map[string]any{"app": "portal", "mode": mode, "confirm": true})
	}

	// A sandbox app cannot be public: denied, with the rule's name and a stop.
	_, got := setAccess("public")
	if got["code"] != gate.CodeDenied || !strings.Contains(got["message"], "«Sandbox no puede ser pública»") ||
		!strings.Contains(got["next_step"], "no reintentes") {
		t.Fatalf("denied: %v", got)
	}

	// An internal one asks for approval (the built-in): not an error but a
	// pending_approval result with the request to follow, and nothing changed.
	if err := platform.Projects.Commands.SetTier(ctx, org, id, project.SetTier{Tier: project.TierInternal, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	pending, errBody := setAccess("public")
	if errBody != nil || pending["status"] != "pending_approval" || pending["request_id"] == "" ||
		!strings.Contains(pending["next_step"].(string), "aprobación de TI") || !strings.Contains(pending["next_step"].(string), "No lo reintentes") {
		t.Fatalf("pending: %v / %v", pending, errBody)
	}

	// get_request follows it, and list_requests shows it.
	req, errBody := call("get_request", map[string]any{"request_id": pending["request_id"]})
	if errBody != nil || req["status"] != "pending" || req["action"] != "set_access" || req["done"] != false ||
		!strings.Contains(req["reason"].(string), "pide aprobación") {
		t.Fatalf("get_request: %v / %v", req, errBody)
	}
	list, errBody := call("list_requests", map[string]any{"status": "pending"})
	if errBody != nil || len(list["items"].([]any)) != 1 {
		t.Fatalf("list_requests: %v / %v", list, errBody)
	}
	if _, errBody := call("get_request", map[string]any{"request_id": identity.NewOrganizationID().String()}); errBody["code"] != "NOT_FOUND" {
		t.Fatalf("a request that is not theirs: %v", errBody)
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
	if _, got := setAccess("organization"); got["code"] != gate.CodeDenied || !strings.Contains(got["message"], "Solo quien cambia niveles") {
		t.Fatalf("actor permissions: %v", got)
	}
	if out, got := setAccess("team"); got != nil || out["status"] == "pending_approval" {
		t.Fatalf("a mode no rule matches must go through: %v / %v", out, got)
	}
}

// TestMCPConfirmsWhatIsHardToUndo: opening an app to anyone with the link, and
// going back in a production app, need the user's yes first. A client that
// cannot be asked (this endpoint is stateless) gets CONFIRMATION_REQUIRED with
// a next_step to ask the user and repeat with confirm: true; nothing changes
// until then.
func TestMCPConfirmsWhatIsHardToUndo(t *testing.T) {
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
	if _, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: "Portal", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	// no rule holds anything here: only the confirmation is under test
	rules, err := platform.Policy.Queries.List(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	for _, r := range rules {
		if _, err := platform.Policy.Commands.Replace(ctx, org, r.ID, policy.Replace{UpdatedBy: user, Draft: policy.Draft{
			Name: r.Name, Description: r.Description, Expression: r.Expression, Effect: r.Effect, Approvers: r.Approvers, Enabled: &off}}); err != nil {
			t.Fatal(err)
		}
	}

	perms := []string{permission.ProjectsRead, permission.ProjectsWrite, permission.AccessPublic}
	caller := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/oauth/claude-code-client-metadata", Permissions: perms}
	h := bootstrap.NewMCPHandler(platform, config.Config{ConsoleURL: "http://localhost:5178"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), caller)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "confirm", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	set := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		args["app"] = "portal"
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_access", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := set(map[string]any{"mode": "public"})
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, mcpx.CodeConfirmationRequired) || !strings.Contains(text, "confirm: true") {
		t.Fatalf("public without confirming: %s", text)
	}
	if res := set(map[string]any{"mode": "team"}); res.IsError { // the safe direction asks nothing
		t.Fatalf("team asked for confirmation: %s", res.Content[0].(*mcp.TextContent).Text)
	}
	if res := set(map[string]any{"mode": "public", "confirm": true}); res.IsError {
		t.Fatalf("confirmed public: %s", res.Content[0].(*mcp.TextContent).Text)
	}
}
