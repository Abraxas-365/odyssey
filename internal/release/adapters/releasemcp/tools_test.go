package releasemcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasemcp"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	user  = identity.MustParseUserID("11111111-1111-4111-8111-111111111111")
	other = identity.MustParseUserID("22222222-2222-4222-8222-222222222222")
	t0    = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
)

// releases is a release.Queries over the deployments of one environment,
// newest first, scoped by organization like the service.
type releases struct {
	org             identity.OrganizationID
	env             project.EnvironmentID
	url             string
	current         *release.Deployment
	release         *release.Release // the serving release
	active          *release.Deployment
	deployments     []release.Deployment // newest first
	finds           int                  // FindDeployment calls
	onFind          func(n int)          // runs at the start of the nth FindDeployment
	settings        *release.Settings    // nil: the defaults
	appliedSettings *int64               // the revision the cluster was last told
	uses            *release.Uses        // nil: nothing to attend to
	usesErr         error                // Uses fails (release's own data)
	states          []release.ServiceState
	statesErr       error // ServiceStates fails
}

func (r *releases) place(org identity.OrganizationID, env project.EnvironmentID) error {
	if org != r.org || env != r.env {
		return errx.NotFound("environment not found")
	}
	return nil
}

func (r *releases) Current(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID) (release.Current, error) {
	if err := r.place(org, env); err != nil {
		return release.Current{}, err
	}
	return release.Current{Environment: env, URL: r.url, Release: r.release, Deployment: r.current, Active: r.active, AppliedSettingsRevision: r.appliedSettings}, nil
}

func (r *releases) FindDeployment(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID, id release.DeploymentID) (release.Deployment, error) {
	if err := r.place(org, env); err != nil {
		return release.Deployment{}, err
	}
	r.finds++
	if r.onFind != nil {
		r.onFind(r.finds)
	}
	for _, d := range r.deployments {
		if d.ID == id {
			return d, nil
		}
	}
	return release.Deployment{}, errx.NotFound("deployment not found")
}

func (r *releases) ListDeployments(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID, filter release.DeploymentFilter, page query.Pagination) (query.Paginated[release.Deployment], error) {
	if err := r.place(org, env); err != nil {
		return query.Paginated[release.Deployment]{}, err
	}
	var all []release.Deployment
	for _, d := range r.deployments {
		if len(filter.Status) == 0 || slices.Contains(filter.Status, d.Status) {
			all = append(all, d)
		}
	}
	from := min(page.Offset, len(all))
	to := min(from+page.Limit, len(all))
	return query.NewPaginated(all[from:to], len(all), page), nil
}

func (r *releases) Settings(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID) (release.Settings, error) {
	if err := r.place(org, env); err != nil {
		return release.Settings{}, err
	}
	if r.settings == nil {
		return release.DefaultSettings(org, env), nil
	}
	return *r.settings, nil
}

func (r *releases) AccessProtectionAvailable(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (bool, error) {
	return true, nil
}

func (r *releases) Secrets(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID) (release.Secrets, error) {
	if err := r.place(org, env); err != nil {
		return release.Secrets{}, err
	}
	return release.Secrets{Items: []release.SecretView{}}, nil
}

func (r *releases) Serving(context.Context, identity.OrganizationID, []project.EnvironmentID) ([]release.Serving, error) {
	return nil, nil
}

func (r *releases) ServiceStates(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) ([]release.ServiceState, error) {
	if r.statesErr != nil {
		return nil, r.statesErr
	}
	return append([]release.ServiceState{}, r.states...), nil
}

func (r *releases) Uses(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID) (release.Uses, error) {
	if err := r.place(org, env); err != nil {
		return release.Uses{}, err
	}
	if r.usesErr != nil {
		return release.Uses{}, r.usesErr
	}
	if r.uses != nil {
		return *r.uses, nil
	}
	s := release.DefaultSettings(org, env)
	if r.settings != nil {
		s = *r.settings
	}
	return release.NewUses(release.UsesInput{Settings: s, Serving: r.release != nil, Active: r.active != nil, Applied: r.appliedSettings,
		Secrets: release.Secrets{Items: []release.SecretView{}, Cluster: release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}}}, AI: release.AIState{Status: "off"},
		Egress: release.ClusterEgress{Available: true, Mode: "internet", State: release.EgressApplied, Enforced: true}}), nil
}

func (r *releases) FindRelease(context.Context, identity.OrganizationID, project.ID, release.ID) (release.Release, error) {
	return release.Release{}, errx.NotFound("release not found")
}

type fixture struct {
	projects *projectsvc.Service
	releases *releases
	commands *commands
	builds   *builds
	options  releasemcp.Options
	org      identity.OrganizationID
	app      project.ID
	env      project.EnvironmentID
}

