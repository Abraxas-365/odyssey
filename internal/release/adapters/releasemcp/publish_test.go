package releasemcp_test

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var writer = []string{permission.ProjectsRead, permission.DeploymentsRead, permission.DeploymentsCreate, permission.DeploymentsRollback}

// commands is a release.Commands that records what the tools ask.
type commands struct {
	issued     []release.IssueUpload
	issuedEnv  project.EnvironmentID
	deployed   []release.Deploy
	rolledBack []release.DeploymentID
	rollbackBy identity.UserID
	clients    []string // event.Meta client of each call
	rollbackFn func(release.DeploymentID) (release.Deployment, error)
	access     []release.SetAccess
	accessFn   func(release.SetAccess) (release.AccessChange, error)
	env        []release.SetEnv
	requests   []release.RequestSecret
	requestFn  func(release.RequestSecret) (release.SecretView, error)
}

func (c *commands) SetEnv(_ context.Context, org identity.OrganizationID, _ project.ID, env project.EnvironmentID, in release.SetEnv) (release.SettingsChange, error) {
	c.env = append(c.env, in)
	s := release.DefaultSettings(org, env)
	s.Revision = 1
	for k, v := range in.Set {
		s.Env[k] = v
	}
	return release.SettingsChange{Settings: s}, nil
}

func (c *commands) RequestSecret(_ context.Context, _ identity.OrganizationID, _ project.ID, _ project.EnvironmentID, in release.RequestSecret) (release.SecretView, error) {
	c.requests = append(c.requests, in)
	if c.requestFn != nil {
		return c.requestFn(in)
	}
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return release.SecretView{}, err
	}
	return release.SecretView{SecretRef: release.SecretRef{Name: in.Name, Source: release.SecretManaged}, State: release.SecretStateMissing}, nil
}

func (c *commands) LoadSecret(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, release.LoadSecret) (release.SettingsChange, error) {
	return release.SettingsChange{}, errx.Internal("an assistant never loads a value")
}

func (c *commands) SetExternalSecret(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, release.SetExternalSecret) (release.SettingsChange, error) {
	return release.SettingsChange{}, errx.Internal("not an assistant tool")
}

func (c *commands) RemoveSecret(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, release.RemoveSecret) (release.SettingsChange, error) {
	return release.SettingsChange{}, errx.Internal("not an assistant tool")
}

func (c *commands) SetAccess(_ context.Context, org identity.OrganizationID, p project.ID, env project.EnvironmentID, in release.SetAccess) (release.AccessChange, error) {
	c.access = append(c.access, in)
	if c.accessFn != nil {
		return c.accessFn(in)
	}
	return release.AccessChange{Settings: release.Settings{Environment: env, Organization: org, Revision: 1, Access: release.AccessSettings{Mode: in.Mode}}}, nil
}

func (c *commands) Deploy(ctx context.Context, org identity.OrganizationID, p project.ID, env project.EnvironmentID, in release.Deploy) (release.Deployment, error) {
	c.deployed = append(c.deployed, in)
	c.clients = append(c.clients, event.MetaFrom(ctx).Client)
	return release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: p, Environment: env, Status: release.StatusPending,
		Reason: release.ReasonDeploy, Image: in.Image, RequestedBy: in.RequestedBy, CreatedAt: t0}, nil
}

func (c *commands) DeploySource(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, release.DeploySource, io.Reader) (release.Deployment, error) {
	return release.Deployment{}, errx.Internal("not used")
}

func (c *commands) DeployUploaded(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID, release.DeployUploaded) (release.Deployment, error) {
	return release.Deployment{}, errx.Internal("not used")
}

func (c *commands) Rollback(ctx context.Context, org identity.OrganizationID, p project.ID, env project.EnvironmentID, id release.DeploymentID, in release.Rollback) (release.Deployment, error) {
	c.rolledBack = append(c.rolledBack, id)
	c.rollbackBy = in.RequestedBy
	c.clients = append(c.clients, event.MetaFrom(ctx).Client)
	if c.rollbackFn != nil {
		return c.rollbackFn(id)
	}
	return release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: p, Environment: env, Status: release.StatusPending,
		Reason: release.ReasonRollback, RollbackOf: &id, RequestedBy: in.RequestedBy, CreatedAt: t0}, nil
}

