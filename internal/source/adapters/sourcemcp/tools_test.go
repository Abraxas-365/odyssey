package sourcemcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcemcp"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var user = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")

// github fakes source.Commands and source.Queries (the other methods are
// never called: they panic on the nil embedded interface).
type github struct {
	source.Commands
	source.Queries

	mu            sync.Mutex
	enabled       bool
	installations []source.Installation
	repos         []source.Repo
	pushes        []source.Push // newest first
	connected     []source.Connect
	connectedBy   []string
	published     int
	publishErr    error
	repoQueries   []string
	// onList runs at the nth ListPushes call (to make a deployment appear).
	onList func(n int)
	lists  int
}

func (g *github) Info(context.Context) source.Info { return source.Info{Enabled: g.enabled} }

func (g *github) ListInstallations(context.Context, identity.OrganizationID) ([]source.Installation, error) {
	return g.installations, nil
}

func (g *github) ListRepositories(_ context.Context, _ identity.OrganizationID, page query.Pagination) (query.Paginated[source.Repo], error) {
	g.repoQueries = append(g.repoQueries, page.Search)
	var all []source.Repo
	for _, r := range g.repos {
		if page.Search == "" || strings.Contains(strings.ToLower(r.FullName), strings.ToLower(page.Search)) {
			all = append(all, r)
		}
	}
	from := min(page.Offset, len(all))
	to := min(from+page.Limit, len(all))
	return query.NewPaginated(all[from:to], len(all), page), nil
}

