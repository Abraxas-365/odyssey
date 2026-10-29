package bootstrap_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// image is a digest-pinned image whose digest ends in v (hex).
func image(v string) string {
	return "registry.local/visitas@sha256:" + strings.Repeat("0", 64-len(v)) + v
}

// rel is a deployment's release (zero while it builds).
func rel(d release.Deployment) release.ID {
	if d.Release == nil {
		return release.ID{}
	}
	return *d.Release
}

// TestDeployWorkflow is phase 1a-a1's exit criterion: publishing an image
// goes live through apply → wait_ready → promote; a version that fails its
// health check never becomes current and the platform restores the one
// online; "Volver a esta" deploys an earlier release; a newer deployment
// cancels the one in progress; and all of it reaches the audit trail.
func TestDeployWorkflow(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool // every app fails its health check
	cluster := releasefake.New(releasefake.Options{Behavior: func(d release.DesiredApp) releasefake.Outcome {
		if broken.Load() {
			return releasefake.Outcome{Unhealthy: true}
		}
		switch d.Image {
		case image("dead"):
			return releasefake.Outcome{Reject: true}
		case image("bad"):
			return releasefake.Outcome{Unhealthy: true}
		case image("5100"):
			return releasefake.Outcome{ReadyAfter: 1_000_000}
		}
		return releasefake.Outcome{}
	}})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
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

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth())
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.DeploymentsRollback, permission.AuditRead)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Visitas"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	envs := httpxtest.JSON[query.Paginated[project.Environment]](t, res)
	if len(envs.Items) != 1 || envs.Items[0].Slug != project.ProductionSlug {
		t.Fatalf("environments %+v", envs)
	}
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + envs.Items[0].ID.String()
	target := release.Target{Organization: org, Project: proj.ID, Environment: envs.Items[0].ID}

	current := func() release.Current {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/current", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[release.Current](t, res)
	}
	if c := current(); c.Release != nil || c.Active != nil || c.URL != "" {
		t.Fatalf("before any deployment %+v", c)
	}
	deploy := func(img string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token,
			map[string]any{"image": img, "port": 8080, "health_path": "/health", "env": map[string]string{"TZ": "America/Lima"}})
		httpxtest.Want(t, res, http.StatusAccepted)
		d := httpxtest.JSON[release.Deployment](t, res)
		if res.Header.Get("Location") != base+"/deployments/"+d.ID.String() || d.Status != release.StatusPending || len(d.Steps) != 3 {
			t.Fatalf("accepted %+v at %q", d, res.Header.Get("Location"))
		}
		return d
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
			if d.Status.Finished() {
				break
			}
		}
		t.Fatalf("deployment %s is %s at %s (want %s): %+v", id, d.Status, d.Step, want, d.Error)
		return d
	}
	settled := func() release.Current {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if c := current(); c.Active == nil {
				return c
			}
		}
		t.Fatal("the environment never settled")
		return release.Current{}
	}

	// 1. the first version goes live
	v1 := await(deploy(image("1")).ID, release.StatusReady)
	for _, s := range v1.Steps {
		if s.Status != release.StepDone {
			t.Fatalf("steps %+v", v1.Steps)
		}
	}
	c := settled()
	if c.Release == nil || c.Release.Image != image("1") || c.Deployment.ID != v1.ID || c.URL != "https://visitas.apps.odyssey.localhost" ||
		c.Release.Config.Env["TZ"] != "America/Lima" || cluster.Running(target, "visitas") != image("1") {
		t.Fatalf("current after v1 %+v", c)
	}

	// 2. a version that fails its health check never becomes current, and the
	// platform puts v1 back
	bad := await(deploy(image("bad")).ID, release.StatusFailed)
	// plain message and the cluster's words travel apart
	if bad.Error == nil || bad.Error.Code != release.CodeUnhealthy || bad.Step != release.StepWaitReady ||
		strings.Contains(bad.Error.Message, "503") || !strings.Contains(bad.Error.Detail, "503") {
		t.Fatalf("failed deployment %+v", bad)
	}
	c = settled()
	if c.Release.ID != rel(v1) || c.Deployment.Reason != release.ReasonAutoRollback || c.Deployment.RollbackOf == nil ||
		*c.Deployment.RollbackOf != bad.ID || cluster.Running(target, "visitas") != image("1") {
		t.Fatalf("current after auto-rollback %+v", c)
	}

	// 3. "Volver a esta": publish v2, then go back to v1
	v2 := await(deploy(image("2")).ID, release.StatusReady)
	res = checked(t, app, http.MethodPost, base+"/deployments/"+v1.ID.String()+"/rollback", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	back := httpxtest.JSON[release.Deployment](t, res)
	if back.Reason != release.ReasonRollback || rel(back) != rel(v1) || res.Header.Get("Location") != base+"/deployments/"+back.ID.String() {
		t.Fatalf("rollback %+v at %q", back, res.Header.Get("Location"))
	}
	await(back.ID, release.StatusReady)
	if c = settled(); c.Release.ID != rel(v1) || cluster.Running(target, "visitas") != image("1") {
		t.Fatalf("current after rollback %+v", c)
	}
	res = checked(t, app, http.MethodPost, base+"/deployments/"+back.ID.String()+"/rollback", token, nil)
	if httpxtest.Want(t, res, http.StatusUnprocessableEntity); httpxtest.ErrorCode(res) != release.CodeAlreadyCurrent {
		t.Fatalf("rollback to current: %s", httpxtest.ErrorCode(res))
	}
	res = checked(t, app, http.MethodPost, base+"/deployments/"+bad.ID.String()+"/rollback", token, nil)
	if httpxtest.Want(t, res, http.StatusUnprocessableEntity); httpxtest.ErrorCode(res) != release.CodeNotRollbackable {
		t.Fatalf("rollback to a failed one: %s", httpxtest.ErrorCode(res))
	}

	// 4. a newer deployment cancels the one in progress
	slow := deploy(image("5100"))
	await(slow.ID, release.StatusVerifying)
	v3 := await(deploy(image("3")).ID, release.StatusReady)
	if d := await(slow.ID, release.StatusCancelled); d.Error != nil {
		t.Fatalf("cancelled %+v", d)
	}
	if c = settled(); c.Release.ID != rel(v3) || cluster.Running(target, "visitas") != image("3") {
		t.Fatalf("current after supersede %+v", c)
	}

	// history, newest first, filterable
	res = checked(t, app, http.MethodGet, base+"/deployments?status=ready", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if page := httpxtest.JSON[query.Paginated[release.Deployment]](t, res); page.Page.Total != 5 || page.Items[0].ID != v3.ID || page.Items[4].ID != v1.ID {
		t.Fatalf("ready history %+v", page)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/releases/"+rel(v2).String(), token, nil)
	if httpxtest.Want(t, res, http.StatusOK); httpxtest.JSON[release.Release](t, res).Image != image("2") {
		t.Fatal("release v2")
	}

	// refusals
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": "ghcr.io/inedge/visitas:latest", "port": 8080})
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorField(res) != "image" {
		t.Fatalf("tag image: %s", httpxtest.ErrorField(res))
	}
	reader := httpxtest.Token(org, permission.ProjectsRead, permission.DeploymentsRead)
	httpxtest.Want(t, checked(t, app, http.MethodPost, base+"/deployments", reader, map[string]any{"image": image("4"), "port": 1}), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, base+"/deployments/"+v1.ID.String()+"/rollback", reader, nil), http.StatusForbidden)
	stranger := httpxtest.Token(identity.NewOrganizationID(), permission.DeploymentsRead, permission.DeploymentsCreate)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/current", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/deployments/"+v1.ID.String(), stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, base+"/deployments", stranger, map[string]any{"image": image("4"), "port": 1}), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/projects/"+proj.ID.String()+"/archive", token, nil), http.StatusOK)
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("4"), "port": 1})
	if httpxtest.Want(t, res, http.StatusUnprocessableEntity); httpxtest.ErrorCode(res) != release.CodeArchived {
		t.Fatalf("archived: %s", httpxtest.ErrorCode(res))
	}

	// every step of it is in the audit trail, attributed to the deployment
	count := func(typ string) int {
		res := checked(t, app, http.MethodGet, "/api/v1/audit?type="+typ, token, nil)
		return httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Page.Total
	}
	want := map[string]int{release.EventCreated: 5, release.EventDeploymentStarted: 7, release.EventDeploymentReady: 5,
		release.EventDeploymentFailed: 1, release.EventDeploymentCancelled: 1}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		missing := ""
		for typ, n := range want {
			if got := count(typ); got != n {
				missing += typ + " "
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trail: wrong counts for %s", missing)
		}
	}
	res = checked(t, app, http.MethodGet, "/api/v1/audit?subject=deployment/"+bad.ID.String(), token, nil)
	trail := httpxtest.JSON[query.Paginated[audit.Entry]](t, res)
	if trail.Page.Total != 2 || trail.Items[0].Type != release.EventDeploymentFailed {
		t.Fatalf("bad deployment's trail %+v", trail)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/audit/verify", token, nil)
	if v := httpxtest.JSON[audit.Verification](t, res); !v.Valid {
		t.Fatalf("verify %+v", v)
	}

	// 5. in a new app: a first version the cluster refuses has nothing to
	// restore; and when the restore fails too, the platform stops there
	res = checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Pagos"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj = httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	base = "/api/v1/projects/" + proj.ID.String() + "/environments/" + httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0].ID.String()
	dead := await(deploy(image("dead")).ID, release.StatusFailed)
	if dead.Error == nil || dead.Error.Code != release.CodeApplyFailed || dead.Step != release.StepApply || dead.Error.Detail == "" {
		t.Fatalf("refused deployment %+v", dead)
	}
	if c = settled(); c.Release != nil || c.Deployment != nil {
		t.Fatalf("current after a failed first version %+v", c)
	}
	p1 := await(deploy(image("a1")).ID, release.StatusReady)
	broken.Store(true)
	await(deploy(image("a2")).ID, release.StatusFailed)
	c = settled()
	res = checked(t, app, http.MethodGet, base+"/deployments", token, nil)
	history := httpxtest.JSON[query.Paginated[release.Deployment]](t, res)
	if c.Release.ID != rel(p1) || history.Page.Total != 4 || history.Items[0].Reason != release.ReasonAutoRollback ||
		history.Items[0].Status != release.StatusFailed {
		t.Fatalf("after a failed restore: current %+v, history %+v", c, history)
	}
}