func (c *commands) IssueUpload(ctx context.Context, org identity.OrganizationID, p project.ID, env project.EnvironmentID, in release.IssueUpload) (release.UploadTicket, error) {
	c.issued = append(c.issued, in)
	c.issuedEnv = env
	c.clients = append(c.clients, event.MetaFrom(ctx).Client)
	return release.UploadTicket{ID: release.NewUploadTicketID(), Organization: org, Project: p, Environment: env, Token: "odu_SECRETTOKEN123",
		RequestedBy: in.RequestedBy, ExpiresAt: t0.Add(release.UploadTicketTTL)}, nil
}

func (c *commands) RedeemUpload(context.Context, string, io.Reader) (release.Deployment, error) {
	return release.Deployment{}, errx.Internal("not used")
}

// builds is a build.Queries over a few builds and their logs.
type builds struct {
	org     identity.OrganizationID
	project project.ID
	byID    map[build.ID]build.Build
	logs    []build.LogLine // seq = index+1
	asked   []build.LogQuery
}

func (b *builds) add(status build.Status, owner project.ID) build.ID {
	if b.byID == nil {
		b.byID = map[build.ID]build.Build{}
	}
	bl := build.Build{ID: build.NewID(), Organization: b.org, Project: owner, Status: status}
	b.byID[bl.ID] = bl
	return bl.ID
}

func (b *builds) Find(_ context.Context, org identity.OrganizationID, id build.ID) (build.Build, error) {
	bl, ok := b.byID[id]
	if !ok || org != b.org {
		return build.Build{}, errx.NotFound("build not found")
	}
	return bl, nil
}

func (b *builds) FindSource(context.Context, identity.OrganizationID, build.SourceID) (build.Source, error) {
	return build.Source{}, errx.NotFound("source not found")
}

func (b *builds) Logs(_ context.Context, _ identity.OrganizationID, _ build.ID, q build.LogQuery) (build.LogPage, error) {
	b.asked = append(b.asked, q)
	q = q.Normalize()
	from := min(int(q.After), len(b.logs))
	to := min(from+q.Limit, len(b.logs))
	page := build.LogPage{Lines: append([]build.LogLine{}, b.logs[from:to]...), Next: q.After, Finished: to == len(b.logs)}
	if len(page.Lines) > 0 {
		page.Next = page.Lines[len(page.Lines)-1].Seq
	}
	return page, nil
}

func (b *builds) fill(n int) {
	b.logs = nil
	for i := 1; i <= n; i++ {
		b.logs = append(b.logs, build.LogLine{Seq: int64(i), Stream: "build", Text: fmt.Sprintf("line %d", i)})
	}
}

func TestWriteToolsAreHiddenFromAReader(t *testing.T) {
	f := newFixture(t)
	list := func(perms ...string) []string {
		res, err := f.connect(t, f.org, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
			if tool.Name == "rollback" && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
				t.Fatalf("rollback must be destructive: %+v", tool.Annotations)
			}
		}
		slices.Sort(names)
		return names
	}
	if got := list(writer...); !slices.Equal(got, []string{"deploy_folder", "deploy_image", "get_app", "get_build_logs", "get_deployment", "list_versions", "rollback"}) {
		t.Fatalf("writer lists %v", got)
	}
	if got := list(permission.ProjectsRead, permission.DeploymentsRead, permission.DeploymentsCreate); slices.Contains(got, "rollback") || !slices.Contains(got, "deploy_folder") {
		t.Fatalf("without deployments:rollback: %v", got)
	}
	if got := list(permission.DeploymentsCreate); len(got) != 0 {
		t.Fatalf("deployments:create without projects:read lists %v", got)
	}

	// A reader that calls a write tool anyway is refused, and nothing happens.
	cs := f.connect(t, f.org, reader...)
	for tool, args := range map[string]map[string]any{
		"deploy_folder": {"app": "cotizador"},
		"deploy_image":  {"app": "cotizador", "image": "reg/x@sha256:" + strings.Repeat("a", 64)},
		"rollback":      {"app": "cotizador"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err == nil && !res.IsError {
			t.Fatalf("%s ran for a reader: %+v", tool, res)
		}
	}
	if c := f.commands; len(c.issued)+len(c.deployed)+len(c.rolledBack) != 0 {
		t.Fatalf("a command ran: %+v", c)
	}
}

