package aimcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aimcp"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var user = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")

// commands records Enable; the other Commands are never called.
type commands struct {
	ai.Commands
	enabled []ai.Enable
	err     error
}

func (c *commands) Enable(_ context.Context, _ identity.OrganizationID, _ project.ID, _ project.EnvironmentID, in ai.Enable) (ai.View, error) {
	c.enabled = append(c.enabled, in)
	if c.err != nil {
		return ai.View{}, c.err
	}
	v := ai.View{Status: ai.StateEnabling, BaseURL: "https://ai.test", Variables: ai.Variables}
	if in.Services != nil {
		v.Services = *in.Services
	}
	return v, nil
}

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer opaque")
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, cmds ai.Commands) *mcp.ClientSession {
	t.Helper()
	org := identity.NewOrganizationID()
	projects := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	if _, err := projects.Create(context.Background(), org, project.Create{Name: "Cotizador", Slug: "cotizador", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	reg, err := mcpx.NewRegistry(aimcp.Tools(projects, cmds, aimcp.Options{ConsoleURL: "https://odicea.test"}))
	if err != nil {
		t.Fatal(err)
	}
	h := mcpx.NewHandler(reg, mcpx.Options{})
	p := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/cimd", Permissions: permission.All}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func enable(t *testing.T, cs *mcp.ClientSession, args map[string]any) (aimcp.EnableAIOut, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "enable_ai", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var out aimcp.EnableAIOut
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, res
}

func TestEnableAIServices(t *testing.T) {
	cmds := &commands{}
	cs := connect(t, cmds)
	// omitted: nil, the AI keeps the parts it has
	if _, res := enable(t, cs, map[string]any{"app": "cotizador"}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.enabled; len(got) != 1 || got[0].Services != nil || got[0].RequestedBy != user {
		t.Fatalf("omitted services must be nil: %+v", got)
	}
	// given: passed through
	if _, res := enable(t, cs, map[string]any{"app": "cotizador", "services": []string{"api"}}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.enabled; len(got) != 2 || got[1].Services == nil || !slices.Equal(*got[1].Services, []string{"api"}) {
		t.Fatalf("services not passed: %+v", got)
	}
	// an empty list is the whole app, not "keep"
	if _, res := enable(t, cs, map[string]any{"app": "cotizador", "services": []string{}}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.enabled; len(got) != 3 || got[2].Services == nil || len(*got[2].Services) != 0 {
		t.Fatalf("empty services must be &[]: %+v", got[2])
	}
}

func TestEnableAIScopeUnavailableGivesNextStep(t *testing.T) {
	cmds := &commands{err: errx.Conflict("El clúster de esta app todavía no puede.").WithCode("SERVICE_SETTINGS_UNAVAILABLE")}
	cs := connect(t, cmds)
	_, res := enable(t, cs, map[string]any{"app": "cotizador", "services": []string{"api"}})
	if !res.IsError {
		t.Fatal("want an error")
	}
	body := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(body, "SERVICE_SETTINGS_UNAVAILABLE") || !strings.Contains(body, "hazlo para toda la app (sin services)") || strings.Contains(body, "set_env (unset)") {
		t.Fatalf("body %s", body)
	}
}
