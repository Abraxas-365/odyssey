package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/catalog"
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
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// groups is a fake IAMKit directory of teams: Datos and Ventas.
type groups map[identity.UserID][]string

func (g groups) Teams(context.Context, identity.OrganizationID) ([]catalog.Team, error) {
	return []catalog.Team{{ID: "grp_datos", Name: "Datos", Members: 1}, {ID: "grp_ventas", Name: "Ventas", Members: 1, Directory: true}}, nil
}

func (g groups) TeamsOf(_ context.Context, _ identity.OrganizationID, u identity.UserID) ([]string, error) {
	return g[u], nil
}

// TestOfferingsForTeamsOverHTTP: IT limits a recipe to a team with the real
// wiring. Its members create it, others are told it is not theirs; an
// approved request runs with the teams of who asked, not of who approved;
// what exists is changed by anyone who may change it.
func TestOfferingsForTeamsOverHTTP(t *testing.T) {
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
	const anaID, brunoID, itID = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	dir := groups{identity.MustParseUserID(anaID): {"grp_datos"}, identity.MustParseUserID(brunoID): {"grp_ventas"}}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond},
		Cluster: cluster, Authority: ca, Gateway: "localhost:0", Teams: dir})
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
	builder := []string{permission.ProjectsRead, permission.ProjectsWrite}
	ana, bruno := httpxtest.TokenAs(anaID, org, builder...), httpxtest.TokenAs(brunoID, org, builder...)
	it := httpxtest.TokenAs(itID, org, permission.ProjectsRead, permission.ApprovalsDecide, permission.CatalogManage)

	// IT sees the teams it can choose; a builder does not
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/catalog/teams", ana, nil), http.StatusForbidden)
	res := checked(t, app, http.MethodGet, "/api/v1/catalog/teams", it, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[catalog.Teams](t, res); !got.Available || len(got.Items) != 2 || !got.Items[1].Directory {
		t.Fatalf("teams %+v", got)
	}

	// caches only for Datos, pre-approved up to 5 GiB
	offer := func(body map[string]any) httpxtest.Response {
		return checked(t, app, http.MethodPut, "/api/v1/catalog/recipes/"+catalog.RedisBasic+"/offering", it, body)
	}
	body := map[string]any{"version": 1, "active": true, "tiers": []string{"internal"}, "defaults": []string{"internal"},
		"preapproved": map[string]any{"storage_gi": map[string]int{"max": 5}}, "teams": []string{"grp_finanzas"}}
	httpxtest.Want(t, offer(body), http.StatusBadRequest)
	body["teams"] = []string{"grp_datos"}
	res = offer(body)
	httpxtest.Want(t, res, http.StatusOK)
	if e := httpxtest.JSON[catalog.Entry](t, res); len(e.Offering.Teams) != 1 || e.Offering.Teams[0] != "grp_datos" {
		t.Fatalf("offering %+v", e.Offering)
	}

	// an app (internal tier) on a cluster that prepares caches
	res = checked(t, app, http.MethodPost, "/api/v1/projects", ana, map[string]any{"name": "Inventario"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t,
		checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", ana, nil)).Items[0]
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: identity.MustParseUserID(itID)})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.Fleet.Agents.Connect(ctx, fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial},
		fleet.NewSession(), fleet.Agent{Version: "test", Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres", "redis", "files"}}},
		"10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	place := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()

	// each one's catalog: Datos has caches, Ventas is told why not
	res = checked(t, app, http.MethodGet, place+"/catalog", bruno, nil)
	httpxtest.Want(t, res, http.StatusOK)
	o := httpxtest.JSON[resource.Offered](t, res)
	for _, r := range o.Recipes {
		if r.Recipe == catalog.RedisBasic {
			t.Fatalf("Ventas offered a cache: %+v", o)
		}
	}
	res = checked(t, app, http.MethodGet, place+"/catalog", ana, nil)
	if o := httpxtest.JSON[resource.Offered](t, res); !offers(o, catalog.RedisBasic) {
		t.Fatalf("Datos not offered a cache: %+v", o)
	}

	// creating: Datos at once, Ventas refused in words
	httpxtest.Want(t, checked(t, app, http.MethodPost, place+"/resources", ana, map[string]any{"name": "cache", "kind": "redis", "storage_gi": 1}),
		http.StatusCreated)
	refused(t, checked(t, app, http.MethodPost, place+"/resources", bruno, map[string]any{"name": "otra", "kind": "redis", "storage_gi": 1}),
		http.StatusUnprocessableEntity, catalog.CodeUnavailable)

	// outside the pre-approved range: held; IT (in no team) approves and it
	// runs with the teams of Ana, who asked
	res = checked(t, app, http.MethodPost, place+"/resources", ana, map[string]any{"name": "grande", "kind": "redis", "storage_gi": 20})
	httpxtest.Want(t, res, http.StatusAccepted)
	id := httpxtest.JSON[pendingBody](t, res).RequestID
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/approvals/"+id+"/approve", it, map[string]any{"note": "ok"}), http.StatusOK)
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if res := checked(t, app, http.MethodGet, place+"/resources/grande", ana, nil); res.Status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the approved create did not run with the asker's teams")
		}
	}

	// what exists is anyone's to change: Ventas grows Datos' cache
	res = checked(t, app, http.MethodPatch, place+"/resources/cache", bruno, map[string]any{"storage_gi": 2})
	httpxtest.Want(t, res, http.StatusOK)
}

func offers(o resource.Offered, recipe string) bool {
	for _, r := range o.Recipes {
		if r.Recipe == recipe {
			return true
		}
	}
	return false
}
