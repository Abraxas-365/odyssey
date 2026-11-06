package bootstrap_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestWhoCanOpenTheApp: "Quién puede abrirla" end to end. The settings are
// read at apply time (a deploy made before the change still gets the mode in
// force when it runs), every change is a new revision, a change applies to
// the release online as a `config` deployment, public needs its own
// permission, and the change reaches the audit trail.
func TestWhoCanOpenTheApp(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond},
		Cluster:  cluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	// What this test proves is the permission for public, not IT's rules.
	switchOff(t, platform, org, policy.BuiltinPublicAccess)
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.AuditRead)
	admin := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.AccessPublic)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()

	type settings struct {
		Revision int64 `json:"revision"`
		Access   struct {
			Mode string `json:"mode"`
		} `json:"access"`
		Available bool `json:"access_login_available"`
	}
	read := func(tok string) settings {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/settings", tok, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[settings](t, res)
	}
	await := func(id release.DeploymentID, want release.Status) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status == want {
				return d
			}
		}
		t.Fatalf("deployment %s is %s (want %s)", id, d.Status, want)
		return d
	}
	lastAccess := func() string {
		a := cluster.Applied()
		if len(a) == 0 {
			t.Fatal("nothing applied")
		}
		return a[len(a)-1].App.Access
	}
	deploy := func() release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080})
		httpxtest.Want(t, res, http.StatusAccepted)
		return await(httpxtest.JSON[release.Deployment](t, res).ID, release.StatusReady)
	}

	// Nobody changed anything: revision 0, the safe default. This test
	// server has no access sign-in configured, and says so.
	if s := read(token); s.Revision != 0 || s.Access.Mode != "team" || s.Available {
		t.Fatalf("defaults %+v", s)
	}
	// Nothing online: the mode is only stored.
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"})
	httpxtest.Want(t, res, http.StatusAccepted)
	change := httpxtest.JSON[release.AccessChange](t, res)
	if change.Deployment != nil || change.Settings.Revision != 1 || change.Settings.Access.Mode != release.AccessOrganization || res.Header.Get("Location") != "" {
		t.Fatalf("stored %+v", change)
	}
	// The first deployment resolves the settings when it applies.
	v1 := deploy()
	if got := lastAccess(); got != "organization" {
		t.Fatalf("first deployment applied access %q", got)
	}
	if v1.SettingsRevision == nil || *v1.SettingsRevision != 1 {
		t.Fatalf("deployment settings revision %v", v1.SettingsRevision)
	}

	// The same mode again changes nothing: no revision, no deployment.
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"})
	httpxtest.Want(t, res, http.StatusAccepted)
	if again := httpxtest.JSON[release.AccessChange](t, res); again.Deployment != nil || again.Settings.Revision != 1 {
		t.Fatalf("no-op %+v", again)
	}

	// A change with a release online applies at once, as a config deployment.
	before := len(cluster.Applied())
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "team"})
	httpxtest.Want(t, res, http.StatusAccepted)
	change = httpxtest.JSON[release.AccessChange](t, res)
	if change.Deployment == nil || change.Deployment.Reason != release.ReasonConfig || change.Settings.Revision != 2 ||
		res.Header.Get("Location") != base+"/deployments/"+change.Deployment.ID.String() {
		t.Fatalf("config change %+v at %q", change, res.Header.Get("Location"))
	}
	cfg := await(change.Deployment.ID, release.StatusReady)
	if cfg.Release == nil || *cfg.Release != *v1.Release || len(cluster.Applied()) != before+1 || lastAccess() != "team" {
		t.Fatalf("config deployment %+v applied %d access %q", cfg, len(cluster.Applied()), lastAccess())
	}
	if s := read(token); s.Revision != 2 || s.Access.Mode != "team" {
		t.Fatalf("settings after %+v", s)
	}

	// Public is a decision of its own: the permission to write the app is not enough.
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "public"}), http.StatusForbidden)
	if s := read(token); s.Revision != 2 {
		t.Fatalf("a refused change stored a revision: %+v", s)
	}
	res = checked(t, app, http.MethodPut, base+"/access", admin, map[string]any{"mode": "public"})
	httpxtest.Want(t, res, http.StatusAccepted)
	pub := httpxtest.JSON[release.AccessChange](t, res)
	if pub.Deployment == nil {
		t.Fatalf("public change %+v", pub)
	}
	await(pub.Deployment.ID, release.StatusReady)
	if lastAccess() != "public" {
		t.Fatalf("applied %q", lastAccess())
	}
	// closing an app again needs no extra permission
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "team"})
	httpxtest.Want(t, res, http.StatusAccepted)
	await(httpxtest.JSON[release.AccessChange](t, res).Deployment.ID, release.StatusReady)

	// A new version keeps the settings in force.
	deploy()
	if lastAccess() != "team" {
		t.Fatalf("a new version applied access %q", lastAccess())
	}

	// Refusals
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "everyone"}), http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{}), http.StatusBadRequest)
	reader := httpxtest.Token(org, permission.ProjectsRead)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", reader, map[string]any{"mode": "team"}), http.StatusForbidden)
	if s := read(reader); s.Access.Mode != "team" {
		t.Fatalf("a reader sees %+v", s)
	}
	stranger := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead, permission.ProjectsWrite, permission.AccessPublic)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/settings", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", stranger, map[string]any{"mode": "public"}), http.StatusNotFound)
	if s := read(token); s.Access.Mode != "team" {
		t.Fatalf("another organization changed the mode: %+v", s)
	}

	// Every change is in the trail (access.changed: org→team→public→team, plus
	// the first organization one).
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		res := checked(t, app, http.MethodGet, "/api/v1/audit?type="+release.EventAccessChanged, token, nil)
		trail := httpxtest.JSON[query.Paginated[audit.Entry]](t, res)
		if trail.Page.Total == 4 {
			if trail.Items[0].Summary == "" {
				t.Fatalf("trail %+v", trail.Items[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("access.changed entries: %d, want 4", trail.Page.Total)
		}
	}
}

// TestAProtectedModeRefusedAtApplyRestoresNothing: a cluster that cannot
// protect apps refuses a team deployment at apply (ACCESS_UNAVAILABLE). That
// deployment never reached the cluster, which still serves what it served, so
// the platform does not "restore" anything (an auto-rollback would be refused
// the same way) and nothing was recorded as applied.
func TestAProtectedModeRefusedAtApplyRestoresNothing(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{NoAccessGate: true,
		Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond}, Cluster: cluster})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	switchOff(t, platform, org, policy.BuiltinPublicAccess) // this test is about the cluster, not IT's rules
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate, permission.AccessPublic)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	await := func(id release.DeploymentID, want release.Status) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status == want {
				return d
			}
		}
		t.Fatalf("deployment %s is %s (want %s)", id, d.Status, want)
		return d
	}
	put := func(mode string) release.AccessChange {
		res := checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": mode})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.AccessChange](t, res)
	}
	deploy := func(img string) release.Deployment {
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image(img), "port": 8080})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.Deployment](t, res)
	}
	settings := func() struct {
		Available bool `json:"access_protection_available"`
	} {
		res := checked(t, app, http.MethodGet, base+"/settings", token, nil)
		return httpxtest.JSON[struct {
			Available bool `json:"access_protection_available"`
		}](t, res)
	}
	if settings().Available {
		t.Fatal("a cluster without the gate reports it can protect apps")
	}

	// Public is all this cluster can serve.
	put("public")
	v1 := await(deploy("1").ID, release.StatusReady)
	applied := func() *int64 {
		return httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil)).AppliedSettingsRevision
	}
	if a := applied(); a == nil || *a != 1 {
		t.Fatalf("applied revision after the first version: %v", a)
	}

	// Asking for team: the config deployment is refused at apply.
	cfg := put("team").Deployment
	if cfg == nil {
		t.Fatal("no config deployment")
	}
	failed := await(cfg.ID, release.StatusFailed)
	if failed.Step != release.StepApply || failed.Error == nil || failed.Error.Code != release.CodeApplyFailed ||
		failed.Error.Message != fleet.AccessUnavailableMessage {
		t.Fatalf("config refused at apply: %+v", failed)
	}
	// ...and so is a new version, which would apply the team settings.
	failed = await(deploy("2").ID, release.StatusFailed)
	if failed.Step != release.StepApply || failed.Reason != release.ReasonDeploy {
		t.Fatalf("version refused at apply: %+v", failed)
	}

	time.Sleep(300 * time.Millisecond) // anything the failures would start has started
	res = checked(t, app, http.MethodGet, base+"/deployments", token, nil)
	for _, d := range httpxtest.JSON[query.Paginated[release.Deployment]](t, res).Items {
		if d.Reason == release.ReasonAutoRollback {
			t.Fatalf("a deployment refused at apply was followed by an automatic rollback: %+v", d)
		}
		if d.Status != release.StatusFailed && d.ID != v1.ID {
			t.Fatalf("something is still running: %+v", d)
		}
	}
	for _, a := range cluster.Applied() {
		if a.App.Access != "public" {
			t.Fatalf("the cluster was told %q", a.App.Access)
		}
	}
	c := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
	if c.Release == nil || c.Release.ID != *v1.Release || c.Active != nil || c.AppliedSettingsRevision == nil || *c.AppliedSettingsRevision != 1 {
		t.Fatalf("current %+v", c)
	}
	// The page says who can open it now: still anyone with the link; team is
	// saved and has not reached the app.
	if c.Access == nil || c.Access.Mode != release.AccessPublic || c.Access.Pending == nil || *c.Access.Pending != release.AccessTeam {
		t.Fatalf("access in force %+v", c.Access)
	}
}