// newFixture has an app "cotizador" in org with three deployments: v1 (ready,
// from a folder), v2 (ready, from a commit, serving) and v3 (failed).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{projects: projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{}), org: identity.NewOrganizationID()}
	var err error
	if f.app, err = f.projects.Create(context.Background(), f.org, project.Create{Name: "Cotizador comercial", Slug: "cotizador", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	env, err := f.projects.FindEnvironmentBySlug(context.Background(), f.org, f.app, project.ProductionSlug)
	if err != nil {
		t.Fatal(err)
	}
	f.env = env.ID
	dep := func(n int, status release.Status, mod func(*release.Deployment)) release.Deployment {
		d := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: status,
			Reason: release.ReasonDeploy, Step: release.StepPromote, Image: "registry.local:5000/acme/cotizador:v" + string(rune('0'+n)),
			RequestedBy: user, CreatedAt: t0.Add(time.Duration(n) * time.Hour)}
		if mod != nil {
			mod(&d)
		}
		return d
	}
	v1 := dep(1, release.StatusReady, func(d *release.Deployment) {
		d.Source = &release.DeploymentSource{Name: "cotizador.tar.gz"}
		d.RequestedByName, d.RequestedVia = "Ana Torres", release.ViaAssistantPrefix+"claude_code"
	})
	v2 := dep(2, release.StatusReady, func(d *release.Deployment) {
		d.RequestedBy = other
		d.Source = &release.DeploymentSource{Name: "repo", Git: &build.GitRef{Provider: "github", Repository: "acme/cotizador", Branch: "main",
			Commit: strings.Repeat("ab", 20), Title: "Descuento del 20%", Author: "diego"}}
	})
	v3 := dep(3, release.StatusFailed, func(d *release.Deployment) {
		d.Step = release.StepWaitReady
		d.Error = &release.Failure{Code: release.CodeUnhealthy, Message: "La app no respondió en /health.", Detail: "connection refused"}
	})
	f.commands = &commands{}
	f.builds = &builds{org: f.org, project: f.app}
	f.options = releasemcp.Options{ConsoleURL: "https://odicea.test/", APIURL: "https://api.odicea.test/", PollInterval: 5 * time.Millisecond}
	f.releases = &releases{org: f.org, env: f.env, url: "https://cotizador.apps.test", current: &v2, deployments: []release.Deployment{v3, v2, v1}}
	return f
}

func (f *fixture) connect(t *testing.T, org identity.OrganizationID, perms ...string) *mcp.ClientSession {
	t.Helper()
	reg, err := mcpx.NewRegistry(releasemcp.Tools(f.projects, f.commands, f.releases, f.builds, f.options))
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

var reader = []string{permission.ProjectsRead, permission.DeploymentsRead}

func TestToolsAreListedOnlyWithBothPermissions(t *testing.T) {
	f := newFixture(t)
	list := func(perms ...string) []string {
		res, err := f.connect(t, f.org, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
			if tool.OutputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("%s: want an output schema and a read-only hint", tool.Name)
			}
		}
		slices.Sort(names)
		return names
	}
	if got := list(reader...); !slices.Equal(got, []string{"get_app", "get_build_logs", "get_deployment", "list_versions"}) {
		t.Fatalf("reader lists %v", got)
	}
	if got := list(permission.ProjectsRead); len(got) != 0 {
		t.Fatalf("projects:read alone lists %v", got)
	}
	if got := list(permission.DeploymentsRead); len(got) != 0 {
		t.Fatalf("deployments:read alone lists %v", got)
	}
}

func TestGetAppBySlugAndByID(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	for _, ref := range []string{"cotizador", "  Cotizador ", f.app.String()} {
		out, res := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": ref})
		if res.IsError {
			t.Fatalf("%q: %+v", ref, res.Content)
		}
		if out.ID != f.app.String() || out.Slug != "cotizador" || out.Name != "Cotizador comercial" || out.ConsoleURL != "https://odicea.test/apps/"+f.app.String() {
			t.Fatalf("%q: %+v", ref, out)
		}
	}
}

