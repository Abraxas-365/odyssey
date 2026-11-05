package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifake"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPOutputsDoNotShadowPending: the architecture test for the pattern. A
// tool result that embeds mcpx.Pending must not declare a field with the same
// JSON name (`status`, `request_id`): encoding/json would let the shallower
// one win and a held result would serialise status "" (enable_ai did).
func TestMCPOutputsDoNotShadowPending(t *testing.T) {
	embedding := 0
	for _, tool := range bootstrap.MCPTools(mcpPlatform(t), config.Config{ConsoleURL: "http://localhost:5178"}) {
		out := tool.OutputType()
		if out == nil {
			t.Errorf("tool %s has no output type (built without mcpx.Typed?)", tool.Name)
			continue
		}
		if c := mcpx.PendingConflicts(out); len(c) != 0 {
			t.Errorf("tool %s: output %s embeds mcpx.Pending and also declares %v: the held result would lose them", tool.Name, out, c)
		}
		for i := 0; i < out.NumField(); i++ {
			if f := out.Field(i); f.Anonymous && f.Name == "Pending" {
				embedding++
			}
		}
	}
	if embedding == 0 {
		t.Error("no tool embeds mcpx.Pending any more: is the check still looking at the right thing?")
	}
}

// TestMCPEnableAIHeldIsPendingApproval: a held enable_ai answers status
// "pending_approval" with the request to follow (it once serialised ""), and
// get_request / list_requests link to the console's Bandeja.
func TestMCPEnableAIHeldIsPendingApproval(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, _ := aiStack(t, cluster, aifake.New())
	ctx := context.Background()
	org := h.target.Organization
	user := identity.MustParseUserID("11111111-1111-4111-8111-111111111111")
	if _, err := h.platform.Policy.Commands.Create(ctx, org, policy.Create{CreatedBy: user, Draft: policy.Draft{
		Name: "La IA se aprueba", Expression: "action == 'enable_ai'", Effect: policy.EffectApproval}}); err != nil {
		t.Fatal(err)
	}

	caller := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/oauth/claude-code-client-metadata",
		Permissions: []string{permission.ProjectsRead, permission.ProjectsWrite}}
	mh := bootstrap.NewMCPHandler(h.platform, config.Config{ConsoleURL: "http://localhost:5178"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
		mh.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), caller)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "pending", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("%s failed: %s", name, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := call("enable_ai", map[string]any{"app": "pagos"})
	if out["status"] != "pending_approval" || out["request_id"] == "" || out["request_id"] == nil ||
		!strings.Contains(out["next_step"].(string), "aprobación de TI") {
		t.Fatalf("held enable_ai: %v", out)
	}
	if keys := h.gateway.Live(); len(keys) != 0 {
		t.Fatalf("a held enable_ai made keys: %v", keys)
	}

	req := call("get_request", map[string]any{"request_id": out["request_id"]})
	if req["status"] != "pending" || req["action"] != "enable_ai" || req["console_url"] != "http://localhost:5178/bandeja?id="+out["request_id"].(string) {
		t.Fatalf("get_request: %v", req)
	}
	list := call("list_requests", map[string]any{"status": "pending"})
	if list["console_url"] != "http://localhost:5178/bandeja" {
		t.Fatalf("list_requests console_url: %v", list["console_url"])
	}
}
