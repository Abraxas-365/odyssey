package releasemcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasemcp"
)

type canRunServices struct{}

func (canRunServices) CanRunServices(context.Context, identity.OrganizationID, []project.EnvironmentID) (bool, error) {
	return true, nil
}

// allowAll is IT's rules letting every change through.
type allowAll struct{}

func (allowAll) Check(context.Context, identity.OrganizationID, gate.Request) (gate.Verdict, error) {
	return gate.Verdict{Effect: gate.Allow}, nil
}

// withServices makes the app a web "/" + api "/api" + worker app.
func withServices(t *testing.T, f *fixture) {
	t.Helper()
	f.projects.UseRuntime(canRunServices{})
	f.projects.UsePolicy(allowAll{})
	if _, err := f.projects.SetServices(context.Background(), f.org, f.app, project.SetServices{UpdatedBy: user, Items: []project.ServiceSpec{
		{Name: "web", Kind: project.ServiceWeb, Root: "apps/web", Route: "/"},
		{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true},
		{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/api"},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployFolderPassesRebuild(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	out, res := call[releasemcp.DeployFolderOut](t, cs, "deploy_folder", map[string]any{"app": "cotizador", "rebuild": true})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if len(f.commands.issued) != 1 || !f.commands.issued[0].Rebuild {
		t.Fatalf("issued: %+v", f.commands.issued)
	}
	// A simple app: no services, the folder of the app.
	if len(out.Services) != 0 || !strings.Contains(out.NextStep, "dentro de la carpeta de la app") {
		t.Fatalf("simple: %+v", out)
	}
	if _, res = call[releasemcp.DeployFolderOut](t, cs, "deploy_folder", map[string]any{"app": "cotizador"}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if f.commands.issued[1].Rebuild {
		t.Fatal("rebuild by default")
	}
}

func TestDeployFolderOfSeveralServices(t *testing.T) {
	f := newFixture(t)
	withServices(t, f)
	out, res := call[releasemcp.DeployFolderOut](t, f.connect(t, f.org, writer...), "deploy_folder", map[string]any{"app": "cotizador"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	want := []releasemcp.FolderService{{Name: "web", Root: "apps/web"}, {Name: "api", Root: "apps/api"}, {Name: "jobs", Root: "apps/api"}}
	if len(out.Services) != 3 || out.Services[0] != want[0] || out.Services[1] != want[1] || out.Services[2] != want[2] {
		t.Fatalf("services: %+v", out.Services)
	}
	if !strings.Contains(out.NextStep, "apps/web, apps/api") || !strings.Contains(out.NextStep, "no dentro de una de ellas") ||
		strings.Contains(out.NextStep, "apps/api, apps/api") || !strings.Contains(out.NextStep, "get_deployment") {
		t.Fatalf("next_step: %s", out.NextStep)
	}
}

func TestGetAppShowsItsServices(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)

	// A simple app, nothing online yet: its one service is not published.
	out, res := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if res.IsError || len(out.Services) != 1 || out.Services[0].Name != "web" || out.Services[0].Route != "/" || out.Services[0].State != "not_published" {
		t.Fatalf("simple: %+v (%v)", out.Services, res.Content)
	}
	attention := out.NeedsAttention

	withServices(t, f)
	f.releases.states = []release.ServiceState{
		{Name: "web", Kind: project.ServiceWeb, Route: "/", Phase: release.PhaseReady, ReadyReplicas: 2, Replicas: 2},
		{Name: "api", Kind: project.ServiceWeb, Route: "/api", Phase: release.PhaseProgressing, ReadyReplicas: 0, Replicas: 1},
	}
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if len(out.Services) != 3 {
		t.Fatalf("services: %+v", out.Services)
	}
	web, api, jobs := out.Services[0], out.Services[1], out.Services[2]
	if web.State != "online" || web.ReadyReplicas != 2 || web.Replicas != 2 || web.Root != "apps/web" || web.Kind != "web" {
		t.Fatalf("web: %+v", web)
	}
	if api.State != "starting" || api.Route != "/api" || !api.StripPrefix || api.Root != "apps/api" {
		t.Fatalf("api: %+v", api)
	}
	// Configured but absent from what is online.
	if jobs.State != "not_published" || jobs.Kind != "worker" || jobs.Route != "" {
		t.Fatalf("jobs: %+v", jobs)
	}
	if out.NeedsAttention != attention {
		t.Fatalf("needs_attention changed without a failure: %v", out.NeedsAttention)
	}

	// A failed service needs attention and says why.
	f.releases.states[1] = release.ServiceState{Name: "api", Kind: project.ServiceWeb, Route: "/api", Phase: release.PhaseFailed, Message: "crash", Replicas: 1}
	f.releases.states = append(f.releases.states, release.ServiceState{Name: "jobs", Kind: project.ServiceWorker, Replicas: 1}) // Phase "" = unknown
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Services[1].State != "failed" || out.Services[1].Message != "crash" || out.Services[2].State != "unknown" || !out.NeedsAttention {
		t.Fatalf("failed: %+v attention=%v", out.Services, out.NeedsAttention)
	}
}

func TestGetAppSurvivesUnreadableServiceStates(t *testing.T) {
	f := newFixture(t)
	f.releases.statesErr = errors.New("cluster down")
	out, res := call[releasemcp.GetAppOut](t, f.connect(t, f.org, reader...), "get_app", map[string]any{"app": "cotizador"})
	if res.IsError || len(out.Services) != 1 || out.Services[0].State != "unknown" || out.Services[0].Name != "web" {
		t.Fatalf("states error: %+v (%v)", out.Services, res.Content)
	}
}

// sourceDeployment is a failed or building deployment from source with one
// build per service name, and what each build says.
func sourceDeployment(f *fixture, status release.Status, step string, services map[string]build.Build, order ...string) release.Deployment {
	d := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: status, Step: step,
		Reason: release.ReasonDeploy, CreatedAt: t0.Add(9 * time.Hour)}
	for _, name := range order {
		b := services[name]
		b.ID, b.Organization, b.Project = build.NewID(), f.org, f.app
		if f.builds.byID == nil {
			f.builds.byID = map[build.ID]build.Build{}
		}
		f.builds.byID[b.ID] = b
		d.Builds = append(d.Builds, release.ServiceBuild{Name: name, Kind: project.ServiceWeb, Build: b.ID})
	}
	if len(d.Builds) > 0 {
		first := d.Builds[0].Build
		d.Build = &first
	}
	f.releases.deployments = append([]release.Deployment{d}, f.releases.deployments...)
	return d
}

func TestGetDeploymentShowsBuildsReusedAndChanged(t *testing.T) {
	f := newFixture(t)
	earlier := build.NewID()
	d := sourceDeployment(f, release.StatusApplying, release.StepApply, map[string]build.Build{
		"web": {Status: build.StatusSucceeded, ReusedFrom: &earlier},
		"api": {Status: build.StatusSucceeded},
	}, "web", "api")
	d.Builds = append(d.Builds, release.ServiceBuild{Name: "ghost", Kind: project.ServiceWorker, Build: build.NewID()}) // not readable
	d.Changed, d.Rebuild = []string{"api"}, true
	f.releases.deployments[0] = d
	out, res := call[releasemcp.GetDeploymentOut](t, f.connect(t, f.org, reader...), "get_deployment", map[string]any{"app": "cotizador", "deployment_id": d.ID.String()})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if len(out.Changed) != 1 || out.Changed[0] != "api" || !out.Rebuild || len(out.Builds) != 3 {
		t.Fatalf("out: %+v", out)
	}
	if b := out.Builds[0]; b.Service != "web" || b.Status != "succeeded" || !b.Reused || b.BuildID != d.Builds[0].Build.String() {
		t.Fatalf("web: %+v", b)
	}
	if b := out.Builds[1]; b.Service != "api" || b.Status != "succeeded" || b.Reused {
		t.Fatalf("api: %+v", b)
	}
	if b := out.Builds[2]; b.Service != "ghost" || b.Status != "unknown" || b.Reused {
		t.Fatalf("ghost: %+v", b)
	}

	// Unknown changes (nil) are not reported; none ([]) is, as absent too (omitempty).
	d.Changed = nil
	f.releases.deployments[0] = d
	out, _ = call[releasemcp.GetDeploymentOut](t, f.connect(t, f.org, reader...), "get_deployment", map[string]any{"app": "cotizador", "deployment_id": d.ID.String()})
	if out.Changed != nil {
		t.Fatalf("changed: %v", out.Changed)
	}
}

func TestGetDeploymentNamesTheFailedService(t *testing.T) {
	f := newFixture(t)
	d := sourceDeployment(f, release.StatusFailed, release.StepBuild, map[string]build.Build{
		"web": {Status: build.StatusSucceeded},
		"api": {Status: build.StatusFailed, Error: &build.Failure{Code: "BUILD_FAILED", Message: "No compila.", Detail: "exit 1"}},
	}, "web", "api")
	out, _ := call[releasemcp.GetDeploymentOut](t, f.connect(t, f.org, reader...), "get_deployment", map[string]any{"app": "cotizador", "deployment_id": d.ID.String()})
	if !strings.Contains(out.NextStep, "api") || !strings.Contains(out.NextStep, "get_build_logs") || !strings.Contains(out.NextStep, "service=api") || strings.Contains(out.NextStep, "web,") {
		t.Fatalf("next_step: %s", out.NextStep)
	}
	if len(out.Builds) != 2 || out.Builds[1].Failure == nil || out.Builds[1].Failure.Code != "BUILD_FAILED" || out.Builds[0].Failure != nil {
		t.Fatalf("builds: %+v", out.Builds)
	}
}

func TestListVersionsShowsChanged(t *testing.T) {
	f := newFixture(t)
	d := f.releases.deployments[1]
	d.Changed = []string{"api", "web"}
	f.releases.deployments[1] = d
	out, _ := call[releasemcp.ListVersionsOut](t, f.connect(t, f.org, reader...), "list_versions", map[string]any{"app": "cotizador"})
	if len(out.Items) != 3 || len(out.Items[1].Changed) != 2 || out.Items[1].Changed[0] != "api" || out.Items[0].Changed != nil {
		t.Fatalf("items: %+v", out.Items)
	}
}

func TestGetBuildLogsPicksTheService(t *testing.T) {
	f := newFixture(t)
	d := sourceDeployment(f, release.StatusFailed, release.StepBuild, map[string]build.Build{
		"web": {Status: build.StatusSucceeded},
		"api": {Status: build.StatusFailed, Error: &build.Failure{Code: "BUILD_FAILED", Message: "No compila."}},
		"ui":  {Status: build.StatusSucceeded},
	}, "web", "api", "ui")
	f.builds.fill(2)
	cs := f.connect(t, f.org, reader...)

	// No service: the one that failed.
	out, res := call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "deployment_id": d.ID.String()})
	if res.IsError || out.Service != "api" || out.BuildID != d.Builds[1].Build.String() || out.BuildStatus != "failed" || out.Failure == nil {
		t.Fatalf("default: %+v (%v)", out, res.Content)
	}
	// By name.
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "deployment_id": d.ID.String(), "service": "ui"})
	if out.Service != "ui" || out.BuildID != d.Builds[2].Build.String() || out.BuildStatus != "succeeded" {
		t.Fatalf("ui: %+v", out)
	}
	// Unknown name.
	_, res = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "deployment_id": d.ID.String(), "service": "nope"})
	body := errorBody(t, res)
	if body["field"] != "service" || !strings.Contains(body["next_step"], "web, api, ui") {
		t.Fatalf("unknown: %v", body)
	}

	// Nothing failed: the first one.
	ok := sourceDeployment(f, release.StatusBuilding, release.StepBuild, map[string]build.Build{
		"web": {Status: build.StatusRunning}, "api": {Status: build.StatusQueued}}, "web", "api")
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "deployment_id": ok.ID.String()})
	if out.Service != "web" || out.BuildID != ok.Builds[0].Build.String() {
		t.Fatalf("first: %+v", out)
	}
}

func TestGetBuildLogsFallsBackToTheFirstBuild(t *testing.T) {
	f := newFixture(t)
	d := withBuild(f, build.StatusRunning, f.app) // Build only, no Builds
	f.builds.fill(1)
	out, res := call[releasemcp.GetBuildLogsOut](t, f.connect(t, f.org, reader...), "get_build_logs", map[string]any{"app": "cotizador", "service": "web"})
	if res.IsError || out.BuildID != d.Build.String() || out.Service != "" {
		t.Fatalf("fallback: %+v (%v)", out, res.Content)
	}
}