func TestGetAppShowsWhatServesAndWhatFailed(t *testing.T) {
	f := newFixture(t)
	out, _ := call[releasemcp.GetAppOut](t, f.connect(t, f.org, reader...), "get_app", map[string]any{"app": "cotizador"})
	if out.State != "online" || out.URL != "https://cotizador.apps.test" || !out.NeedsAttention || out.Active != nil {
		t.Fatalf("state: %+v", out)
	}
	cur := out.Current
	if cur == nil || !cur.IsCurrent || cur.Status != "ready" || cur.Commit != strings.Repeat("ab", 20) || cur.Author != "diego" ||
		cur.Title != "Descuento del 20%" || cur.Repository != "acme/cotizador" || cur.CreatedBy != other.String() || cur.Image != "cotizador:v2" {
		t.Fatalf("current: %+v", cur)
	}
	last := out.LastDeployment
	if last == nil || last.Status != "failed" || last.IsCurrent || last.Step != "wait_ready" || last.Error == nil ||
		last.Error.Code != release.CodeUnhealthy || last.Error.Detail != "connection refused" {
		t.Fatalf("last: %+v", last)
	}
}

func TestGetAppStates(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)

	f.releases.active = &release.Deployment{ID: release.NewDeploymentID(), Status: release.StatusBuilding, Step: release.StepBuild, Reason: release.ReasonDeploy, CreatedAt: t0}
	if out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"}); out.State != "publishing" || out.Active == nil || out.Active.Status != "building" {
		t.Fatalf("publishing: %+v", out)
	}

	f.releases.active, f.releases.current = nil, nil // never went live, last attempt failed
	if out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"}); out.State != "failed" || out.URL != "" || out.Current != nil || !out.NeedsAttention {
		t.Fatalf("failed: %+v", out)
	}

	f.releases.deployments = nil
	if out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"}); out.State != "not_published" || out.LastDeployment != nil || out.NeedsAttention {
		t.Fatalf("not published: %+v", out)
	}
}