func (g *github) ListPushes(_ context.Context, _ identity.OrganizationID, _ project.ID, _ source.PushFilter, page query.Pagination) (query.Paginated[source.Push], error) {
	g.mu.Lock()
	g.lists++
	n := g.lists
	g.mu.Unlock()
	if g.onList != nil {
		g.onList(n)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	to := min(page.Limit, len(g.pushes))
	return query.NewPaginated(append([]source.Push(nil), g.pushes[:to]...), len(g.pushes), page), nil
}

func (g *github) Connect(ctx context.Context, _ identity.OrganizationID, app project.ID, in source.Connect) (source.Connection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.connected = append(g.connected, in)
	g.connectedBy = append(g.connectedBy, event.MetaFrom(ctx).Client)
	var name string
	for _, r := range g.repos {
		if r.ID == in.RepositoryID {
			name = r.FullName
		}
	}
	branch := in.Branch
	if branch == "" {
		branch = "main"
	}
	g.pushes = append([]source.Push{{ID: source.NewPushID(), Project: app, Status: source.StatusQueued, Trigger: source.TriggerManual}}, g.pushes...)
	return source.Connection{Project: app, Installation: in.Installation, RepositoryID: in.RepositoryID, Repository: name, Branch: branch, Root: in.Root, Config: in.Config}, nil
}

func (g *github) Publish(_ context.Context, _ identity.OrganizationID, app project.ID, in source.Publish) (source.Push, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.publishErr != nil {
		return source.Push{}, g.publishErr
	}
	g.published++
	push := source.Push{ID: source.NewPushID(), Project: app, Status: source.StatusQueued, Trigger: source.TriggerManual, RequestedBy: in.RequestedBy,
		Git: build.GitRef{Repository: "acme/cotizador", Branch: "main", Commit: strings.Repeat("cd", 20)}}
	g.pushes = append([]source.Push{push}, g.pushes...)
	return push, nil
}

// deploy marks the newest push as deployed.
func (g *github) deploy(id release.DeploymentID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pushes[0].Status, g.pushes[0].Deployment = source.StatusDeployed, &id
}

type fixture struct {
	projects *projectsvc.Service
	gh       *github
	org      identity.OrganizationID
	app      project.ID
	opts     sourcemcp.Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{projects: projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{}), org: identity.NewOrganizationID(),
		opts: sourcemcp.Options{ConsoleURL: "https://odicea.test/", PollInterval: 5 * time.Millisecond, WaitTimeout: 300 * time.Millisecond}}
	var err error
	if f.app, err = f.projects.Create(context.Background(), f.org, project.Create{Name: "Cotizador", Slug: "cotizador", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	inst := source.NewInstallationID()
	f.gh = &github{enabled: true, installations: []source.Installation{{ID: inst, Organization: f.org, Account: "acme"}}}
	for i, name := range []string{"acme/cotizador", "acme/visitas", "acme/Visitas-web"} {
		f.gh.repos = append(f.gh.repos, source.Repo{ID: int64(100 + i), FullName: name, DefaultBranch: "main", Private: i == 0, Installation: inst})
	}
	return f
}

func (f *fixture) connect(t *testing.T, org identity.OrganizationID, perms ...string) *mcp.ClientSession {
	t.Helper()
	reg, err := mcpx.NewRegistry(sourcemcp.Tools(f.projects, f.gh, f.gh, f.opts))
	if err != nil {
		t.Fatal(err)
	}
	h := mcpx.NewHandler(reg, mcpx.Options{})
	p := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/cimd", Permissions: perms}
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

func errorBody(t *testing.T, res *mcp.CallToolResult) map[string]string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("want an error result, got %+v", res.StructuredContent)
	}
	var body struct{ Error map[string]string }
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error
}

var (
	reader = []string{permission.ProjectsRead, permission.DeploymentsRead}
	writer = []string{permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate}
)

func TestToolsAreListedByPermission(t *testing.T) {
	f := newFixture(t)
	list := func(perms ...string) []string {
		res, err := f.connect(t, f.org, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
			if tool.OutputSchema == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (tool.Name == "list_repositories") {
				t.Fatalf("%s: schema/annotations %+v", tool.Name, tool.Annotations)
			}
		}
		slices.Sort(names)
		return names
	}
	if got := list(reader...); len(got) != 0 {
		t.Fatalf("a reader lists %v", got)
	}
	if got := list(writer...); !slices.Equal(got, []string{"connect_repository", "list_repositories", "publish_now"}) {
		t.Fatalf("writer lists %v", got)
	}
	// publish_now needs deployments:create + projects:read; list_repositories projects:write.
	if got := list(permission.ProjectsRead, permission.DeploymentsCreate); !slices.Equal(got, []string{"publish_now"}) {
		t.Fatalf("deployer lists %v", got)
	}
	if got := list(permission.ProjectsWrite); !slices.Equal(got, []string{"list_repositories"}) {
		t.Fatalf("projects:write lists %v", got)
	}

	// A reader calling them anyway is refused and nothing runs.
	cs := f.connect(t, f.org, reader...)
	for tool, args := range map[string]map[string]any{
		"list_repositories":  nil,
		"connect_repository": {"app": "cotizador", "repository": "acme/cotizador"},
		"publish_now":        {"app": "cotizador"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err == nil && !res.IsError {
			t.Fatalf("%s ran for a reader", tool)
		}
	}
	if len(f.gh.connected) != 0 || f.gh.published != 0 || len(f.gh.repoQueries) != 0 {
		t.Fatalf("something ran: %+v", f.gh)
	}
}

func TestListRepositories(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	out, res := call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", nil)
	if res.IsError || out.Total != 3 || len(out.Items) != 3 || out.NextOffset != nil || out.Limit != 20 {
		t.Fatalf("list: %+v (%v)", out, res.Content)
	}
	if r := out.Items[0]; r.ID != 100 || r.FullName != "acme/cotizador" || r.DefaultBranch != "main" || !r.Private {
		t.Fatalf("first: %+v", r)
	}
	if !strings.Contains(out.NextStep, "connect_repository") {
		t.Fatalf("next_step: %q", out.NextStep)
	}

	out, _ = call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", map[string]any{"search": "VISITAS"})
	if out.Total != 2 {
		t.Fatalf("search: %+v", out)
	}
	out, _ = call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", map[string]any{"limit": 2})
	if len(out.Items) != 2 || out.NextOffset == nil || *out.NextOffset != 2 {
		t.Fatalf("page 1: %+v", out)
	}
	out, _ = call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", map[string]any{"limit": 2, "offset": 2})
	if len(out.Items) != 1 || out.NextOffset != nil {
		t.Fatalf("page 2: %+v", out)
	}
	out, _ = call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", map[string]any{"search": "zzz"})
	if out.Total != 0 || !strings.Contains(out.NextStep, "search") {
		t.Fatalf("no match: %+v", out)
	}
	if _, res := call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", map[string]any{"offset": -1}); !res.IsError {
		t.Fatal("negative offset accepted")
	}
}

func TestListRepositoriesWhenGitHubIsOffOrNotInstalled(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)

	f.gh.enabled = false
	for tool, args := range map[string]map[string]any{
		"list_repositories":  nil,
		"connect_repository": {"app": "cotizador", "repository": "acme/cotizador"},
		"publish_now":        {"app": "cotizador"},
	} {
		_, res := call[sourcemcp.ListRepositoriesOut](t, cs, tool, args)
		if body := errorBody(t, res); body["code"] != source.CodeNotConfigured || !strings.Contains(body["next_step"], "GitHub no está habilitado") {
			t.Fatalf("%s disabled: %v", tool, body)
		}
	}

	f.gh.enabled, f.gh.installations, f.gh.repos = true, nil, nil
	_, res := call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", nil)
	if body := errorBody(t, res); !strings.Contains(body["next_step"], "https://odicea.test/configuracion/github") {
		t.Fatalf("no installation: %v", body)
	}
	// Connecting with none installed sends there too.
	_, res = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": "acme/x"})
	if body := errorBody(t, res); !strings.Contains(body["next_step"], "/configuracion/github") {
		t.Fatalf("connect without installation: %v", body)
	}

	// Installed but reaching nothing is not an error.
	f.gh.installations = []source.Installation{{ID: source.NewInstallationID()}}
	out, res := call[sourcemcp.ListRepositoriesOut](t, cs, "list_repositories", nil)
	if res.IsError || out.Total != 0 || out.NextStep == "" {
		t.Fatalf("installed, no repos: %+v %v", out, res.Content)
	}
}