func TestDeployFolderIssuesATicketAndTheCommand(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	out, res := call[releasemcp.DeployFolderOut](t, cs, "deploy_folder", map[string]any{
		"app": "cotizador", "name": "v2", "port": 3000, "health_path": "/health", "replicas": 2, "env": map[string]any{"LOG": "debug"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.UploadURL != "https://api.odicea.test/uploads/odu_SECRETTOKEN123" || !out.ExpiresAt.Equal(t0.Add(10*time.Minute)) || out.App != "cotizador" {
		t.Fatalf("out: %+v", out)
	}
	for _, want := range []string{"'" + out.UploadURL + "'", "git ls-files -z -co --exclude-standard", "COPYFILE_DISABLE=1 tar -cf - --null -T -",
		"--exclude=.git --exclude=node_modules --exclude='.env*' .", "| gzip -c | curl --fail-with-body -sS -H 'Content-Type: application/gzip' -T - "} {
		if !strings.Contains(out.Command, want) {
			t.Fatalf("command lacks %q: %s", want, out.Command)
		}
	}
	if !strings.Contains(out.Command, "odu_SECRETTOKEN123") || !strings.HasPrefix(out.Command, "if git rev-parse --is-inside-work-tree") {
		t.Fatalf("command: %s", out.Command)
	}
	if !strings.Contains(out.NextStep, "get_deployment") || !strings.Contains(out.NextStep, "10 minutos") {
		t.Fatalf("next_step: %s", out.NextStep)
	}
	c := f.commands
	if len(c.issued) != 1 || c.issuedEnv != f.env || c.issued[0].RequestedBy != user || c.issued[0].Name != "v2" {
		t.Fatalf("issued: %+v", c.issued)
	}
	if cfg := c.issued[0].Config; cfg.Port != 3000 || cfg.HealthPath != "/health" || cfg.Replicas != 2 || cfg.Env["LOG"] != "debug" {
		t.Fatalf("config: %+v", cfg)
	}
	if c.clients[0] != "mcp:https://claude.ai/cimd" {
		t.Fatalf("client: %q", c.clients[0])
	}
}

func TestDeployFolderNeedsAPIURL(t *testing.T) {
	f := newFixture(t)
	f.options.APIURL = ""
	_, res := call[releasemcp.DeployFolderOut](t, f.connect(t, f.org, writer...), "deploy_folder", map[string]any{"app": "cotizador"})
	if !res.IsError || len(f.commands.issued) != 0 {
		t.Fatalf("issued a ticket without an address: %+v", res)
	}
}

func TestDeployImage(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	image := "registry.local:5000/acme/cotizador@sha256:" + strings.Repeat("a", 64)
	out, res := call[releasemcp.DeployOut](t, cs, "deploy_image", map[string]any{"app": f.app.String(), "image": image, "port": 9000})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.DeploymentID == "" || out.Status != "pending" || out.App != "cotizador" || !strings.Contains(out.NextStep, "get_deployment") || !strings.Contains(out.NextStep, out.DeploymentID) {
		t.Fatalf("out: %+v", out)
	}
	d := f.commands.deployed
	if len(d) != 1 || d[0].Image != image || d[0].Port != 9000 || d[0].RequestedBy != user || f.commands.clients[0] != "mcp:https://claude.ai/cimd" {
		t.Fatalf("deployed: %+v", d)
	}
}

func TestGetDeploymentDefaults(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	v3, v2 := f.releases.deployments[0], f.releases.deployments[1]

	// Nothing active: the one online, with its URL.
	out, res := call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador"})
	if res.IsError || out.DeploymentID != v2.ID.String() || out.Status != "ready" || !out.Done || out.URL != "https://cotizador.apps.test" ||
		out.Repository != "acme/cotizador" || out.Commit != strings.Repeat("ab", 20) || out.ConsoleURL != "https://odicea.test/apps/"+f.app.String() ||
		!strings.Contains(out.NextStep, "https://cotizador.apps.test") || out.FinishedAt != nil && out.FinishedAt.IsZero() {
		t.Fatalf("online: %+v", out)
	}

	// By id: the failed one, with its failure and no URL.
	out, _ = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "deployment_id": v3.ID.String()})
	if out.Status != "failed" || !out.Done || out.URL != "" || out.Failure == nil || out.Failure.Code != release.CodeUnhealthy || out.Step != release.StepWaitReady {
		t.Fatalf("failed: %+v", out)
	}

	// The one in progress wins over the one online.
	active := release.Deployment{ID: release.NewDeploymentID(), Status: release.StatusApplying, Step: release.StepApply, Reason: release.ReasonDeploy, CreatedAt: t0,
		Steps: []release.Progress{{Name: release.StepApply, Status: release.StepRunning}, {Name: release.StepWaitReady, Status: release.StepPending}}}
	f.releases.active = &active
	out, _ = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador"})
	if out.DeploymentID != active.ID.String() || out.Done || out.URL != "" || len(out.Steps) != 2 || out.Steps[0].Status != "running" ||
		!strings.Contains(out.NextStep, "wait_seconds") {
		t.Fatalf("active: %+v", out)
	}

	// No current, no active: the newest.
	f.releases.active, f.releases.current = nil, nil
	out, _ = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador"})
	if out.DeploymentID != v3.ID.String() {
		t.Fatalf("newest: %+v", out)
	}

	// A ready deployment that is no longer the one online has no URL.
	out, _ = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "deployment_id": v2.ID.String()})
	if out.URL != "" || !out.Done {
		t.Fatalf("not online anymore: %+v", out)
	}
}

