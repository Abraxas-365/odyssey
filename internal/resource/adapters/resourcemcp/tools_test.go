package resourcemcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcemcp"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

var user = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")

// commands records Create; the other Commands are never called.
type commands struct {
	resource.Commands
	created []resource.Create
	fn      func(resource.Create) (resource.Result, error)
}

func (c *commands) Create(_ context.Context, _ identity.OrganizationID, _ project.ID, _ project.EnvironmentID, in resource.Create) (resource.Result, error) {
	c.created = append(c.created, in)
	if c.fn != nil {
		return c.fn(in)
	}
	v := resource.NewView(resource.Resource{Name: in.Name, Kind: in.Kind, StorageGi: 1}, resource.Observed{}, &resource.Connection{Name: in.Name, Prefix: in.Connect.Prefix})
	return resource.Result{View: v}, nil
}

type queries struct {
	resource.Queries
	list resource.Listing
}

func (q queries) List(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (resource.Listing, error) {
	return q.list, nil
}

func connect(t *testing.T, cmds resource.Commands, qs resource.Queries) (*mcp.ClientSession, identity.OrganizationID) {
	t.Helper()
	org := identity.NewOrganizationID()
	projects := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	if _, err := projects.Create(context.Background(), org, project.Create{Name: "Cotizador", Slug: "cotizador", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	reg, err := mcpx.NewRegistry(resourcemcp.Tools(projects, cmds, qs, resourcemcp.Options{ConsoleURL: "https://odicea.test"}))
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
	return cs, org
}

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer opaque")
	return http.DefaultTransport.RoundTrip(r)
}

func call[Out any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (out Out, res *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, res
}

func TestCreateDatabaseServices(t *testing.T) {
	cmds := &commands{}
	cs, _ := connect(t, cmds, queries{})
	// omitted: the whole app (no scope at all)
	if _, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador"}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.created; len(got) != 1 || got[0].Connect == nil || got[0].Connect.Services != nil {
		t.Fatalf("omitted services must stay nil: %+v", got)
	}
	// named services are passed, and the resource says which parts get it
	cmds.fn = func(in resource.Create) (resource.Result, error) {
		var services []string
		if in.Connect.Services != nil {
			services = *in.Connect.Services
		}
		v := resource.NewView(resource.Resource{Name: in.Name, Kind: in.Kind, StorageGi: 1}, resource.Observed{},
			&resource.Connection{Name: in.Name, Services: services})
		return resource.Result{View: v}, nil
	}
	out, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador", "services": []string{"api", "worker"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.created; len(got) != 2 || got[1].Connect.Services == nil || !slices.Equal(*got[1].Connect.Services, []string{"api", "worker"}) {
		t.Fatalf("services not passed: %+v", got)
	}
	if !slices.Equal(out.Resource.Services, []string{"api", "worker"}) {
		t.Fatalf("out %+v", out.Resource)
	}
	// an empty list is the whole app too
	if _, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador", "services": []string{}}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.created; got[2].Connect.Services != nil {
		t.Fatalf("empty services must stay nil: %+v", got[2])
	}
}

func TestCreateDatabaseScopeUnavailableGivesNextStep(t *testing.T) {
	cmds := &commands{fn: func(resource.Create) (resource.Result, error) {
		return resource.Result{}, errx.Conflict("El clúster de esta app todavía no puede.").WithCode("SERVICE_SETTINGS_UNAVAILABLE")
	}}
	cs, _ := connect(t, cmds, queries{})
	_, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador", "services": []string{"api"}})
	if !res.IsError {
		t.Fatal("want an error")
	}
	body := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(body, "SERVICE_SETTINGS_UNAVAILABLE") || !strings.Contains(body, "hazlo para toda la app (sin services)") {
		t.Fatalf("body %s", body)
	}
}

func TestListResourcesShowsServices(t *testing.T) {
	scoped := resource.NewView(resource.Resource{Name: "db", Kind: resourcekind.Postgres, StorageGi: 1}, resource.Observed{Phase: resource.PhaseReady},
		&resource.Connection{Name: "db", Services: []string{"api"}})
	whole := resource.NewView(resource.Resource{Name: "cache", Kind: resourcekind.Redis, StorageGi: 1}, resource.Observed{Phase: resource.PhaseReady},
		&resource.Connection{Name: "cache"})
	cs, _ := connect(t, &commands{}, queries{list: resource.Listing{Items: []resource.View{scoped, whole}}})
	out, res := call[resourcemcp.ListResourcesOut](t, cs, "list_resources", map[string]any{"app": "cotizador"})
	if res.IsError || len(out.Resources) != 2 {
		t.Fatalf("%+v %+v", out, res)
	}
	if !slices.Equal(out.Resources[0].Services, []string{"api"}) || out.Resources[1].Services != nil {
		t.Fatalf("services %+v", out.Resources)
	}
}