func TestConnectRepositoryByNameAndByID(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	deployment := release.NewDeploymentID()
	// The push of the connection becomes a deployment on the third look.
	f.gh.onList = func(n int) {
		if n == 4 { // 1: the newest before connecting; 2..: waiting
			f.gh.deploy(deployment)
		}
	}

	out, res := call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{
		"app": "cotizador", "repository": "ACME/Visitas", "branch": "produccion", "root": "/web/", "port": 3000, "health_path": "/up", "replicas": 2,
		"env": map[string]any{"MODE": "prod"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.Repository != "acme/visitas" || out.Branch != "produccion" || out.Root != "web" && out.Root != "/web/" || out.DeploymentID != deployment.String() ||
		!strings.Contains(out.NextStep, "get_deployment") || !strings.Contains(out.NextStep, deployment.String()) || out.App != "cotizador" {
		t.Fatalf("out: %+v", out)
	}
	c := f.gh.connected[0]
	if c.RepositoryID != 101 || c.Installation != f.gh.repos[1].Installation || c.Branch != "produccion" || c.ConnectedBy != user ||
		c.Port != 3000 || c.HealthPath != "/up" || c.Replicas != 2 || c.Env["MODE"] != "prod" {
		t.Fatalf("connect input: %+v", c)
	}
	if f.gh.connectedBy[0] != "mcp:https://claude.ai/cimd" {
		t.Fatalf("client: %q", f.gh.connectedBy[0])
	}

	// By GitHub's numeric id (a string or a number), the default branch.
	f.gh.pushes, f.gh.lists, f.gh.onList = nil, 0, nil
	out, _ = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": f.app.String(), "repository": "102"})
	if got := f.gh.connected[1]; got.RepositoryID != 102 || out.Repository != "acme/Visitas-web" {
		t.Fatalf("by id: %+v %+v", got, out)
	}
}

func TestConnectRepositoryWithoutADeploymentYetOrFailed(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)

	// The push stays queued past the wait: the agent is told to look later.
	start := time.Now()
	out, res := call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": "acme/cotizador"})
	if res.IsError || out.DeploymentID != "" || !strings.Contains(out.NextStep, "unos segundos") || !strings.Contains(out.NextStep, "get_deployment") {
		t.Fatalf("queued: %+v %v", out, res.Content)
	}
	if el := time.Since(start); el < 250*time.Millisecond || el > 3*time.Second {
		t.Fatalf("waited %v", el)
	}

	// The push fails: the answer says why and does not wait the whole time.
	f.gh.onList = func(n int) {
		if n == 3 {
			f.gh.mu.Lock()
			f.gh.pushes[0].Status = source.StatusFailed
			f.gh.pushes[0].Error = &source.Failure{Code: source.CodeRootMissing, Message: "La carpeta elegida no existe en ese commit.", Detail: "web/"}
			f.gh.mu.Unlock()
		}
	}
	f.gh.lists = 0
	out, _ = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": "acme/cotizador", "root": "web"})
	if out.DeploymentID != "" || !strings.Contains(out.NextStep, "La carpeta elegida no existe") || !strings.Contains(out.NextStep, "publish_now") {
		t.Fatalf("failed: %+v", out)
	}
}

func TestConnectRepositoryIgnoresOlderPushes(t *testing.T) {
	f := newFixture(t)
	old := release.NewDeploymentID()
	f.gh.pushes = []source.Push{{ID: source.NewPushID(), Status: source.StatusDeployed, Deployment: &old}}
	out, _ := call[sourcemcp.ConnectRepositoryOut](t, f.connect(t, f.org, writer...), "connect_repository", map[string]any{"app": "cotizador", "repository": "acme/cotizador"})
	if out.DeploymentID != "" {
		t.Fatalf("took an older push's deployment: %+v", out)
	}
}

func TestConnectRepositoryErrors(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	_, res := call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": "acme/no-existe"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || body["field"] != "repository" || !strings.Contains(body["next_step"], "list_repositories") {
		t.Fatalf("unknown repo: %v", body)
	}
	_, res = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": "acme"}) // a prefix is not a match
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" {
		t.Fatalf("partial name: %v", body)
	}
	_, res = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "cotizador", "repository": " "})
	if body := errorBody(t, res); body["field"] != "repository" {
		t.Fatalf("blank repo: %v", body)
	}
	_, res = call[sourcemcp.ConnectRepositoryOut](t, cs, "connect_repository", map[string]any{"app": "no-existe", "repository": "acme/cotizador"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || body["field"] != "app" || !strings.Contains(body["next_step"], "list_apps") {
		t.Fatalf("unknown app: %v", body)
	}
	stranger := f.connect(t, identity.NewOrganizationID(), writer...)
	for _, ref := range []string{"cotizador", f.app.String()} {
		_, res = call[sourcemcp.ConnectRepositoryOut](t, stranger, "connect_repository", map[string]any{"app": ref, "repository": "acme/cotizador"})
		if body := errorBody(t, res); body["code"] != "NOT_FOUND" || body["field"] != "app" {
			t.Fatalf("another organization's app %q: %v", ref, body)
		}
	}
	if len(f.gh.connected) != 0 {
		t.Fatalf("connected anyway: %+v", f.gh.connected)
	}
}

func TestPublishNow(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	deployment := release.NewDeploymentID()
	f.gh.onList = func(n int) {
		if n == 2 {
			f.gh.deploy(deployment)
		}
	}
	out, res := call[sourcemcp.PublishNowOut](t, cs, "publish_now", map[string]any{"app": "cotizador"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.DeploymentID != deployment.String() || out.Repository != "acme/cotizador" || out.Branch != "main" || out.Commit != strings.Repeat("cd", 20) ||
		!strings.Contains(out.NextStep, "get_deployment") || f.gh.published != 1 {
		t.Fatalf("out: %+v", out)
	}

	// Still queued after the wait.
	f.gh.onList = nil
	out, _ = call[sourcemcp.PublishNowOut](t, cs, "publish_now", map[string]any{"app": "cotizador"})
	if out.DeploymentID != "" || !strings.Contains(out.NextStep, "unos segundos") {
		t.Fatalf("queued: %+v", out)
	}
}

func TestPublishNowWithoutAConnection(t *testing.T) {
	f := newFixture(t)
	f.gh.publishErr = errx.NotFound("repository connection not found")
	_, res := call[sourcemcp.PublishNowOut](t, f.connect(t, f.org, writer...), "publish_now", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || !strings.Contains(body["next_step"], "connect_repository") {
		t.Fatalf("not connected: %v", body)
	}
	// Other failures pass through.
	f.gh.publishErr = errx.Business("the project is archived").WithCode(project.CodeArchived)
	_, res = call[sourcemcp.PublishNowOut](t, f.connect(t, f.org, writer...), "publish_now", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] != project.CodeArchived {
		t.Fatalf("archived: %v", body)
	}
}
