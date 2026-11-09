package bootstrap_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
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
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// TestRecipesOfTheOrganizationOverHTTP: stage 2 cut 4 with the real wiring.
// IT registers a database the company already has (only with
// catalog:manage, and secrets:external since it reads a vault the cluster
// announces); it is offered to nobody until IT offers it; whoever only
// reads the catalog does not see its connection; an app gets it as one
// that already exists; deleting it only disconnects it; and a new version
// is not offered until IT moves the offering.
func TestRecipesOfTheOrganizationOverHTTP(t *testing.T) {
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
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Cluster: cluster, Authority: ca, Gateway: "localhost:0",
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	// the worker writes the trail from the outbox
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
	const itUser = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	builder := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.ResourcesDelete, permission.AuditRead)
	catalogOnly := httpxtest.TokenAs(itUser, org, permission.ProjectsRead, permission.CatalogManage)
	it := httpxtest.TokenAs(itUser, org, permission.ProjectsRead, permission.CatalogManage, permission.SecretsExternal)

	body := map[string]any{"recipe": "org-reportes", "kind": "postgres", "title": "Base de reportes",
		"description": "La RDS de reportes de la empresa", "existing": resourcespec.ExamplePostgres("vault").Existing}

	// No cluster announces the vault yet: refused, and nothing is stored.
	refused(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes", it, body), http.StatusUnprocessableEntity, catalog.CodeStoreUnknown)
	res := checked(t, app, http.MethodGet, "/api/v1/catalog/stores", it, nil)
	httpxtest.Want(t, res, http.StatusOK)
	type stores struct {
		Items []catalog.Store `json:"items"`
	}
	if got := httpxtest.JSON[stores](t, res).Items; len(got) != 0 {
		t.Fatalf("stores before any cluster: %+v", got)
	}

	// A cluster that connects existing resources and reads the vault.
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: identity.MustParseUserID(itUser)})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	agent := fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial}
	if err := platform.Fleet.Agents.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "test",
		Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres", "redis", "files"},
			ResourceCapabilities: []string{resourcespec.ConnectExisting}, SecretStores: []string{"vault"}}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/stores", it, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[stores](t, res).Items; len(got) != 1 || got[0].Name != "vault" || len(got[0].Clusters) != 1 || got[0].Clusters[0].Name != "lima" {
		t.Fatalf("stores: %+v", got)
	}

	// Registering is IT's; reading a vault also needs secrets:external.
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/catalog/stores", builder, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes", builder, body), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes", catalogOnly, body), http.StatusForbidden)
	res = checked(t, app, http.MethodPost, "/api/v1/catalog/recipes", it, body)
	httpxtest.Want(t, res, http.StatusCreated)
	if loc := res.Header.Get("Location"); loc != "/api/v1/catalog/recipes/org-reportes" {
		t.Fatalf("location %q", loc)
	}
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Engine != resourcespec.EngineExisting || e.Offering.Active || e.Spec.Existing == nil {
		t.Fatalf("registered: %+v", e)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes", it, body), http.StatusConflict)

	// Whoever only reads the catalog sees that it exists, not where it is
	// nor which vault it reads (in one entry or in the list).
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/recipes/org-reportes", builder, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Engine != resourcespec.EngineExisting || e.Spec.Existing != nil ||
		slices.Contains(e.Requires, resourcespec.SecretStore("vault")) {
		t.Fatalf("a builder reads the connection: %s", res.Body)
	}
	for _, path := range []string{"/api/v1/catalog/recipes/org-reportes", "/api/v1/catalog/recipes"} {
		res := checked(t, app, http.MethodGet, path, builder, nil)
		httpxtest.Want(t, res, http.StatusOK)
		if b := string(res.Body); strings.Contains(b, "rds/reportes") || strings.Contains(b, "vault") || strings.Contains(b, "amazonaws") {
			t.Fatalf("%s leaks the connection: %s", path, b)
		}
	}
	// IT does read it
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/recipes", catalogOnly, nil)
	if !strings.Contains(string(res.Body), "rds/reportes") {
		t.Fatalf("IT cannot read the connection: %s", res.Body)
	}

	// An app: nothing offered yet, then IT offers it everywhere.
	res = checked(t, app, http.MethodPost, "/api/v1/projects", builder, map[string]any{"name": "Reportes"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", builder, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	offered := func() []resource.Offer {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/catalog", builder, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[resource.Offered](t, res).Recipes
	}
	isReportes := func(o resource.Offer) bool { return o.Recipe == "org-reportes" }
	if slices.ContainsFunc(offered(), isReportes) {
		t.Fatal("offered before IT offers it")
	}
	tiers := []string{"sandbox", "internal", "production"}
	httpxtest.Want(t, checked(t, app, http.MethodPut, "/api/v1/catalog/recipes/org-reportes/offering", it,
		map[string]any{"version": 1, "active": true, "tiers": tiers}), http.StatusOK)
	got := offered()
	i := slices.IndexFunc(got, isReportes)
	if i < 0 || !got[i].Existing || got[i].Version != 1 {
		t.Fatalf("offered: %+v", got)
	}

	// The app gets it as one the company already has.
	res = checked(t, app, http.MethodPost, base+"/resources", builder, map[string]any{"name": "reportes", "kind": "postgres", "recipe": "org-reportes"})
	httpxtest.Want(t, res, http.StatusCreated)
	if v := httpxtest.JSON[resource.Result](t, res); !v.Existing || v.Recipe != "org-reportes" {
		t.Fatalf("created: %+v", v)
	}

	// A v2 is published but not offered: the app still gets v1.
	v2 := map[string]any{"title": "Base de reportes (réplica)", "existing": resourcespec.ExamplePostgres("vault").Existing}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes/org-reportes/versions", catalogOnly, v2), http.StatusForbidden)
	res = checked(t, app, http.MethodPost, "/api/v1/catalog/recipes/org-reportes/versions", it, v2)
	httpxtest.Want(t, res, http.StatusCreated)
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Version != 1 || !slices.Equal(e.Versions, []int{1, 2}) {
		t.Fatalf("after v2: %+v", e)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes/"+catalog.PostgresBasic+"/versions", it, v2), http.StatusUnprocessableEntity)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/catalog/recipes/org-nada/versions", it, v2), http.StatusNotFound)
	if got := offered(); got[slices.IndexFunc(got, isReportes)].Version != 1 {
		t.Fatalf("v2 offered before IT moved the offering: %+v", got)
	}

	// Deleting it only disconnects it: the trail says so.
	res = checked(t, app, http.MethodDelete, base+"/resources/reportes", builder, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	if v := httpxtest.JSON[resource.View](t, res); !v.Existing || v.Status != resource.StatusDeleting {
		t.Fatalf("deleting: %+v", v)
	}
	trail := func(typ string) []audit.Entry {
		res := checked(t, app, http.MethodGet, "/api/v1/audit?type="+typ, builder, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Items
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		deleted, published := trail(resource.EventDeleted), trail(catalog.EventRecipePublished)
		if len(deleted) == 1 && len(published) == 2 {
			if s := deleted[0].Summary; !strings.HasPrefix(s, "Desconectó") || !strings.Contains(s, "no se tocan") {
				t.Fatalf("deleted: %q", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trail: %d deleted, %d published", len(deleted), len(published))
		}
	}
}