func TestGetDeploymentErrors(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	f.releases.deployments, f.releases.current = nil, nil
	_, res := call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || !strings.Contains(body["next_step"], "deploy_folder") {
		t.Fatalf("none: %v", body)
	}
	_, res = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "deployment_id": release.NewDeploymentID().String()})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || body["field"] != "deployment_id" || !strings.Contains(body["next_step"], "list_versions") {
		t.Fatalf("unknown id: %v", body)
	}
	_, res = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "deployment_id": "nope"})
	if body := errorBody(t, res); body["code"] != "VALIDATION" || body["field"] != "deployment_id" {
		t.Fatalf("bad id: %v", body)
	}
	for _, secs := range []int{-1, 46} {
		_, res = call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "wait_seconds": secs})
		if body := errorBody(t, res); body["field"] != "wait_seconds" {
			t.Fatalf("wait %d: %v", secs, body)
		}
	}
}

func TestGetDeploymentWaitsUntilItFinishes(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	v2 := f.releases.deployments[1]
	building := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: release.StatusBuilding,
		Step: release.StepBuild, Reason: release.ReasonDeploy, CreatedAt: t0.Add(5 * time.Hour)}
	f.releases.deployments = append([]release.Deployment{building}, f.releases.deployments...)
	f.releases.active = &building

	// After three polls it becomes ready and is promoted.
	f.releases.onFind = func(n int) {
		if n == 3 {
			done := building
			done.Status, done.Step = release.StatusReady, release.StepPromote
			f.releases.deployments[0] = done
			f.releases.current, f.releases.active = &done, nil
		}
	}
	start := time.Now()
	out, res := call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "wait_seconds": 30})
	if res.IsError || out.Status != "ready" || !out.Done || out.DeploymentID != building.ID.String() || out.URL != "https://cotizador.apps.test" {
		t.Fatalf("waited: %+v (%v)", out, res.Content)
	}
	if f.releases.finds != 3 || time.Since(start) > 5*time.Second {
		t.Fatalf("polled %d times in %v", f.releases.finds, time.Since(start))
	}
	_ = v2
}

