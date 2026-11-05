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
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var user = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")

type fixture struct {
	svc    *projectsvc.Service
	events *eventtest.Recorder
	org    identity.OrganizationID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	rec := &eventtest.Recorder{}
	return fixture{svc: projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, rec), events: rec, org: identity.NewOrganizationID()}
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
	reg, err := mcpx.NewRegistry(projectmcp.Tools(f.svc, f.svc, opts))
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

// needs_attention comes from one batch call for the page; without the batch
// (or when it fails) the field is absent, never a wrong false.
func TestListAppsFlagsNeedsAttentionInOneBatch(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, f.org, "Cotizador", "cotizador")
	b := f.create(t, f.org, "Visitas", "visitas")
	calls := 0
	var failing bool
	opts := projectmcp.Options{NeedingAttention: func(_ context.Context, org identity.OrganizationID, apps []project.ID) (map[project.ID]bool, error) {
		calls++
		if failing {
			return nil, context.DeadlineExceeded
		}
		if org != f.org || len(apps) != 2 {
			t.Errorf("asked %v for %v", org, apps)
		}
		return map[project.ID]bool{a: true, b: false}, nil
	}}
	cs := f.connect(t, opts, permission.ProjectsRead)

	var out projectmcp.ListAppsOut
	call(t, cs, "list_apps", nil, &out)
	if calls != 1 || out.Items[0].NeedsAttention == nil || !*out.Items[0].NeedsAttention || out.Items[1].NeedsAttention == nil || *out.Items[1].NeedsAttention {
		t.Fatalf("calls %d: %+v", calls, out.Items)
	}
	failing = true
	var failed projectmcp.ListAppsOut
	call(t, cs, "list_apps", nil, &failed)
	if len(failed.Items) != 2 || failed.Items[0].NeedsAttention != nil {
		t.Fatalf("a failed judgement must leave the flag out: %+v", failed.Items)
	}
	var bare projectmcp.ListAppsOut
	call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsRead), "list_apps", nil, &bare)
	if bare.Items[0].NeedsAttention != nil {
		t.Fatalf("no judge wired: %+v", bare.Items[0])
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

func TestCreateAppIsListedOnlyWithProjectsWrite(t *testing.T) {
	f := newFixture(t)
	list := func(perms ...string) []string {
		res, err := f.connect(t, projectmcp.Options{}, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		return names
	}
	if got := list(permission.ProjectsRead, permission.DeploymentsRead); slices.Contains(got, "create_app") {
		t.Fatalf("a reader sees create_app: %v", got)
	}
	if got := list(permission.ProjectsRead, permission.ProjectsWrite); !slices.Equal(got, []string{"create_app", "list_apps", "whoami"}) {
		t.Fatalf("a writer lists %v", got)
	}
	// Calling it anyway is refused and creates nothing.
	cs := f.connect(t, projectmcp.Options{}, permission.ProjectsRead)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_app", Arguments: map[string]any{"name": "Visitas"}})
	if err == nil && !res.IsError {
		t.Fatalf("create_app ran without projects:write: %+v", res)
	}
	if page, _ := f.svc.List(context.Background(), f.org, project.Filter{}, query.Pagination{}.Normalize()); page.Page.Total != 0 {
		t.Fatalf("an app was created: %+v", page)
	}
}

func TestCreateApp(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, projectmcp.Options{ConsoleURL: "https://odicea.test/"}, permission.ProjectsWrite)
	var out projectmcp.CreateAppOut
	res := call(t, cs, "create_app", map[string]any{"name": "Visitas a clientes", "description": "Registro de visitas"}, &out)
	if res.IsError {
		t.Fatalf("create: %+v", res.Content)
	}
	if out.Slug != "visitas-a-clientes" || out.Name != "Visitas a clientes" || out.ID == "" || out.ConsoleURL != "https://odicea.test/apps/"+out.ID ||
		out.NextStep != "Publica con deploy_folder (app=visitas-a-clientes)." {
		t.Fatalf("create: %+v", out)
	}
	id, err := project.ParseID(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.Find(context.Background(), f.org, id)
	if err != nil || p.Description != "Registro de visitas" || p.CreatedBy != user {
		t.Fatalf("stored: %+v %v", p, err)
	}

	// An explicit slug.
	call(t, cs, "create_app", map[string]any{"name": "Otra", "slug": "mi-app"}, &out)
	if out.Slug != "mi-app" || out.NextStep != "Publica con deploy_folder (app=mi-app)." {
		t.Fatalf("explicit slug: %+v", out)
	}
}

func TestCreateAppIsAttributedToTheMCPClient(t *testing.T) {
	f := newFixture(t)
	call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsWrite), "create_app", map[string]any{"name": "Visitas"}, nil)
	events := f.events.Events()
	if len(events) == 0 {
		t.Fatal("no events published")
	}
	for _, e := range events {
		if e.Client != "mcp:https://claude.ai/cimd" {
			t.Fatalf("%s carries client %q", e.Type, e.Client)
		}
	}
}

func TestCreateAppSlugConflictSuggestsGetApp(t *testing.T) {
	f := newFixture(t)
	f.create(t, f.org, "Visitas", "visitas")
	cs := f.connect(t, projectmcp.Options{}, permission.ProjectsWrite)
	res := call(t, cs, "create_app", map[string]any{"name": "Otra cosa", "slug": "visitas"}, nil)
	if !res.IsError {
		t.Fatal("duplicate slug accepted")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "CONFLICT") || !strings.Contains(text, "get_app") || !strings.Contains(text, "next_step") {
		t.Fatalf("conflict: %s", text)
	}
	// The same slug in another organization is fine.
	g := newFixture(t)
	g.create(t, g.org, "Visitas", "visitas")
	if res := call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsWrite), "create_app", map[string]any{"name": "Visitas 2", "slug": "visitas-2"}, nil); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
}

func TestCreateAppRejectsBadInput(t *testing.T) {
	cs := newFixture(t).connect(t, projectmcp.Options{}, permission.ProjectsWrite)
	for _, args := range []map[string]any{{"name": " "}, {"name": "Ok", "slug": "Mal Slug!"}} {
		res := call(t, cs, "create_app", args, nil)
		if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "VALIDATION") {
			t.Fatalf("%v: %+v", args, res)
		}
	}
}