// TestAConfigDeploymentThatFailsAfterApplyDoesNotReopenTheApp: once the
// cluster was told a mode, a config deployment that then fails (the app never
// gets healthy) does not put the previous mode back by itself, and does not
// start another deployment: the cluster keeps what it was told, the
// environment records it as applied, and the failed deployment is in the
// history with its error.
func TestAConfigDeploymentThatFailsAfterApplyDoesNotReopenTheApp(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome {
		if broken.Load() {
			return releasefake.Outcome{Unhealthy: true}
		}
		return releasefake.Outcome{}
	}})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }}, Cluster: cluster})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	await := func(id release.DeploymentID, want release.Status) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			d = httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil))
			if d.Status == want {
				return d
			}
		}
		t.Fatalf("deployment %s is %s (want %s)", id, d.Status, want)
		return d
	}
	current := func() release.Current {
		return httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
	}
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080})
	v1 := await(httpxtest.JSON[release.Deployment](t, res).ID, release.StatusReady)
	if a := current().AppliedSettingsRevision; a == nil || *a != 0 {
		t.Fatalf("applied after the first version: %v", a)
	}
	if a := current().Access; a == nil || a.Mode != release.AccessTeam || a.Pending != nil {
		t.Fatalf("access in force after the first version (the defaults): %+v", a)
	}

	broken.Store(true)
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"})
	httpxtest.Want(t, res, http.StatusAccepted)
	cfg := httpxtest.JSON[release.AccessChange](t, res)
	failed := await(cfg.Deployment.ID, release.StatusFailed)
	if failed.Reason != release.ReasonConfig || failed.Step != release.StepWaitReady || failed.Error == nil || failed.Error.Code != release.CodeUnhealthy {
		t.Fatalf("failed config deployment %+v", failed)
	}
	time.Sleep(300 * time.Millisecond) // anything it would start has started
	c := current()
	if c.AppliedSettingsRevision == nil || *c.AppliedSettingsRevision != 1 || c.Active != nil || c.Release == nil || c.Release.ID != *v1.Release {
		t.Fatalf("after the failed config deployment: %+v", c)
	}
	if c.Access == nil || c.Access.Mode != release.AccessOrganization || c.Access.Pending != nil {
		t.Fatalf("the cluster was told organization; that is in force: %+v", c.Access)
	}
	res = checked(t, app, http.MethodGet, base+"/deployments", token, nil)
	history := httpxtest.JSON[query.Paginated[release.Deployment]](t, res)
	if history.Page.Total != 2 || history.Items[0].ID != cfg.Deployment.ID || history.Items[0].Error == nil {
		t.Fatalf("history: no rollback, no retry; the failure is listed: %+v", history)
	}
	applied := cluster.Applied()
	if got := applied[len(applied)-1].App.Access; got != "organization" || len(applied) != 2 {
		t.Fatalf("the cluster was told %q (%d applies): the app was reopened", got, len(applied))
	}
	// the sign-in follows what the cluster was told
	t.Logf("applied settings revision %d, latest %d", *c.AppliedSettingsRevision, cfg.Settings.Revision)

	// The cluster already has the mode, so asking again changes nothing; the
	// way out of an unhealthy app is a new version.
	broken.Store(false)
	res = checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"})
	httpxtest.Want(t, res, http.StatusAccepted)
	if again := httpxtest.JSON[release.AccessChange](t, res); again.Deployment != nil || again.Settings.Revision != 1 {
		t.Fatalf("asking for the mode the cluster has: %+v", again)
	}
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("2"), "port": 8080})
	await(httpxtest.JSON[release.Deployment](t, res).ID, release.StatusReady)
	applied = cluster.Applied()
	if got := applied[len(applied)-1].App.Access; got != "organization" {
		t.Fatalf("the new version applied %q", got)
	}
}