func TestGetDeploymentWaitGivesUpAtTheDeadline(t *testing.T) {
	f := newFixture(t)
	f.options.PollInterval = time.Millisecond
	building := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: release.StatusBuilding,
		Step: release.StepBuild, Reason: release.ReasonDeploy, CreatedAt: t0, Build: new(build.ID)}
	f.releases.deployments = append([]release.Deployment{building}, f.releases.deployments...)
	f.releases.active = &building
	cs := f.connect(t, f.org, reader...)
	start := time.Now()
	out, res := call[releasemcp.GetDeploymentOut](t, cs, "get_deployment", map[string]any{"app": "cotizador", "wait_seconds": 1})
	if res.IsError || out.Done || out.Status != "building" || !strings.Contains(out.NextStep, "wait_seconds") || !strings.Contains(out.NextStep, "get_build_logs") {
		t.Fatalf("pending: %+v (%v)", out, res.Content)
	}
	if el := time.Since(start); el < 900*time.Millisecond || el > 5*time.Second {
		t.Fatalf("waited %v", el)
	}
	if f.releases.finds < 2 {
		t.Fatalf("polled %d times", f.releases.finds)
	}
}

func TestGetDeploymentPointsAtBuildLogsWhenTheBuildFails(t *testing.T) {
	f := newFixture(t)
	bid := f.builds.add(build.StatusFailed, f.app)
	failed := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: release.StatusFailed,
		Step: release.StepBuild, Reason: release.ReasonDeploy, CreatedAt: t0.Add(6 * time.Hour), Build: &bid,
		Error: &release.Failure{Code: "BUILD_FAILED", Message: "El build falló."}}
	f.releases.deployments = append([]release.Deployment{failed}, f.releases.deployments...)
	out, _ := call[releasemcp.GetDeploymentOut](t, f.connect(t, f.org, reader...), "get_deployment", map[string]any{"app": "cotizador", "deployment_id": failed.ID.String()})
	if out.Status != "failed" || !strings.Contains(out.NextStep, "get_build_logs") || out.Failure == nil || out.Failure.Code != "BUILD_FAILED" {
		t.Fatalf("build failed: %+v", out)
	}
}

// withBuild makes the newest deployment a build one and returns it.
func withBuild(f *fixture, status build.Status, owner project.ID) release.Deployment {
	bid := f.builds.add(status, owner)
	d := release.Deployment{ID: release.NewDeploymentID(), Organization: f.org, Project: f.app, Environment: f.env, Status: release.StatusBuilding,
		Step: release.StepBuild, Reason: release.ReasonDeploy, CreatedAt: t0.Add(7 * time.Hour), Build: &bid}
	f.releases.deployments = append([]release.Deployment{d}, f.releases.deployments...)
	f.releases.active = &d
	return d
}

func TestGetBuildLogsTail(t *testing.T) {
	f := newFixture(t)
	d := withBuild(f, build.StatusRunning, f.app)
	cs := f.connect(t, f.org, reader...)

	// Short log: everything, in order.
	f.builds.fill(3)
	out, res := call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador"})
	if res.IsError || out.Log != "line 1\nline 2\nline 3" || out.Next != 3 || !out.Finished || out.Truncated || out.BuildID != d.Build.String() || out.BuildStatus != "running" {
		t.Fatalf("short: %+v (%v)", out, res.Content)
	}

	// A log of 2500 lines spans three pages: the ring keeps the last 200 by default.
	f.builds.fill(2500)
	f.builds.asked = nil
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "deployment_id": d.ID.String()})
	lines := strings.Split(out.Log, "\n")
	if len(lines) != 200 || lines[0] != "line 2301" || lines[199] != "line 2500" || out.Next != 2500 || !out.Finished || out.Truncated {
		t.Fatalf("tail: %d lines, first %q last %q, %+v", len(lines), lines[0], lines[len(lines)-1], out)
	}
	if len(f.builds.asked) != 3 || f.builds.asked[0].After != 0 || f.builds.asked[1].After != 1000 || f.builds.asked[2].After != 2000 || f.builds.asked[0].Limit != 1000 {
		t.Fatalf("asked %+v", f.builds.asked)
	}

	// An explicit limit, capped at 1000.
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "limit": 5})
	if got := strings.Split(out.Log, "\n"); len(got) != 5 || got[0] != "line 2496" {
		t.Fatalf("limit 5: %q", out.Log)
	}
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "limit": 99999})
	if got := strings.Split(out.Log, "\n"); len(got) != 1000 || got[0] != "line 1501" {
		t.Fatalf("limit cap: %d first %q", len(got), got[0])
	}
}

