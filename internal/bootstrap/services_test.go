package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
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

// TestServicesOverHTTP: MS-1 end to end. A new app is one web service; a
// list of several services is refused until every cluster of the app runs
// them (capability odicea.services), then stored and audited; a version of
// one image is refused while the app has several services (per-service
// builds are MS-3) without touching what is online; going back to one web
// service at "/" deploys again, and every version keeps its services.
func TestServicesOverHTTP(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := fleetca.Generate("test fleet CA", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := fleetca.Parse(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:  workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:   cluster,
		Authority: ca,
		Gateway:   "localhost:0",
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
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.AuditRead)
	reader := httpxtest.Token(org, permission.ProjectsRead)
	stranger := httpxtest.Token(other, permission.ProjectsRead, permission.ProjectsWrite)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	sbase := "/api/v1/projects/" + proj.ID.String() + "/services"
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, checked(t, app, http.MethodGet,
		"/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()

	type list struct {
		Items []project.Service `json:"items"`
	}
	services := func() []project.Service {
		t.Helper()
		res := checked(t, app, http.MethodGet, sbase, reader, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[list](t, res).Items
	}
	code := func(res httpxtest.Response, status int, want string) {
		t.Helper()
		httpxtest.Want(t, res, status)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(res.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != want {
			t.Fatalf("code %q, want %q: %s", body.Error.Code, want, res.Body)
		}
	}
	await := func(id string) release.Deployment {
		t.Helper()
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
	deploy := func(img string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": img, "port": 3000})
		httpxtest.Want(t, res, http.StatusAccepted)
		return await(httpxtest.JSON[release.Deployment](t, res).ID.String())
	}

	if s := services(); len(s) != 1 || s[0].Name != "web" || s[0].Route != "/" || s[0].Kind != project.ServiceWeb {
		t.Fatalf("a new app's services %+v", s)
	}
	monorepo := map[string]any{"items": []map[string]any{
		{"name": "web", "root": "apps/web", "route": "/"},
		{"name": "api", "root": "apps/api", "route": "/api/", "strip_prefix": true},
		{"name": "jobs", "kind": "worker", "root": "apps/jobs"},
	}}

	// Permissions, tenant, validation.
	httpxtest.Want(t, checked(t, app, http.MethodPut, sbase, reader, monorepo), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodGet, sbase, stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPut, sbase, stranger, monorepo), http.StatusNotFound)
	for _, body := range []map[string]any{
		{"items": []map[string]any{}},
		{"items": []map[string]any{{"name": "Web", "route": "/"}}},
		{"items": []map[string]any{{"name": "web", "route": "/_odicea"}}},
		{"items": []map[string]any{{"name": "web", "route": "/"}, {"name": "site", "route": "/"}}},
		{"items": []map[string]any{{"name": "jobs", "kind": "worker", "route": "/jobs"}}},
	} {
		httpxtest.Want(t, checked(t, app, http.MethodPut, sbase, token, body), http.StatusBadRequest)
	}

	// The first version runs the one web service.
	first := deploy(image("1"))
	if first.Status != release.StatusReady {
		t.Fatalf("first deployment %+v", first)
	}
	r1 := httpxtest.JSON[release.Release](t, checked(t, app, http.MethodGet,
		"/api/v1/projects/"+proj.ID.String()+"/releases/"+first.Release.String(), token, nil))
	if len(r1.Services) != 1 || r1.Services[0] != (release.Service{Name: "web", Kind: project.ServiceWeb, Route: "/",
		Image: image("1"), Port: 3000, HealthPath: "/", Replicas: 1}) {
		t.Fatalf("first version's services %+v", r1.Services)
	}
	if a := cluster.Applied(); len(a) == 0 || len(a[len(a)-1].App.Services) != 1 || a[len(a)-1].App.Services[0] != r1.Services[0] {
		t.Fatalf("the cluster was not told the version's services: %+v", a)
	}

	// No cluster at all, then an agent without the capability: refused.
	code(checked(t, app, http.MethodPut, sbase, token, monorepo), http.StatusUnprocessableEntity, project.CodeServicesUnavailable)
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	agent := fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial}
	online := func(apis ...string) {
		t.Helper()
		if err := platform.Fleet.Agents.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "test",
			Capabilities: fleet.Capabilities{APIs: apis}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	online(fleet.CapabilityAccess)
	code(checked(t, app, http.MethodPut, sbase, token, monorepo), http.StatusUnprocessableEntity, project.CodeServicesUnavailable)
	if s := services(); len(s) != 1 || s[0].Name != "web" {
		t.Fatalf("a refused change stored %+v", s)
	}
	// Renaming the one service needs nothing.
	res = checked(t, app, http.MethodPut, sbase, token, map[string]any{"items": []map[string]any{{"name": "site", "route": "/"}}})
	httpxtest.Want(t, res, http.StatusOK)

	// An agent that runs services: stored, in order, normalized.
	online(fleet.CapabilityAccess, fleet.CapabilityServices)
	res = checked(t, app, http.MethodPut, sbase, token, monorepo)
	httpxtest.Want(t, res, http.StatusOK)
	got := httpxtest.JSON[list](t, res).Items
	if len(got) != 3 || got[1].Name != "api" || got[1].Route != "/api" || !got[1].StripPrefix || got[2].Kind != project.ServiceWorker ||
		got[0].Root != "apps/web" {
		t.Fatalf("stored %+v", got)
	}
	if s := services(); !project.SameServices(s, got) {
		t.Fatalf("read back %+v", s)
	}

	// A version of one image cannot run several services yet: refused with
	// the code, and the first version stays online.
	code(checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("2"), "port": 3000}),
		http.StatusUnprocessableEntity, project.CodeServicesUnavailable)
	cur := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
	if cur.Release == nil || cur.Release.ID != *first.Release || cur.Active != nil {
		t.Fatalf("online after the refusal %+v", cur)
	}

	// Back to one service: deploys again.
	httpxtest.Want(t, checked(t, app, http.MethodPut, sbase, token,
		map[string]any{"items": []map[string]any{{"name": "web", "route": "/"}}}), http.StatusOK)
	if d := deploy(image("3")); d.Status != release.StatusReady {
		t.Fatalf("deployment after going back %+v", d)
	}

	// The audit trail names each change.
	events := 0
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && events < 3; time.Sleep(20 * time.Millisecond) {
		page, err := platform.Audit.Queries.List(ctx, org, audit.Filter{Types: []string{project.EventServicesChanged}}, query.Pagination{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		events = len(page.Items)
	}
	if events != 3 { // site, monorepo, web again
		t.Fatalf("audited %d services changes, want 3", events)
	}
}
