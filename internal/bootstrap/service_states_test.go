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
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestServiceStatesOverHTTP: the per-service read of what an environment
// serves (…/services), the deployments' `changed` and the inventory's
// `services`, end to end against the fake cluster (which reports per service).
func TestServiceStatesOverHTTP(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
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
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate,
		permission.InventoryRead)
	noRead := httpxtest.Token(org, permission.ProjectsRead, permission.DeploymentsCreate)
	stranger := httpxtest.Token(other, permission.ProjectsRead, permission.DeploymentsRead)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, checked(t, app, http.MethodGet,
		"/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()

	type states struct {
		Items []release.ServiceState `json:"items"`
	}
	read := func() []release.ServiceState {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/services", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[states](t, res).Items
	}

	// Permission and tenant.
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/services", noRead, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/services", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/services", "", nil), http.StatusUnauthorized)

	// Nothing serving: an empty list, not null.
	res = checked(t, app, http.MethodGet, base+"/services", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[states](t, res); got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("nothing serving: %s", res.Body)
	}

	deploy := func(img string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": img, "port": 3000, "replicas": 2})
		httpxtest.Want(t, res, http.StatusAccepted)
		id := httpxtest.JSON[release.Deployment](t, res).ID.String()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			d = httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+id, token, nil))
			if d.Status.Finished() {
				return d
			}
		}
		t.Fatalf("deployment %s is %s", id, d.Status)
		return d
	}
	first := deploy(image("1"))
	if first.Status != release.StatusReady || first.Changed == nil || len(first.Changed) != 1 || first.Changed[0] != "web" {
		t.Fatalf("first deployment %+v (changed %#v): every service is new", first, first.Changed)
	}
	got := read()
	want := release.ServiceState{Name: "web", Kind: project.ServiceWeb, Route: "/", Phase: release.PhaseReady, ReadyReplicas: 2, Replicas: 2}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("services %+v, want [%+v]", got, want)
	}

	second := deploy(image("2"))
	if second.Status != release.StatusReady || len(second.Changed) != 1 || second.Changed[0] != "web" {
		t.Fatalf("second deployment changed %#v", second.Changed)
	}
	list := httpxtest.JSON[query.Paginated[release.Deployment]](t, checked(t, app, http.MethodGet, base+"/deployments", token, nil))
	if len(list.Items) != 2 || list.Items[0].Changed == nil || list.Items[1].Changed == nil {
		t.Fatalf("list %+v", list.Items)
	}
	cur := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
	if cur.Deployment == nil || len(cur.Deployment.Changed) != 1 {
		t.Fatalf("current deployment %+v", cur.Deployment)
	}

	// The inventory lists the same services.
	inv := checked(t, app, http.MethodGet, "/api/v1/inventory", token, nil)
	httpxtest.Want(t, inv, http.StatusOK)
	rows := httpxtest.JSON[struct {
		Items []struct {
			Services []release.ServiceRef `json:"services"`
		} `json:"items"`
	}](t, inv).Items
	if len(rows) != 1 || len(rows[0].Services) != 1 || rows[0].Services[0] != (release.ServiceRef{Name: "web", Kind: project.ServiceWeb, Route: "/"}) {
		t.Fatalf("inventory services %+v", rows)
	}
}
