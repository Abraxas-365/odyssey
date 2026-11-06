package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

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
	"github.com/Abraxas-365/odyssey/internal/release/releasemodule"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestArchiveRetiresTheApp: archiving an app removes it from the cluster
// (asynchronously, from project.archived); archiving one that is publishing
// stops that deployment with reason archived and retires what it handed the
// cluster; the history stays; a redelivered event changes nothing; and an
// archived app is refused at the door.
func TestArchiveRetiresTheApp(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(d release.DesiredApp) releasefake.Outcome {
		if d.Image == image("5100") {
			return releasefake.Outcome{ReadyAfter: 1_000_000} // never ready
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

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)

	type place struct {
		proj   project.Project
		base   string
		target release.Target
	}
	create := func(name string) place {
		t.Helper()
		res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": name})
		httpxtest.Want(t, res, http.StatusCreated)
		p := httpxtest.JSON[project.Project](t, res)
		envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, p.ID, project.EnvironmentFilter{})
		if err != nil || len(envs) != 1 {
			t.Fatalf("environments %v %v", envs, err)
		}
		return place{proj: p, base: "/api/v1/projects/" + p.ID.String() + "/environments/" + envs[0].ID.String(),
			target: release.Target{Organization: org, Project: p.ID, Environment: envs[0].ID}}
	}
	deploy := func(at place, img string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, at.base+"/deployments", token, map[string]any{"image": img, "port": 8080, "health_path": "/health"})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.Deployment](t, res)
	}
	await := func(at place, id release.DeploymentID, want release.Status) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, at.base+"/deployments/"+id.String(), token, nil)
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
	archive := func(at place) {
		t.Helper()
		res := checked(t, app, http.MethodPost, "/api/v1/projects/"+at.proj.ID.String()+"/archive", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
	}
	retired := func(at place) int64 {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if r := cluster.Retired(at.target, at.proj.Slug); r > 0 {
				return r
			}
		}
		t.Fatalf("%s was never retired", at.proj.Slug)
		return 0
	}

	// 1. a live app leaves the cluster; its history stays
	visitas := create("Visitas")
	v1 := await(visitas, deploy(visitas, image("1")).ID, release.StatusReady)
	if cluster.Running(visitas.target, "visitas") != image("1") {
		t.Fatal("not running before the archive")
	}
	archive(visitas)
	revision := retired(visitas)
	if cluster.Running(visitas.target, "visitas") != "" || revision <= v1.Revision {
		t.Fatalf("still running, or retired at %d (deployed at %d)", revision, v1.Revision)
	}
	res := checked(t, app, http.MethodGet, visitas.base+"/deployments", token, nil)
	if page := httpxtest.JSON[query.Paginated[release.Deployment]](t, res); page.Page.Total != 1 || page.Items[0].ID != v1.ID {
		t.Fatalf("history %+v", page)
	}

	// 2. an app being published: the deployment stops (reason archived) and
	// what it handed the cluster is retired
	pagos := create("Pagos")
	slow := deploy(pagos, image("5100"))
	await(pagos, slow.ID, release.StatusVerifying)
	archive(pagos)
	retired(pagos)
	if d := await(pagos, slow.ID, release.StatusCancelled); d.Error != nil {
		t.Fatalf("cancelled %+v", d)
	}
	if cluster.Running(pagos.target, "pagos") != "" {
		t.Fatal("the deployment in progress kept running")
	}
	var reasons []string
	if err := db.SelectContext(ctx, &reasons, `SELECT payload->>'reason' FROM event.outbox WHERE organization_id = $1 AND type = $2`,
		org, release.EventDeploymentCancelled); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || reasons[0] != release.CancelArchived {
		t.Fatalf("cancel reasons %v", reasons)
	}

	// 3. a redelivered project.archived changes nothing
	applied := len(cluster.Applied())
	var redelivered int64
	if err := db.GetContext(ctx, &redelivered, `UPDATE event.deliveries d SET status = 'pending', next_at = now(), done_at = NULL
		  FROM event.outbox o WHERE o.id = d.event_id AND o.organization_id = $1 AND o.type = $2 AND d.subscriber = $3
		RETURNING d.seq`, org, project.EventArchived, releasemodule.RetireOnArchived); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var status string
		if err := db.GetContext(ctx, &status, `SELECT status FROM event.deliveries WHERE seq = $1 AND subscriber = $2`,
			redelivered, releasemodule.RetireOnArchived); err != nil {
			t.Fatal(err)
		}
		if status == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redelivery is %s", status)
		}
	}
	if cluster.Retired(visitas.target, "visitas") != revision || len(cluster.Applied()) != applied {
		t.Fatal("a redelivery retired again or applied something")
	}
	var states []struct {
		Retired *time.Time `db:"retired_at"`
		Active  *string    `db:"active_deployment_id"`
	}
	if err := db.SelectContext(ctx, &states, `SELECT retired_at, active_deployment_id FROM release.environment_states WHERE organization_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states %+v", states)
	}
	for _, s := range states {
		if s.Retired == nil || s.Active != nil {
			t.Fatalf("state %+v", s)
		}
	}

	// 4. archived: a deployment is refused at the door
	res = checked(t, app, http.MethodPost, visitas.base+"/deployments", token, map[string]any{"image": image("2"), "port": 8080})
	if httpxtest.Want(t, res, http.StatusUnprocessableEntity); httpxtest.ErrorCode(res) != release.CodeArchived {
		t.Fatalf("deploy to an archived app: %s", httpxtest.ErrorCode(res))
	}
}
