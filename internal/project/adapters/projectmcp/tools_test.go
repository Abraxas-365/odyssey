package projectmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectmcp"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var user = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")

type fixture struct {
	svc *projectsvc.Service
	org identity.OrganizationID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return fixture{svc: projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{}), org: identity.NewOrganizationID()}
}

func (f fixture) create(t *testing.T, org identity.OrganizationID, name, slug string) project.ID {
	t.Helper()
	id, err := f.svc.Create(context.Background(), org, project.Create{Name: name, Slug: slug, CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) connect(t *testing.T, opts projectmcp.Options, perms ...string) *mcp.ClientSession {
	t.Helper()
	reg, err := mcpx.NewRegistry(projectmcp.Tools(f.svc, opts))
	if err != nil {
		t.Fatal(err)
	}
	h := mcpx.NewHandler(reg, mcpx.Options{})
	p := mcpx.Principal{Organization: f.org, User: user, Client: "https://claude.ai/cimd", Permissions: perms}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := event.WithMeta(mcpx.WithPrincipal(r.Context(), p), event.Meta{CorrelationID: "req-1", Client: "mcp:" + p.Client})
		h.ServeHTTP(w, r.WithContext(ctx))
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

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer opaque")
	return http.DefaultTransport.RoundTrip(r)
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

func TestToolsAreListedOnlyWithProjectsRead(t *testing.T) {
	f := newFixture(t)
	list := func(perms ...string) []string {
		res, err := f.connect(t, projectmcp.Options{}, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
			if tool.OutputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("%s: want an output schema and a read-only hint, got %+v", tool.Name, tool)
			}
		}
		slices.Sort(names)
		return names
	}
	if got := list(permission.ProjectsRead); !slices.Equal(got, []string{"list_apps", "whoami"}) {
		t.Fatalf("with projects:read: %v", got)
	}
	if got := list(permission.DeploymentsRead); len(got) != 0 {
		t.Fatalf("without projects:read: %v", got)
	}
}

func TestWhoami(t *testing.T) {
	f := newFixture(t)
	perms := []string{permission.ProjectsRead, permission.DeploymentsRead}
	var out projectmcp.WhoamiOut
	call(t, f.connect(t, projectmcp.Options{ConsoleURL: "https://odicea.test/", GitHubEnabled: func(context.Context) bool { return true }}, perms...), "whoami", nil, &out)
	if out.OrganizationID != f.org.String() || out.UserID != user.String() || out.Client != "https://claude.ai/cimd" ||
		!slices.Equal(out.Permissions, perms) || out.ConsoleURL != "https://odicea.test" || !out.GitHubEnabled {
		t.Fatalf("whoami: %+v", out)
	}
	call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsRead), "whoami", nil, &out)
	if out.GitHubEnabled {
		t.Fatal("GitHub must be off when the deployment does not say so")
	}
}

func TestListAppsFiltersPagesAndStaysInTheOrganization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, f.org, "Cotizador", "cotizador")
	visitas := f.create(t, f.org, "Visitas a clientes", "visitas")
	old := f.create(t, f.org, "Viejo", "viejo")
	f.create(t, identity.NewOrganizationID(), "Ajena", "ajena")
	if err := f.svc.Archive(ctx, f.org, old, project.Archive{ArchivedBy: user}); err != nil {
		t.Fatal(err)
	}
	cs := f.connect(t, projectmcp.Options{ConsoleURL: "https://odicea.test"}, permission.ProjectsRead)

	var out projectmcp.ListAppsOut
	call(t, cs, "list_apps", nil, &out)
	if out.Total != 2 || len(out.Items) != 2 || out.NextOffset != nil || out.Limit != 20 {
		t.Fatalf("default list: %+v", out)
	}
	first := out.Items[0]
	if first.Slug != "cotizador" || first.Status != "active" || first.CreatedAt.IsZero() || first.ConsoleURL != "https://odicea.test/apps/"+first.ID {
		t.Fatalf("first app: %+v", first)
	}
	if out.Items[1].ID != visitas.String() {
		t.Fatalf("order: %+v", out.Items)
	}

	call(t, cs, "list_apps", map[string]any{"status": "archived"}, &out)
	if out.Total != 1 || out.Items[0].Slug != "viejo" {
		t.Fatalf("archived: %+v", out)
	}
	call(t, cs, "list_apps", map[string]any{"search": "visit"}, &out)
	if out.Total != 1 || out.Items[0].Slug != "visitas" {
		t.Fatalf("search: %+v", out)
	}

	call(t, cs, "list_apps", map[string]any{"limit": 1}, &out)
	if len(out.Items) != 1 || out.NextOffset == nil || *out.NextOffset != 1 || out.Total != 2 {
		t.Fatalf("page 1: %+v", out)
	}
	var last projectmcp.ListAppsOut
	call(t, cs, "list_apps", map[string]any{"limit": 1, "offset": 1}, &last)
	if len(last.Items) != 1 || last.Items[0].Slug != "visitas" || last.NextOffset != nil {
		t.Fatalf("page 2: %+v", last)
	}
}

func TestListAppsRejectsBadInput(t *testing.T) {
	cs := newFixture(t).connect(t, projectmcp.Options{}, permission.ProjectsRead)
	res := call(t, cs, "list_apps", map[string]any{"status": "deleted"}, nil)
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "VALIDATION") {
		t.Fatalf("bad status: %+v", res)
	}
	if res := call(t, cs, "list_apps", map[string]any{"offset": -1}, nil); !res.IsError {
		t.Fatal("negative offset accepted")
	}
}