func TestAnotherOrganizationsAppIsNotFound(t *testing.T) {
	f := newFixture(t)
	stranger := f.connect(t, identity.NewOrganizationID(), writer...)
	for _, tool := range []string{"get_app", "list_versions", "get_deployment", "get_build_logs", "rollback", "deploy_folder", "deploy_image"} {
		for _, ref := range []string{"cotizador", f.app.String()} {
			args := map[string]any{"app": ref}
			if tool == "deploy_image" {
				args["image"] = "reg/x@sha256:" + strings.Repeat("a", 64)
			}
			_, res := call[releasemcp.GetAppOut](t, stranger, tool, args)
			body := errorBody(t, res)
			if body["code"] != "NOT_FOUND" || body["field"] != "app" || !strings.Contains(body["next_step"], "list_apps") {
				t.Fatalf("%s %q: %v", tool, ref, body)
			}
		}
	}
	_, res := call[releasemcp.GetAppOut](t, f.connect(t, f.org, reader...), "get_app", map[string]any{"app": "no-existe"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || !strings.Contains(body["next_step"], "list_apps") {
		t.Fatalf("unknown slug: %v", body)
	}
}

func TestGetAppNeedsAnApp(t *testing.T) {
	f := newFixture(t)
	_, res := call[releasemcp.GetAppOut](t, f.connect(t, f.org, reader...), "get_app", map[string]any{"app": " "})
	if body := errorBody(t, res); body["field"] != "app" || body["code"] == "" {
		t.Fatalf("blank app: %v", body)
	}
}

func TestListVersions(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	out, res := call[releasemcp.ListVersionsOut](t, cs, "list_versions", map[string]any{"app": "cotizador"})
	if res.IsError || out.App != "cotizador" || out.Total != 3 || len(out.Items) != 3 || out.NextOffset != nil || out.Limit != 10 {
		t.Fatalf("list: %+v", out)
	}
	if got := []string{out.Items[0].Status, out.Items[1].Status, out.Items[2].Status}; !slices.Equal(got, []string{"failed", "ready", "ready"}) {
		t.Fatalf("order: %v", got)
	}
	if out.Items[0].IsCurrent || !out.Items[1].IsCurrent || out.Items[2].IsCurrent {
		t.Fatalf("is_current: %+v", out.Items)
	}
	if v := out.Items[2]; v.CreatedBy != user.String() || v.Commit != "" || v.Image != "cotizador:v1" || v.Reason != "deploy" || !v.CreatedAt.Equal(t0.Add(time.Hour)) ||
		v.CreatedByName != "Ana Torres" || v.RequestedVia != "assistant:claude_code" {
		t.Fatalf("folder version: %+v", v)
	}

	page, _ := call[releasemcp.ListVersionsOut](t, cs, "list_versions", map[string]any{"app": f.app.String(), "limit": 2})
	if len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != 2 || page.Total != 3 {
		t.Fatalf("page 1: %+v", page)
	}
	rest, _ := call[releasemcp.ListVersionsOut](t, cs, "list_versions", map[string]any{"app": "cotizador", "limit": 2, "offset": 2})
	if len(rest.Items) != 1 || rest.NextOffset != nil || rest.Items[0].Status != "ready" {
		t.Fatalf("page 2: %+v", rest)
	}
}

func TestListVersionsCapsTheLimit(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	out, _ := call[releasemcp.ListVersionsOut](t, cs, "list_versions", map[string]any{"app": "cotizador", "limit": 5000})
	if out.Limit != 50 {
		t.Fatalf("limit not capped: %+v", out)
	}
	_, res := call[releasemcp.ListVersionsOut](t, cs, "list_versions", map[string]any{"app": "cotizador", "offset": -1})
	if !res.IsError {
		t.Fatal("negative offset accepted")
	}
}

func TestPlatformStartedVersionsHaveNoUser(t *testing.T) {
	f := newFixture(t)
	auto := release.Deployment{ID: release.NewDeploymentID(), Status: release.StatusReady, Reason: release.ReasonAutoRollback, Image: "reg/x:v1", CreatedAt: t0}
	f.releases.deployments = []release.Deployment{auto}
	out, _ := call[releasemcp.ListVersionsOut](t, f.connect(t, f.org, reader...), "list_versions", map[string]any{"app": "cotizador"})
	if v := out.Items[0]; v.CreatedBy != "" || v.Reason != "auto_rollback" {
		t.Fatalf("auto rollback: %+v", v)
	}
}

// get_app carries what the app uses, and needs_attention is the last
// publication failing OR something pending in uses.attention.
func TestGetAppUsesAndNeedsAttention(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	f.releases.deployments = f.releases.deployments[1:] // the failed publication is gone: only v2 (ready) and v1
	out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.NeedsAttention || len(out.Uses.Attention) != 0 || out.Uses.Access.Mode != "team" || !out.Uses.Identity.Verifiable ||
		out.Uses.Resources == nil || out.Uses.Secrets.Missing == nil || out.Uses.Egress.Mode != "internet" {
		t.Fatalf("quiet app: %+v", out)
	}

	in := release.DefaultSettings(f.org, f.env)
	in.Secrets = []release.SecretRef{{Name: "STRIPE_API_KEY", Source: release.SecretManaged}}
	u := release.NewUses(release.UsesInput{Settings: in, Secrets: release.Secrets{Items: []release.SecretView{
		{SecretRef: in.Secrets[0], State: release.SecretStateMissing}}, Cluster: release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}}}, AI: release.AIState{Status: "off"}})
	f.releases.uses = &u
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if !out.NeedsAttention || len(out.Uses.Attention) != 1 || out.Uses.Attention[0].Kind != "secret_missing" || out.Uses.Attention[0].Who != "owner" ||
		len(out.Uses.Secrets.Missing) != 1 || out.Uses.Secrets.Missing[0] != "STRIPE_API_KEY" || out.State != "online" {
		t.Fatalf("secret missing: %+v", out)
	}

	// something in progress (who wait) is not a reason to attend
	in.Secrets = nil
	in.AI.Enabled = true
	u = release.NewUses(release.UsesInput{Settings: in, Secrets: release.Secrets{Items: []release.SecretView{}, Cluster: release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}}},
		AI: release.AIState{Available: true, Status: "enabling"}})
	f.releases.uses = &u
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.NeedsAttention || len(out.Uses.Attention) != 1 || out.Uses.Attention[0].Who != "wait" {
		t.Fatalf("waiting: %+v", out)
	}
}

// A section that could not be read comes back null and named in
// uses.unavailable; get_app itself still answers, and it answers even when the
// whole read model fails.
func TestGetAppDegradesWhenUsesDoes(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	f.releases.deployments = f.releases.deployments[1:]
	in := release.DefaultSettings(f.org, f.env)
	u := release.NewUses(release.UsesInput{Settings: in, AI: release.AIState{Status: "off"}, Unavailable: []release.UsesSection{release.SectionSecrets, release.SectionEgress}})
	f.releases.uses = &u
	out, res := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if res.IsError || out.Uses.Secrets != nil || out.Uses.Egress != nil || out.Uses.AI == nil || len(out.Uses.Unavailable) != 2 ||
		out.Uses.Unavailable[0] != "secrets" || out.Uses.Unavailable[1] != "egress" || out.NeedsAttention || out.State != "online" {
		t.Fatalf("partial uses: %+v %+v", out, res)
	}

	f.releases.uses = nil
	f.releases.usesErr = errors.New("database down")
	out, res = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if res.IsError || out.Slug != "cotizador" || out.State != "online" || out.Uses.Secrets != nil || out.Uses.AI != nil || out.Uses.Egress != nil ||
		len(out.Uses.Unavailable) != 4 || out.Uses.Attention == nil || out.Uses.Resources == nil {
		t.Fatalf("no uses at all: %+v %+v", out, res)
	}
}