func TestGetBuildLogsTruncatesVeryLongLogs(t *testing.T) {
	f := newFixture(t)
	withBuild(f, build.StatusRunning, f.app)
	f.builds.fill(21500) // 22 pages
	f.builds.asked = nil
	out, _ := call[releasemcp.GetBuildLogsOut](t, f.connect(t, f.org, reader...), "get_build_logs", map[string]any{"app": "cotizador", "limit": 3})
	if !out.Truncated || out.Finished || out.Next != 20000 || len(f.builds.asked) != 20 || out.Log != "line 19998\nline 19999\nline 20000" {
		t.Fatalf("truncated: %+v asked %d", out, len(f.builds.asked))
	}
}

func TestGetBuildLogsAfterIsOnePage(t *testing.T) {
	f := newFixture(t)
	withBuild(f, build.StatusRunning, f.app)
	f.builds.fill(30)
	f.builds.asked = nil
	cs := f.connect(t, f.org, reader...)
	out, res := call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "after": 10, "limit": 5})
	if res.IsError || out.Log != "line 11\nline 12\nline 13\nline 14\nline 15" || out.Next != 15 || out.Finished || len(f.builds.asked) != 1 {
		t.Fatalf("after: %+v (%v)", out, res.Content)
	}
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "after": 15, "limit": 100})
	if !out.Finished || out.Next != 30 || !strings.HasSuffix(out.Log, "line 30") {
		t.Fatalf("rest: %+v", out)
	}
	// after=0 is a page from the start, not the tail.
	f.builds.asked = nil
	out, _ = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "after": 0, "limit": 2})
	if out.Log != "line 1\nline 2" || len(f.builds.asked) != 1 {
		t.Fatalf("after 0: %+v", out)
	}
}

func TestGetBuildLogsShowsTheFailure(t *testing.T) {
	f := newFixture(t)
	d := withBuild(f, build.StatusFailed, f.app)
	b := f.builds.byID[*d.Build]
	b.Error = &build.Failure{Code: "BUILD_UNSUPPORTED", Message: "No sé cómo arrancar esto.", Detail: "no start command"}
	f.builds.byID[b.ID] = b
	f.builds.fill(2)
	out, _ := call[releasemcp.GetBuildLogsOut](t, f.connect(t, f.org, reader...), "get_build_logs", map[string]any{"app": "cotizador"})
	if out.BuildStatus != "failed" || out.Failure == nil || out.Failure.Code != "BUILD_UNSUPPORTED" || out.Failure.Detail != "no start command" {
		t.Fatalf("failure: %+v", out)
	}
}

func TestGetBuildLogsErrors(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)

	// An image deployment has no build.
	_, res := call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] == "" || !strings.Contains(body["next_step"], "get_deployment") {
		t.Fatalf("image: %v", body)
	}

	// A build of another app is NotFound, like the REST route.
	withBuild(f, build.StatusRunning, project.NewID())
	_, res = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" {
		t.Fatalf("foreign build: %v", body)
	}
	if len(f.builds.asked) != 0 {
		t.Fatal("read the log of another app's build")
	}

	_, res = call[releasemcp.GetBuildLogsOut](t, cs, "get_build_logs", map[string]any{"app": "cotizador", "limit": -1})
	if !res.IsError {
		t.Fatal("negative limit accepted")
	}
}

