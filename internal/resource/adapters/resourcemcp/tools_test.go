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

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
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
	var conn *resource.Connection
	if in.Connect != nil {
		conn = &resource.Connection{Name: in.Name, Prefix: in.Connect.Prefix}
	}
	v := resource.NewView(resource.Resource{Name: in.Name, Kind: in.Kind, StorageGi: 1}, resource.Observed{}, conn)
	return resource.Result{View: v}, nil
}

type queries struct {
	resource.Queries
	list    resource.Listing
	offered resource.Offered
}

func (q queries) List(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (resource.Listing, error) {
	return q.list, nil
}

func (q queries) Offered(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, identity.UserID) (resource.Offered, error) {
	return q.offered, nil
}

func connect(t *testing.T, cmds resource.Commands, qs resource.Queries) (*mcp.ClientSession, identity.OrganizationID) {
	t.Helper()
	return connectAs(t, cmds, qs, permission.All)
}

func connectAs(t *testing.T, cmds resource.Commands, qs resource.Queries, perms []string) (*mcp.ClientSession, identity.OrganizationID) {
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
	p := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/cimd", Permissions: perms}
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

// create_resource passes recipe and params through, connects by default and
// not with connect=false (where prefix/services make no sense); a held
// creation answers pending_approval with nothing created.
func TestCreateResource(t *testing.T) {
	cmds := &commands{}
	cs, _ := connect(t, cmds, queries{})
	out, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_resource", map[string]any{"app": "cotizador", "kind": "postgres",
		"recipe": " Postgres-Produccion ", "params": map[string]int{"storage_gi": 20}, "prefix": "ANALYTICS"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	got := cmds.created[0]
	if got.Recipe != catalog.PostgresProduction || got.Params[catalog.ParamStorage] != 20 || got.Connect == nil || got.Connect.Prefix != "ANALYTICS" ||
		got.Name != "db" || got.RequestedBy != user {
		t.Fatalf("create %+v", got)
	}
	if out.Resource.Name != "db" || out.Status != "" {
		t.Fatalf("out %+v", out)
	}

	out, res = call[resourcemcp.CreateDatabaseOut](t, cs, "create_resource", map[string]any{"app": "cotizador", "kind": "files", "name": "uploads", "connect": false})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.created[1]; got.Connect != nil || got.Kind != resourcekind.Files {
		t.Fatalf("connect=false must not connect: %+v", got)
	}
	if !strings.Contains(out.NextStep, "no está conectado") {
		t.Fatalf("next step %q", out.NextStep)
	}

	_, res = call[resourcemcp.CreateDatabaseOut](t, cs, "create_resource", map[string]any{"app": "cotizador", "connect": false, "prefix": "X"})
	if !res.IsError || len(cmds.created) != 2 {
		t.Fatalf("prefix without connecting must be refused before creating: %+v", res)
	}

	cmds.fn = func(resource.Create) (resource.Result, error) {
		return resource.Result{View: resource.View{Outcome: gate.Outcome{Pending: &gate.Pending{Status: gate.StatusPendingApproval, RequestID: "req-1"}}}}, nil
	}
	out, res = call[resourcemcp.CreateDatabaseOut](t, cs, "create_resource", map[string]any{"app": "cotizador", "params": map[string]int{"storage_gi": 80}})
	if res.IsError || out.Status != gate.StatusPendingApproval || out.RequestID != "req-1" || out.Resource.Name != "" {
		t.Fatalf("held %+v %+v", out, res)
	}
}

// create_database is create_resource with the default recipe: storage_gi
// becomes params.storage_gi and it always connects.
func TestCreateDatabaseStorage(t *testing.T) {
	cmds := &commands{}
	cs, _ := connect(t, cmds, queries{})
	if _, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador", "storage_gi": 7}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if _, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_database", map[string]any{"app": "cotizador", "name": "two"}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := cmds.created; got[0].Params[catalog.ParamStorage] != 7 || got[0].Recipe != "" || got[0].Connect == nil ||
		got[1].Params != nil || got[1].Kind != resourcekind.Postgres {
		t.Fatalf("create %+v", got)
	}
}

// A catalog refusal tells the assistant where to look (list_catalog) and not
// to retry on its own.
func TestCreateResourceCatalogRefusalGivesNextStep(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		step string
	}{
		"out of limits": {errx.Validation("El tamaño debe estar entre 5 y 100 GiB.").WithCode(catalog.CodeOutOfLimits), "No reintentes"},
		"not offered":   {catalog.Unavailable(resourcekind.Redis, project.TierSandbox), "TI no ofrece"},
		"old agent": {catalog.ClusterCannot(catalog.RecipeVersion{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
			Version: 1, Title: "PostgreSQL de producción"}, []string{"spec/v1"}), "agente es anterior"},
		"recipe changed": {errx.Business("cambió").WithCode(catalog.CodeRecipeChanged), "TI cambió esa receta"},
		"unknown recipe": {errx.InvalidField("recipe", "no recipe postgres-gigante"), "no existe"},
		"unknown param":  {errx.InvalidField("params", "postgres-basico@1 has no parameter cpu"), "no existe"},
	} {
		t.Run(name, func(t *testing.T) {
			cmds := &commands{fn: func(resource.Create) (resource.Result, error) { return resource.Result{}, tc.err }}
			cs, _ := connect(t, cmds, queries{})
			_, res := call[resourcemcp.CreateDatabaseOut](t, cs, "create_resource", map[string]any{"app": "cotizador", "params": map[string]int{"storage_gi": 900}})
			if !res.IsError {
				t.Fatal("want an error")
			}
			var body struct{ Error map[string]string }
			if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
				t.Fatal(err)
			}
			if step := body.Error["next_step"]; !strings.Contains(step, tc.step) || !strings.Contains(step, "list_catalog") {
				t.Fatalf("body %v", body.Error)
			}
		})
	}
}

