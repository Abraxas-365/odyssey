package mcpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string `json:"text" jsonschema:"what to echo"`
}
type echoOut struct {
	Text   string `json:"text"`
	Client string `json:"client"`
	Meta   string `json:"meta"`
}

func tools(t *testing.T) *mcpx.Registry {
	t.Helper()
	echo := mcpx.Typed(mcpx.Tool{Name: "echo", Title: "Echo", Description: "echoes", Toolset: "test",
		Permissions: []string{"p:read"}, Risk: mcpx.RiskRead},
		func(ctx context.Context, caller mcpx.Principal, in echoIn) (echoOut, error) {
			return echoOut{Text: in.Text, Client: caller.Client, Meta: event.MetaFrom(ctx).Client}, nil
		})
	fail := mcpx.Typed(mcpx.Tool{Name: "fail", Description: "fails", Permissions: []string{"p:read"}, Risk: mcpx.RiskWrite,
		Annotations: mcpx.Annotations{Destructive: true}},
		func(_ context.Context, _ mcpx.Principal, in echoIn) (echoOut, error) {
			switch in.Text {
			case "internal":
				return echoOut{}, errors.New("db password leaked in a driver error")
			case "default":
				return echoOut{}, errx.NotFound("app not found")
			case "denied":
				return echoOut{}, gate.Verdict{Effect: gate.Deny, Rules: []gate.Rule{{ID: "r1", Name: "Sandbox no puede ser pública"}}}.Err()
			case "approval":
				return echoOut{}, gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{ID: "r2", Name: "Publicar en producción"}}, Approvers: []string{"odyssey:approvals:decide"}}.Err()
			}
			return echoOut{}, mcpx.NextStep(errx.Conflict("slug taken").WithDetail("field", "name"), "Usa get_app.")
		})
	write := mcpx.Typed(mcpx.Tool{Name: "write_it", Description: "writes", Permissions: []string{"p:read", "p:write"}, Risk: mcpx.RiskWrite},
		func(context.Context, mcpx.Principal, echoIn) (echoOut, error) { return echoOut{}, nil })
	r, err := mcpx.NewRegistry([]mcpx.Tool{echo, fail}, []mcpx.Tool{write})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// connect serves the registry behind a fake authenticator that admits
// principal (nil: lets the request through unauthenticated).
func connect(t *testing.T, r *mcpx.Registry, principal *mcpx.Principal) *mcp.ClientSession {
	t.Helper()
	h := mcpx.NewHandler(r, mcpx.Options{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := event.WithMeta(req.Context(), event.Meta{CorrelationID: "req-1", Client: "mcp:" + "https://claude.ai/cimd"})
		if principal != nil {
			ctx = mcpx.WithPrincipal(ctx, *principal)
		}
		h.ServeHTTP(w, req.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer opaque")
	return http.DefaultTransport.RoundTrip(r)
}

func principal(perms ...string) *mcpx.Principal {
	return &mcpx.Principal{Organization: identity.NewOrganizationID(), User: identity.MustParseUserID("11111111-1111-4111-8111-111111111111"),
		Client: "https://claude.ai/cimd", Permissions: perms}
}

func names(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestListsOnlyWhatThePrincipalMayUse(t *testing.T) {
	r := tools(t)
	if got := names(t, connect(t, r, principal("p:read"))); !slices.Equal(got, []string{"echo", "fail"}) {
		t.Fatalf("p:read lists %v", got)
	}
	if got := names(t, connect(t, r, principal("p:read", "p:write"))); !slices.Equal(got, []string{"echo", "fail", "write_it"}) {
		t.Fatalf("p:read+p:write lists %v", got)
	}
	if got := names(t, connect(t, r, principal())); len(got) != 0 {
		t.Fatalf("no permissions lists %v", got)
	}
}

func TestInitializeCarriesTheInstructions(t *testing.T) {
	init := connect(t, tools(t), principal("p:read")).InitializeResult()
	if init.ServerInfo.Name != mcpx.ServerName || !strings.Contains(init.Instructions, "deploy_folder") {
		t.Fatalf("initialize: %+v", init)
	}
	// honest about what Odicea does not give apps yet, and points at the skill
	for _, want := range []string{"Todavía no", "bases de datos", "NO las crees en servicios externos", "skill", "Conecta tu asistente"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("instructions do not say %q", want)
		}
	}
	if strings.Contains(init.Instructions, "TI los configura aparte") {
		t.Error("instructions still promise that IT configures secrets separately")
	}
}

func call(t *testing.T, cs *mcp.ClientSession, name, text string) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"text": text}})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestCallRunsWithThePrincipalAndProvenance(t *testing.T) {
	res := call(t, connect(t, tools(t), principal("p:read")), "echo", "hola")
	if res.IsError {
		t.Fatalf("echo failed: %+v", res.Content)
	}
	var out echoOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != (echoOut{Text: "hola", Client: "https://claude.ai/cimd", Meta: "mcp:https://claude.ai/cimd"}) {
		t.Fatalf("echo: %+v", out)
	}
}

