package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestResources: 2b's control plane end to end over HTTP with a fleet
// cluster whose agent is played by the test: create → desired in the fleet
// (and refused while the cluster cannot provision the kind), connect → a
// settings revision and a config deployment whose desired app carries the
// resource (and not its variables), conflicts and the in-use guard, delete →
// deleting until the agent reports the data gone, then forgotten.
func TestResources(t *testing.T) {
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
		Workflow:  workflowpg.Options{PollInterval: 20 * time.Millisecond},
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
	deleter := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.ResourcesDelete)
	stranger := httpxtest.Token(other, permission.ProjectsRead, permission.ProjectsWrite, permission.ResourcesDelete)
	reader := httpxtest.Token(org, permission.ProjectsRead)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Pagos"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	rbase := base + "/resources"

	await := func(id string) {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			d = httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+id, token, nil))
			if d.Status == release.StatusReady {
				return
			}
			if d.Status == release.StatusFailed {
				t.Fatalf("deployment failed: %+v", d.Error)
			}
		}
		t.Fatalf("deployment %s is %s", id, d.Status)
	}
	last := func() release.DesiredApp {
		t.Helper()
		a := cluster.Applied()
		if len(a) == 0 {
			t.Fatal("nothing applied")
		}
		return a[len(a)-1].App
	}
	list := func() resource.Listing {
		t.Helper()
		res := checked(t, app, http.MethodGet, rbase, token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[resource.Listing](t, res)
	}
	errCode := func(res httpxtest.Response, status int, code string) {
		t.Helper()
		httpxtest.Want(t, res, status)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(res.Body, &body); err != nil {
			t.Fatal(err)
		}
		if got := body.Error.Code + body.Code; got != code {
			t.Fatalf("code %q, want %q: %s", got, code, res.Body)
		}
	}

	// The app is online, so connecting starts config deployments.
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080})
	httpxtest.Want(t, res, http.StatusAccepted)
	await(httpxtest.JSON[release.Deployment](t, res).ID.String())

	// No cluster at all: nothing is stored.
	if l := list(); len(l.Items) != 0 || len(l.Kinds) != 0 {
		t.Fatalf("empty listing %+v", l)
	}
	res = checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres"})
	if res.Status != http.StatusUnprocessableEntity && res.Status != http.StatusConflict {
		t.Fatalf("create without a cluster → %d: %s", res.Status, res.Body)
	}
	if l := list(); len(l.Items) != 0 {
		t.Fatalf("a refused create left %+v", l.Items)
	}

	// An agent older than 2b: the cluster cannot provision anything.
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	agent := fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial}
	session := fleet.NewSession()
	online := func(kinds ...string) {
		t.Helper()
		session = fleet.NewSession()
		if err := platform.Fleet.Agents.Connect(ctx, agent, session, fleet.Agent{Version: "test",
			Capabilities: fleet.Capabilities{ResourceKinds: kinds}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	online()
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres"}),
		http.StatusUnprocessableEntity, fleet.CodeResourcesUnavailable)
	if l := list(); len(l.Items) != 0 || len(l.Kinds) != 0 {
		t.Fatalf("listing after a refusal %+v", l)
	}
	online("postgres", "redis", "files")
	if l := list(); len(l.Kinds) != 3 {
		t.Fatalf("kinds %v", l.Kinds)
	}

	// Validation.
	for _, body := range []map[string]any{
		{"name": "Bad Name", "kind": "postgres"},
		{"name": "db", "kind": "mysql"},
		{"name": "db", "kind": "postgres", "storage_gi": 101},
		{"name": "db", "kind": "postgres", "connect": map[string]any{"prefix": "lower"}},
	} {
		httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, body), http.StatusBadRequest)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, reader, map[string]any{"name": "db", "kind": "postgres"}), http.StatusForbidden)

	// Create without connecting: desired in the fleet, nothing in the app.
	res = checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres", "storage_gi": 2})
	httpxtest.Want(t, res, http.StatusCreated)
	db1 := httpxtest.JSON[resource.Result](t, res)
	if db1.Name != "db" || db1.StorageGi != 2 || db1.Status != resource.StatusActive || db1.Phase != resource.PhaseProvisioning ||
		db1.Connected || db1.Deployment != nil || db1.Revision != 0 || len(db1.Variables) == 0 || db1.Generation != 1 {
		t.Fatalf("created %+v", db1)
	}
	if loc := res.Header.Get("Location"); !strings.HasSuffix(loc, "/resources/db") {
		t.Fatalf("Location %q", loc)
	}
	desired, ok, err := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "db")
	if err != nil || !ok || desired.Kind != "postgres" || desired.StorageGi != 2 || desired.Deleted || desired.Generation != 1 {
		t.Fatalf("desired %+v %v %v", desired, ok, err)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "redis"}), http.StatusConflict)

	// The agent reports it: the console shows the phase.
	reportAs := func(gen int64, name string, phase fleet.ResourcePhase, msg string) {
		t.Helper()
		if err := platform.Fleet.Agents.ReportResources(ctx, agent, session, fleet.ResourceReport{Resources: []fleet.ResourceStatus{
			{Environment: env.ID, Name: name, ObservedGeneration: gen, Phase: phase, Message: msg}}}); err != nil {
			t.Fatal(err)
		}
	}
	reportAs(1, "db", fleet.ResourceFailed, "no hay espacio")
	got := httpxtest.JSON[resource.View](t, checked(t, app, http.MethodGet, rbase+"/db", token, nil))
	if got.Phase != resource.PhaseFailed || got.Message != "no hay espacio" {
		t.Fatalf("failed phase %+v", got)
	}
	reportAs(1, "db", fleet.ResourceReady, "")
	if got := httpxtest.JSON[resource.View](t, checked(t, app, http.MethodGet, rbase+"/db", token, nil)); got.Phase != resource.PhaseReady || got.Message != "" {
		t.Fatalf("ready phase %+v", got)
	}

	// Grow only.
	httpxtest.Want(t, checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 1}), http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 101}), http.StatusBadRequest)
	res = checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 5})
	httpxtest.Want(t, res, http.StatusOK)
	if v := httpxtest.JSON[resource.View](t, res); v.StorageGi != 5 || v.Generation != 2 {
		t.Fatalf("resized %+v", v)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 5}), http.StatusOK)
	if v := httpxtest.JSON[resource.View](t, checked(t, app, http.MethodGet, rbase+"/db", token, nil)); v.Generation != 2 {
		t.Fatalf("resizing to the same size bumped the generation: %+v", v)
	}
	if d, _, _ := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "db"); d.StorageGi != 5 || d.Generation != 2 {
		t.Fatalf("desired after resize %+v", d)
	}

	// Connect: a settings revision and a config deployment carrying the resource.
	res = checked(t, app, http.MethodPut, rbase+"/db/connection", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	conn := httpxtest.JSON[resource.Result](t, res)
	if !conn.Connected || conn.Revision != 1 || conn.Deployment == nil || conn.Prefix != "" {
		t.Fatalf("connected %+v", conn)
	}
	await(*conn.Deployment)
	a := last()
	if len(a.Resources) != 1 || a.Resources[0].Name != "db" || a.Resources[0].Kind != "postgres" {
		t.Fatalf("applied resources %+v", a.Resources)
	}
	for _, v := range conn.Variables {
		if _, ok := a.Env[v]; ok {
			t.Fatalf("%s was written into the plain env: the cluster builds it", v)
		}
	}
	if s := httpxtest.JSON[struct {
		Resources []release.ResourceUse `json:"resources"`
	}](t, checked(t, app, http.MethodGet, base+"/settings", token, nil)); len(s.Resources) != 1 || s.Resources[0].Name != "db" {
		t.Fatalf("settings %+v", s)
	}
	// Connecting again changes nothing.
	res = checked(t, app, http.MethodPut, rbase+"/db/connection", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	if again := httpxtest.JSON[resource.Result](t, res); again.Deployment != nil || again.Revision != 1 {
		t.Fatalf("connecting twice %+v", again)
	}

	// Variable conflicts: with a plain variable, with another resource.
	errCode(checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{conn.Variables[0]: "x"}}),
		http.StatusConflict, release.CodeVariableTaken)
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db2", "kind": "postgres", "connect": map[string]any{}}),
		http.StatusConflict, release.CodeVariableTaken)
	for _, r := range list().Items {
		if r.Name == "db2" {
			t.Fatalf("a create whose connection conflicted left the resource behind")
		}
	}
	if _, ok, _ := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "db2"); ok {
		t.Fatal("a create whose connection conflicted left a desired resource")
	}
	// Create and connect with a prefix in one request.
	res = checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "analytics", "kind": "postgres", "connect": map[string]any{"prefix": "ANALYTICS"}})
	httpxtest.Want(t, res, http.StatusCreated)
	an := httpxtest.JSON[resource.Result](t, res)
	if !an.Connected || an.Prefix != "ANALYTICS" || an.Deployment == nil || !strings.HasPrefix(an.Variables[0], "ANALYTICS_") {
		t.Fatalf("created and connected %+v", an)
	}
	await(*an.Deployment)
	if a := last(); len(a.Resources) != 2 {
		t.Fatalf("applied resources %+v", a.Resources)
	}
	l := list()
	if len(l.Items) != 2 || l.Items[0].Name != "db" || l.Items[1].Name != "analytics" || !l.Items[0].Connected || !l.Items[1].Connected {
		t.Fatalf("listing %+v", l.Items)
	}

	// Delete: refused while the app uses it, and only with the permission.
	httpxtest.Want(t, checked(t, app, http.MethodDelete, rbase+"/db", token, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, rbase+"/db", reader, nil), http.StatusForbidden)
	errCode(checked(t, app, http.MethodDelete, rbase+"/db", deleter, nil), http.StatusConflict, resource.CodeInUse)
	if v := httpxtest.JSON[resource.View](t, checked(t, app, http.MethodGet, rbase+"/db", token, nil)); v.Status != resource.StatusActive {
		t.Fatalf("a refused delete changed it: %+v", v)
	}

	// Tenant isolation: another organization sees and touches nothing.
	httpxtest.Want(t, checked(t, app, http.MethodGet, rbase, stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodGet, rbase+"/db", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, rbase+"/db", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPut, rbase+"/db/connection", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, stranger, map[string]any{"name": "x", "kind": "redis"}), http.StatusNotFound)

	// Disconnect, then delete.
	res = checked(t, app, http.MethodDelete, rbase+"/db/connection", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	dis := httpxtest.JSON[resource.Result](t, res)
	if dis.Connected || dis.Deployment == nil {
		t.Fatalf("disconnected %+v", dis)
	}
	await(*dis.Deployment)
	if a := last(); len(a.Resources) != 1 || a.Resources[0].Name != "analytics" {
		t.Fatalf("applied after disconnect %+v", a.Resources)
	}
	res = checked(t, app, http.MethodDelete, rbase+"/db", deleter, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	if v := httpxtest.JSON[resource.View](t, res); v.Status != resource.StatusDeleting || v.Generation != 3 {
		t.Fatalf("deleting %+v", v)
	}
	if d, _, _ := platform.Fleet.Agents.DesiredResource(ctx, agent, env.ID, "db"); !d.Deleted || d.Generation != 3 {
		t.Fatalf("desired after delete %+v", d)
	}
	// A resource being deleted takes no changes; deleting again is harmless.
	errCode(checked(t, app, http.MethodPut, rbase+"/db/connection", token, nil), http.StatusConflict, release.CodeResourceDeleting)
	errCode(checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 9}), http.StatusConflict, resource.CodeDeleting)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, rbase+"/db", deleter, nil), http.StatusAccepted)
	// It still counts and keeps its name until the cluster confirms.
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres"}), http.StatusConflict)

	// The agent reports the data gone: the row is forgotten, the name is free.
	reportAs(3, "db", fleet.ResourceDeleted, "")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if l := list(); len(l.Items) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deleted resource was not forgotten: %+v", list().Items)
		}
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, rbase+"/db", token, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "db", "kind": "postgres"}), http.StatusCreated)

	// The limit of ten (deleting ones count).
	for i := range 8 {
		name := "c" + string(rune('a'+i))
		httpxtest.Want(t, checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": name, "kind": "redis"}), http.StatusCreated)
	}
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "one-more", "kind": "redis"}), http.StatusConflict, resource.CodeLimit)

	// An archived app takes no new resources and no resizing.
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/projects/"+proj.ID.String()+"/archive", token, nil), http.StatusOK)
	errCode(checked(t, app, http.MethodPatch, rbase+"/db", token, map[string]any{"storage_gi": 9}), http.StatusUnprocessableEntity, project.CodeArchived)
	errCode(checked(t, app, http.MethodPost, rbase, token, map[string]any{"name": "late", "kind": "redis"}), http.StatusUnprocessableEntity, project.CodeArchived)
	if v := httpxtest.JSON[resource.View](t, checked(t, app, http.MethodGet, rbase+"/db", token, nil)); v.StorageGi == 9 {
		t.Fatalf("an archived app was resized: %+v", v)
	}

	// The trail has the whole story, and no event carries a credential.
	res = checked(t, app, http.MethodGet, "/api/v1/audit?limit=100", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	seen := map[string]bool{}
	for _, e := range httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Items {
		seen[e.Type] = true
	}
	for _, typ := range []string{resource.EventCreated, resource.EventResized, resource.EventDeleted, resource.EventRemoved,
		release.EventResourcesChanged, fleet.EventResourceApplied} {
		if !seen[typ] {
			t.Errorf("the trail has no %s: %v", typ, seen)
		}
	}
}