// list_catalog words the environment's offer: params with limits, the
// variables, instant / needs_approval / denied, why the rest is unavailable
// and whether the caller may create at all.
func TestListCatalog(t *testing.T) {
	offered := resource.Offered{Tier: project.TierProduction,
		Recipes: []resource.Offer{
			{Kind: resourcekind.Postgres, Recipe: catalog.PostgresBasic, Version: 1, Title: "PostgreSQL básico", Default: true, Verdict: gate.Allow,
				Params: []resource.OfferParam{{Name: catalog.ParamStorage, Label: "Tamaño", Unit: "GiB", Min: 1, Max: 100, Default: 1,
					PreapprovedMin: 1, PreapprovedMax: 100, Mutability: catalog.Grows}},
				Variables: []resource.OfferVariable{{Name: "DATABASE_URL", Secret: true, Meaning: "la conexión"}}},
			{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction, Version: 1, Verdict: gate.Approval, Rules: []string{"prod-data"}},
			{Kind: resourcekind.Redis, Recipe: catalog.RedisBasic, Version: 1, Verdict: gate.Deny, Rules: []string{"no-redis"}},
		},
		Unavailable: []resource.UnavailableKind{{Kind: resourcekind.Files, Reason: "El clúster no puede."}},
	}
	cs, _ := connect(t, &commands{}, queries{offered: offered})
	out, res := call[resourcemcp.ListCatalogOut](t, cs, "list_catalog", map[string]any{"app": "cotizador"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.Tier != "production" || len(out.Recipes) != 3 || !out.CanCreate || !strings.Contains(out.NextStep, "create_resource") {
		t.Fatalf("out %+v", out)
	}
	first := out.Recipes[0]
	if !first.Default || first.Approval != "instant" || first.Params[0].Max != 100 || !first.Params[0].Grows || first.Params[0].Fixed ||
		first.Variables[0].Name != "DATABASE_URL" || !first.Variables[0].Secret || first.Rules != nil {
		t.Fatalf("first %+v", first)
	}
	if out.Recipes[1].Approval != "needs_approval" || !slices.Equal(out.Recipes[1].Rules, []string{"prod-data"}) ||
		out.Recipes[2].Approval != "denied" {
		t.Fatalf("verdicts %+v", out.Recipes)
	}
	if len(out.Unavailable) != 1 || out.Unavailable[0].Kind != "files" || out.Unavailable[0].Reason != "El clúster no puede." {
		t.Fatalf("unavailable %+v", out.Unavailable)
	}

	// read-only: sees the catalog, is told it cannot create
	cs, _ = connectAs(t, &commands{}, queries{offered: offered}, []string{permission.ProjectsRead})
	out, res = call[resourcemcp.ListCatalogOut](t, cs, "list_catalog", map[string]any{"app": "cotizador"})
	if res.IsError || out.CanCreate || !strings.Contains(out.NextStep, "no crear") {
		t.Fatalf("read-only %+v %+v", out, res)
	}

	// nothing offered: say why, not how to create
	cs, _ = connect(t, &commands{}, queries{offered: resource.Offered{Tier: project.TierSandbox, Recipes: []resource.Offer{},
		Unavailable: []resource.UnavailableKind{{Kind: resourcekind.Postgres, Reason: "sin clúster"}}}})
	out, _ = call[resourcemcp.ListCatalogOut](t, cs, "list_catalog", map[string]any{"app": "cotizador"})
	if len(out.Recipes) != 0 || !strings.Contains(out.NextStep, "unavailable") {
		t.Fatalf("empty %+v", out)
	}
}