func toolError(t *testing.T, res *mcp.CallToolResult) map[string]string {
	t.Helper()
	if !res.IsError || len(res.Content) != 1 {
		t.Fatalf("want one isError content, got %+v", res)
	}
	var body struct{ Error map[string]string }
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error
}

func TestErrorsTellTheAgentWhatToDo(t *testing.T) {
	cs := connect(t, tools(t), principal("p:read"))
	got := toolError(t, call(t, cs, "fail", "conflict"))
	if got["code"] != "CONFLICT" || got["message"] != "slug taken" || got["field"] != "name" || got["next_step"] != "Usa get_app." || got["request_id"] != "req-1" {
		t.Fatalf("conflict: %v", got)
	}
	got = toolError(t, call(t, cs, "fail", "default"))
	if got["code"] != "NOT_FOUND" || !strings.Contains(got["next_step"], "list_apps") {
		t.Fatalf("not found: %v", got)
	}
	got = toolError(t, call(t, cs, "fail", "internal"))
	if got["code"] != "INTERNAL" || strings.Contains(got["message"], "password") || got["message"] != errx.GenericMessage {
		t.Fatalf("internal leaks: %v", got)
	}
}

// IT's refusals reach the agent with their Spanish message intact and a
// next_step that says "show it and stop", not the generic "ask for the
// permission" a 403 would get.
func TestPolicyRefusalsTellTheAgentToStop(t *testing.T) {
	cs := connect(t, tools(t), principal("p:read"))
	got := toolError(t, call(t, cs, "fail", "denied"))
	if got["code"] != gate.CodeDenied || got["message"] != "La regla de TI «Sandbox no puede ser pública» no permite esto." ||
		!strings.Contains(got["next_step"], "no reintentes") || strings.Contains(got["next_step"], "permiso") {
		t.Fatalf("denied: %v", got)
	}
	got = toolError(t, call(t, cs, "fail", "approval"))
	if got["code"] != gate.CodeApprovalRequired || !strings.Contains(got["message"], "necesita aprobación de TI") ||
		!strings.Contains(got["next_step"], "aprobación de TI") {
		t.Fatalf("approval: %v", got)
	}
}

func TestUnlistedToolsCannotBeCalled(t *testing.T) {
	res, err := connect(t, tools(t), principal("p:read")).CallTool(context.Background(), &mcp.CallToolParams{Name: "write_it", Arguments: map[string]any{"text": "x"}})
	if err == nil && !res.IsError {
		t.Fatal("write_it ran without p:write")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	h := mcpx.NewHandler(tools(t), mcpx.Options{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer opaque")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", w.Code)
	}
}

func TestRegistryRejectsBadTools(t *testing.T) {
	ok := func(t mcpx.Tool) mcpx.Tool {
		return mcpx.Typed(t, func(context.Context, mcpx.Principal, echoIn) (echoOut, error) { return echoOut{}, nil })
	}
	base := mcpx.Tool{Name: "a_tool", Description: "d", Permissions: []string{"p"}, Risk: mcpx.RiskRead}
	cases := map[string][]mcpx.Tool{
		"camelCase":   {ok(mcpx.Tool{Name: "aTool", Description: "d", Permissions: []string{"p"}, Risk: mcpx.RiskRead})},
		"duplicate":   {ok(base), ok(base)},
		"no risk":     {ok(mcpx.Tool{Name: "b", Description: "d", Permissions: []string{"p"}})},
		"no perms":    {ok(mcpx.Tool{Name: "b", Description: "d", Risk: mcpx.RiskRead})},
		"no handler":  {base},
		"no describe": {ok(mcpx.Tool{Name: "b", Permissions: []string{"p"}, Risk: mcpx.RiskRead})},
	}
	for name, ts := range cases {
		if _, err := mcpx.NewRegistry(ts); err == nil {
			t.Errorf("%s: registry accepted it", name)
		}
	}
}

func TestAnnotationsFollowTheRisk(t *testing.T) {
	res, err := connect(t, tools(t), principal("p:read")).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		switch tool.Name {
		case "echo":
			if !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint != nil {
				t.Errorf("echo: %+v", a)
			}
		case "fail":
			if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("fail: %+v", a)
			}
		}
	}
}