func TestRollbackDefaultsToThePreviousVersionThatWasOnline(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	v2, v1 := f.releases.deployments[1], f.releases.deployments[2]
	rel1, rel2 := release.NewID(), release.NewID()
	f.releases.deployments[1].Release, f.releases.deployments[2].Release = &rel2, &rel1
	f.releases.current = &f.releases.deployments[1]
	f.releases.release = &release.Release{ID: rel2}

	out, res := call[releasemcp.DeployOut](t, cs, "rollback", map[string]any{"app": "cotizador"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.rolledBack; len(got) != 1 || got[0] != v1.ID || f.commands.rollbackBy != user || f.commands.clients[0] != "mcp:https://claude.ai/cimd" {
		t.Fatalf("rolled back %v (want %v), by %v", got, v1.ID, f.commands.rollbackBy)
	}
	if out.Reason != "rollback" || out.Status != "pending" || !strings.Contains(out.NextStep, "get_deployment") {
		t.Fatalf("out: %+v", out)
	}
	_ = v2

	// An explicit id is passed as is.
	call[releasemcp.DeployOut](t, cs, "rollback", map[string]any{"app": "cotizador", "deployment_id": v2.ID.String()})
	if got := f.commands.rolledBack; len(got) != 2 || got[1] != v2.ID {
		t.Fatalf("explicit: %v", got)
	}
}

func TestRollbackWithoutAnEarlierVersion(t *testing.T) {
	f := newFixture(t)
	rel := release.NewID()
	for i := range f.releases.deployments {
		f.releases.deployments[i].Release = &rel // all the same release
	}
	f.releases.current = &f.releases.deployments[1]
	f.releases.release = &release.Release{ID: rel}
	_, res := call[releasemcp.DeployOut](t, f.connect(t, f.org, writer...), "rollback", map[string]any{"app": "cotizador"})
	if body := errorBody(t, res); body["code"] != "NOT_FOUND" || !strings.Contains(body["next_step"], "list_versions") {
		t.Fatalf("none: %v", body)
	}
	if len(f.commands.rolledBack) != 0 {
		t.Fatal("rolled back to nothing")
	}
}

func TestRollbackErrorsSuggestListVersions(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, writer...)
	for _, code := range []string{release.CodeAlreadyCurrent, release.CodeNotRollbackable} {
		f.commands.rollbackFn = func(release.DeploymentID) (release.Deployment, error) {
			return release.Deployment{}, errx.Business("refused").WithCode(code)
		}
		_, res := call[releasemcp.DeployOut](t, cs, "rollback", map[string]any{"app": "cotizador", "deployment_id": release.NewDeploymentID().String()})
		if body := errorBody(t, res); body["code"] != code || !strings.Contains(body["next_step"], "list_versions") {
			t.Fatalf("%s: %v", code, body)
		}
	}
	_, res := call[releasemcp.DeployOut](t, cs, "rollback", map[string]any{"app": "cotizador", "deployment_id": "x"})
	if body := errorBody(t, res); body["field"] != "deployment_id" {
		t.Fatalf("bad id: %v", body)
	}
}

func TestGetAppShowsTheConnectedRepository(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	if out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"}); out.Repository != nil {
		t.Fatalf("repository without a function: %+v", out.Repository)
	}
	f.options.Repository = func(_ context.Context, org identity.OrganizationID, app project.ID) (*releasemcp.ConnectedRepo, error) {
		if org != f.org || app != f.app {
			t.Errorf("asked for %v %v", org, app)
		}
		return &releasemcp.ConnectedRepo{Repository: "acme/cotizador", Branch: "main", Root: "web", LastCommit: "abc"}, nil
	}
	cs = f.connect(t, f.org, reader...)
	out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Repository == nil || *out.Repository != (releasemcp.ConnectedRepo{Repository: "acme/cotizador", Branch: "main", Root: "web", LastCommit: "abc"}) {
		t.Fatalf("repository: %+v", out.Repository)
	}

	// No connection (nil, or NotFound): the field is absent.
	f.options.Repository = func(context.Context, identity.OrganizationID, project.ID) (*releasemcp.ConnectedRepo, error) {
		return nil, errx.NotFound("no connection")
	}
	out, res := call[releasemcp.GetAppOut](t, f.connect(t, f.org, reader...), "get_app", map[string]any{"app": "cotizador"})
	if res.IsError || out.Repository != nil {
		t.Fatalf("not connected: %+v %v", out, res.Content)
	}
}
