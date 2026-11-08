package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestCatalogGovernsResources: stage 1a cut 3 over HTTP with the real wiring.
// The catalog resolves what fulfils a resource and its offering's
// pre-approved range is a floor of IT's rules: inside it a create or change
// goes through, outside it the operation is held as ONE request (with the
// connection when it connects) and runs, as approved, once IT approves; a
// rule only tightens it (deny wins); an approved request is checked again
// against the catalog when it runs; an app does not move to a tier where its
// data would be outside the catalog; and a recipe the cluster cannot prepare
// is unavailable.
func TestCatalogGovernsResources(t *testing.T) {
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
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond},
		Cluster: cluster, Authority: ca, Gateway: "localhost:0"})
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
	const itUser = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite)
	it := httpxtest.TokenAs(itUser, org, permission.ProjectsRead, permission.ProjectsWrite, permission.ProjectsTier,
		permission.ApprovalsDecide, permission.CatalogManage)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Inventario"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	rbase := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String() + "/resources"
	tierURL := "/api/v1/projects/" + proj.ID.String() + "/tier"

	// A cluster that prepares the three kinds (and announces no recipe spec).
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
		Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres", "redis", "files"}}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}

	errCode := func(res httpxtest.Response, status int, code string) {
		t.Helper()
		httpxtest.Want(t, res, status)
		if got := httpxtest.ErrorCode(res); got != code {
			t.Fatalf("code %q, want %q: %s", got, code, res.Body)
		}
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out: %s", what)
	}
	held := func(res httpxtest.Response) string {
		t.Helper()
		httpxtest.Want(t, res, http.StatusAccepted)
		p := httpxtest.JSON[pendingBody](t, res)
		if p.Status != "pending_approval" || p.RequestID == "" {
			t.Fatalf("not held: %s", res.Body)
		}
		return p.RequestID
	}
	request := func(id string) approval.Request {
		t.Helper()
		res := checked(t, app, http.MethodGet, "/api/v1/approvals/"+id, it, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[approval.Request](t, res)
	}
	approve := func(id string) {
		t.Helper()
		httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/approvals/"+id+"/approve", it, map[string]any{"note": "ok"}), http.StatusOK)
	}
	find := func(name string) (resource.View, bool) {
		t.Helper()
		res := checked(t, app, http.MethodGet, rbase+"/"+name, token, nil)
		if res.Status == http.StatusNotFound {
			return resource.View{}, false
		}
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[resource.View](t, res), true
	}
	offer := func(recipe string, draft map[string]any) {
		t.Helper()
		httpxtest.Want(t, checked(t, app, http.MethodPut, "/api/v1/catalog/recipes/"+recipe+"/offering", it, draft), http.StatusOK)
	}
	tiers := []string{"sandbox", "internal", "production"}

	// IT pre-approves basic databases up to 10 GiB (the limits stay 1–100).
	offer("postgres-basico", map[string]any{"version": 1, "active": true, "tiers": tiers, "defaults": tiers,
		"preapproved": map[string]any{"storage_gi": map[string]int{"max": 10}}})

	// Inside the range: created at once, pinned to the recipe that made it.
	res = checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres", "params": map[string]int{"storage_gi": 5}})
	httpxtest.Want(t, res, http.StatusCreated)
	if v := httpxtest.JSON[resource.Result](t, res); v.Recipe != "postgres-basico" || v.RecipeVersion != 1 || v.Params["storage_gi"] != 5 || v.StorageGi != 5 {
		t.Fatalf("created %+v", v)
	}

	// Outside it: held as one create_resource, nothing stored or applied.
	id := held(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "big", "kind": "postgres", "storage_gi": 20}))
	r := request(id)
	if r.Action != gate.ActionCreateResource || !slices.ContainsFunc(r.Rules, func(x approval.Rule) bool { return x.ID == catalog.RuleCatalog }) ||
		!slices.Contains(r.Approvers, permission.ApprovalsDecide) {
		t.Fatalf("the request %+v", r)
	}
	var payload resource.CreateApproval
	if err := json.Unmarshal(r.Payload, &payload); err != nil || payload.SchemaVersion != resource.PayloadVersion || len(payload.Steps) != 1 ||
		payload.Steps[0].Recipe != "postgres-basico" || payload.Steps[0].RecipeVersion != 1 || payload.Steps[0].Params["storage_gi"] != 20 {
		t.Fatalf("payload %s %v", r.Payload, err)
	}
	if _, ok := find("big"); ok {
		t.Fatal("a held create stored the resource")
	}
	if _, ok, _ := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "big"); ok {
		t.Fatal("a held create reached the cluster")
	}
	// IT approves: it runs, as approved, once.
	approve(id)
	waitFor("the approved create to run", func() bool { v, ok := find("big"); return ok && v.StorageGi == 20 })
	waitFor("the approval to be consumed", func() bool { return request(id).ConsumedAt != nil })

	// A change inside the range goes through; outside it, it is held and
	// nothing changes until it is approved.
	res = checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"params": map[string]int{"storage_gi": 8}})
	httpxtest.Want(t, res, http.StatusOK)
	if v := httpxtest.JSON[resource.View](t, res); v.StorageGi != 8 || v.Generation != 2 {
		t.Fatalf("changed %+v", v)
	}
	id = held(checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 30}))
	if r := request(id); r.Action != gate.ActionChangeResource {
		t.Fatalf("change request %+v", r)
	}
	if v, _ := find("db"); v.StorageGi != 8 {
		t.Fatalf("a held change changed it: %+v", v)
	}
	approve(id)
	waitFor("the approved change to run", func() bool { v, _ := find("db"); return v.StorageGi == 30 && v.Generation == 3 })
	if d, _, _ := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "db"); d.StorageGi != 30 || d.Generation != 3 {
		t.Fatalf("desired after the approved change %+v", d)
	}

	// A rule only tightens the catalog: a deny wins over a pre-approved size.
	small, err := platform.Policy.Commands.Create(ctx, org, policy.Create{CreatedBy: identity.MustParseUserID(itUser), Draft: policy.Draft{
		Name: "Nada de más de 3 GiB", Expression: "action == 'create_resource' && resource.params.storage_gi > 3", Effect: policy.EffectDeny}})
	if err != nil {
		t.Fatal(err)
	}
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "four", "kind": "postgres", "storage_gi": 4}),
		http.StatusForbidden, gate.CodeDenied)
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "three", "kind": "postgres", "storage_gi": 3}),
		http.StatusCreated)
	if err := platform.Policy.Commands.Delete(ctx, org, small.ID, policy.Delete{DeletedBy: identity.MustParseUserID(itUser)}); err != nil {
		t.Fatal(err)
	}

	// An approved request is checked again when it runs: IT narrowed the
	// limits meanwhile, so it fails (in words) and creates nothing.
	id = held(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "late", "kind": "postgres", "storage_gi": 40}))
	offer("postgres-basico", map[string]any{"version": 1, "active": true, "tiers": tiers, "defaults": tiers,
		"limits": map[string]any{"storage_gi": map[string]int{"max": 35}}, "preapproved": map[string]any{"storage_gi": map[string]int{"max": 10}}})
	approve(id)
	waitFor("the stale approval to fail", func() bool { return request(id).ExecutionError != "" })
	if _, ok := find("late"); ok {
		t.Fatal("an approval the catalog no longer allows created the resource")
	}

	// A create that connects is judged and held ONCE, with the connection.
	connectRule, err := platform.Policy.Commands.Create(ctx, org, policy.Create{CreatedBy: identity.MustParseUserID(itUser), Draft: policy.Draft{
		Name: "Conectar se revisa", Expression: "action == 'connect_resource'", Effect: policy.EffectApproval}})
	if err != nil {
		t.Fatal(err)
	}
	id = held(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "cache", "kind": "redis", "connect": map[string]any{"prefix": "CACHE"}}))
	if r := request(id); r.Action != gate.ActionCreateResource || !slices.ContainsFunc(r.Rules, func(x approval.Rule) bool { return x.ID == connectRule.ID.String() }) {
		t.Fatalf("create-and-connect request %+v", r)
	}
	approve(id)
	waitFor("the approved create-and-connect to run", func() bool { v, ok := find("cache"); return ok && v.Connected && v.Prefix == "CACHE" })
	if err := platform.Policy.Commands.Delete(ctx, org, connectRule.ID, policy.Delete{DeletedBy: identity.MustParseUserID(itUser)}); err != nil {
		t.Fatal(err)
	}

	// The app does not move to a tier where its databases' recipe is not
	// offered; it does once IT offers it there.
	offer("postgres-basico", map[string]any{"version": 1, "active": true, "tiers": []string{"sandbox", "internal"}, "defaults": []string{"sandbox", "internal"}})
	errCode(checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "production"}), http.StatusConflict, project.CodeTierResources)
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "internal"}), http.StatusOK) // the same tier: a no-op
	offer("postgres-basico", map[string]any{"version": 1, "active": true, "tiers": tiers, "defaults": tiers})
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "production"}), http.StatusOK)

	// A recipe the cluster cannot prepare (no spec announced) is unavailable.
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "prod", "kind": "postgres", "recipe": "postgres-produccion"}),
		http.StatusUnprocessableEntity, catalog.CodeUnavailable)
}
