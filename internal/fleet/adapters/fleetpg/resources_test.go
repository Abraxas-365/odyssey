package fleetpg_test

import (
	"reflect"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

func TestDesiredResources(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c, theirs := e.create(org, "lima"), e.create(other, "lima")
	e.enrol(c, "s1")
	e.enrol(theirs, "s2")
	proj, environment := project.NewID(), project.NewEnvironmentID()
	if _, err := repo.Place(ctx, org, proj, environment, c.ID); err != nil {
		t.Fatal(err)
	}

	res := fleet.DesiredResource{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "db",
		Kind: resourcekind.Postgres, Generation: 2, StorageGi: 5}
	if changed, err := repo.PutResource(ctx, res); err != nil || !changed {
		t.Fatalf("put %v %v", changed, err)
	}
	older := res
	older.Generation, older.StorageGi = 1, 1
	if changed, err := repo.PutResource(ctx, older); err != nil || changed {
		t.Fatalf("an older generation replaced the newer: %v %v", changed, err)
	}
	if changed, _ := repo.PutResource(ctx, res); changed {
		t.Fatal("the same generation counted as a change")
	}
	hijack := res
	hijack.Organization, hijack.Cluster, hijack.Generation = other, theirs.ID, 9
	if changed, _ := repo.PutResource(ctx, hijack); changed {
		t.Fatal("another tenant overwrote the resource")
	}
	got, err := repo.FindResource(ctx, org, c.ID, environment, "db")
	if err != nil || got.Generation != 2 || got.StorageGi != 5 || got.Kind != resourcekind.Postgres || got.Deleted || got.Project != proj {
		t.Fatalf("find %+v %v", got, err)
	}
	_, err = repo.FindResource(ctx, other, theirs.ID, environment, "db")
	notFound(t, "cross-tenant resource", err)
	_, err = repo.FindResource(ctx, other, c.ID, environment, "db")
	notFound(t, "cross-tenant resource on the cluster", err)
	if list, err := repo.ListResources(ctx, org, c.ID); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if list, _ := repo.ListResources(ctx, other, c.ID); len(list) != 0 {
		t.Fatalf("another tenant lists %v", list)
	}
	if list, _ := repo.ListResources(ctx, other, theirs.ID); len(list) != 0 {
		t.Fatalf("other tenant's cluster lists %v", list)
	}

	// reports: only from the cluster's current tunnel, only for resources
	// desired on the reporting cluster, never beyond the desired generation,
	// never backwards
	mine, stale := fleet.NewSession(), fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, mine, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSession(ctx, other, theirs.ID, fleet.NewSession(), fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	st := func(gen int64, phase fleet.ResourcePhase, msg string) fleet.ResourceReport {
		return fleet.ResourceReport{Resources: []fleet.ResourceStatus{{Environment: environment, Name: "db", ObservedGeneration: gen, Phase: phase, Message: msg}}}
	}
	if live, _, err := repo.ObserveResources(ctx, org, c.ID, stale, st(2, fleet.ResourceReady, "")); err != nil || live {
		t.Fatalf("a stale tunnel wrote: %v %v", live, err)
	}
	if _, err := repo.FindResourceStatus(ctx, org, environment, "db"); err == nil {
		t.Fatal("a stale tunnel stored a status")
	}
	if live, _, err := repo.ObserveResources(ctx, org, c.ID, mine, st(2, fleet.ResourceProvisioning, "creando")); err != nil || !live {
		t.Fatalf("observe %v %v", live, err)
	}
	if s, err := repo.FindResourceStatus(ctx, org, environment, "db"); err != nil || s.Phase != fleet.ResourceProvisioning || s.Message != "creando" || s.ObservedGeneration != 2 {
		t.Fatalf("status %+v %v", s, err)
	}
	repo.ObserveResources(ctx, org, c.ID, mine, st(3, fleet.ResourceFailed, "beyond desired"))
	repo.ObserveResources(ctx, org, c.ID, mine, st(1, fleet.ResourceFailed, "backwards"))
	if s, _ := repo.FindResourceStatus(ctx, org, environment, "db"); s.Phase != fleet.ResourceProvisioning {
		t.Fatalf("a bogus generation changed the status: %+v", s)
	}
	repo.ObserveResources(ctx, org, c.ID, mine, st(2, fleet.ResourceReady, ""))
	if s, _ := repo.FindResourceStatus(ctx, org, environment, "db"); s.Phase != fleet.ResourceReady {
		t.Fatalf("status %+v", s)
	}
	_, err = repo.FindResourceStatus(ctx, other, environment, "db")
	notFound(t, "cross-tenant status", err)
	// another tenant's cluster cannot report on it
	otherSession := fleet.NewSession()
	if err := repo.SetSession(ctx, other, theirs.ID, otherSession, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	repo.ObserveResources(ctx, other, theirs.ID, otherSession, st(2, fleet.ResourceFailed, "forged"))
	if s, _ := repo.FindResourceStatus(ctx, org, environment, "db"); s.Phase != fleet.ResourceReady {
		t.Fatalf("another tenant wrote the status: %+v", s)
	}

	// "deleted" only removes a resource being deleted
	if _, removed, _ := repo.ObserveResources(ctx, org, c.ID, mine, st(2, fleet.ResourceDeleted, "")); len(removed) != 0 {
		t.Fatalf("a live resource was removed: %v", removed)
	}
	del := res
	del.Generation, del.Deleted = 3, true
	if changed, err := repo.PutResource(ctx, del); err != nil || !changed {
		t.Fatalf("delete %v %v", changed, err)
	}
	if _, removed, _ := repo.ObserveResources(ctx, org, c.ID, mine, st(2, fleet.ResourceDeleted, "")); len(removed) != 0 {
		t.Fatalf("removed by a report older than the deletion: %v", removed)
	}
	if _, removed, _ := repo.ObserveResources(ctx, other, theirs.ID, otherSession, st(3, fleet.ResourceDeleted, "")); len(removed) != 0 {
		t.Fatalf("another tenant removed it: %v", removed)
	}
	live, removed, err := repo.ObserveResources(ctx, org, c.ID, mine, st(3, fleet.ResourceDeleted, ""))
	if err != nil || !live || len(removed) != 1 || removed[0].Name == "" || removed[0].Project != proj {
		t.Fatalf("removed %v %v %v", live, removed, err)
	}
	_, err = repo.FindResource(ctx, org, c.ID, environment, "db")
	notFound(t, "removed resource", err)
	_, err = repo.FindResourceStatus(ctx, org, environment, "db")
	notFound(t, "removed status", err)

	// a resource moves with its environment's apps, its status does not
	again := res
	again.Generation = 10
	if _, err := repo.PutResource(ctx, again); err != nil {
		t.Fatal(err)
	}
	repo.ObserveResources(ctx, org, c.ID, mine, st(10, fleet.ResourceReady, ""))
	to := e.create(org, "arequipa")
	e.enrol(to, "s3")
	if _, err := repo.MovePlacement(ctx, org, environment, c.ID, to.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := repo.ListResources(ctx, org, to.ID); len(list) != 1 {
		t.Fatalf("the resource did not move: %v", list)
	}
	if list, _ := repo.ListResources(ctx, org, c.ID); len(list) != 0 {
		t.Fatalf("the resource stayed: %v", list)
	}
	_, err = repo.FindResourceStatus(ctx, org, environment, "db")
	notFound(t, "status after the move", err)
}

// A recipe's spec and ref are stored and read back as they went in, change
// with a newer generation, and the environment's resources are listed for
// one tenant and cluster only.
func TestDesiredResourceSpec(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c, theirs := e.create(org, "lima"), e.create(other, "lima")
	e.enrol(c, "s1")
	e.enrol(theirs, "s2")
	proj, environment, elsewhere := project.NewID(), project.NewEnvironmentID(), project.NewEnvironmentID()
	for _, env := range []project.EnvironmentID{environment, elsewhere} {
		if _, err := repo.Place(ctx, org, proj, env, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	spec := resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17, CPU: "1", Memory: "2Gi"}}
	db := fleet.DesiredResource{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "db",
		Kind: resourcekind.Postgres, Generation: 1, StorageGi: 10, Recipe: "postgres-produccion@1", Spec: spec}
	plain := fleet.DesiredResource{Organization: org, Project: proj, Environment: elsewhere, Cluster: c.ID, Name: "cache",
		Kind: resourcekind.Redis, Generation: 1, StorageGi: 1}
	for _, d := range []fleet.DesiredResource{db, plain} {
		if _, err := repo.PutResource(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.FindResource(ctx, org, c.ID, environment, "db")
	if err != nil || got.Recipe != db.Recipe || !reflect.DeepEqual(got.Spec, spec) {
		t.Fatalf("find %+v %v", got, err)
	}
	if got, _ := repo.FindResource(ctx, org, c.ID, elsewhere, "cache"); got.Recipe != "" || got.Spec.Postgres != nil {
		t.Fatalf("a resource without a recipe %+v", got)
	}
	grown := db
	grown.Generation, grown.Spec = 2, resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 3, Major: 17}}
	if changed, err := repo.PutResource(ctx, grown); err != nil || !changed {
		t.Fatalf("put %v %v", changed, err)
	}
	if got, _ := repo.FindResource(ctx, org, c.ID, environment, "db"); !reflect.DeepEqual(got.Spec, grown.Spec) {
		t.Fatalf("spec after a change %+v", got.Spec)
	}

	list, err := repo.ListEnvironmentResources(ctx, org, c.ID, environment)
	if err != nil || len(list) != 1 || list[0].Name != "db" || !reflect.DeepEqual(list[0].Spec, grown.Spec) {
		t.Fatalf("environment resources %+v %v", list, err)
	}
	if list, err := repo.ListEnvironmentResources(ctx, other, c.ID, environment); err != nil || len(list) != 0 {
		t.Fatalf("another tenant lists %v %v", list, err)
	}
	if list, _ := repo.ListEnvironmentResources(ctx, other, theirs.ID, environment); len(list) != 0 {
		t.Fatalf("another tenant's cluster lists %v", list)
	}
	if list, _ := repo.ListEnvironmentResources(ctx, org, theirs.ID, environment); len(list) != 0 {
		t.Fatalf("another cluster lists %v", list)
	}
}

// A full report lists every resource of the cluster: the status of one it
// leaves out is forgotten (the console shows "preparing" again); a partial
// report, a stale tunnel and another tenant never forget anything.
func TestFullResourceReportForgetsWhatTheAgentLost(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c, theirs := e.create(org, "lima"), e.create(other, "lima")
	e.enrol(c, "s1")
	e.enrol(theirs, "s2")
	proj, environment := project.NewID(), project.NewEnvironmentID()
	theirProj, theirEnv := project.NewID(), project.NewEnvironmentID()
	for _, p := range []struct {
		o   identity.OrganizationID
		pr  project.ID
		env project.EnvironmentID
		c   fleet.Cluster
	}{{org, proj, environment, c}, {other, theirProj, theirEnv, theirs}} {
		if _, err := repo.Place(ctx, p.o, p.pr, p.env, p.c.ID); err != nil {
			t.Fatal(err)
		}
	}
	put := func(o identity.OrganizationID, pr project.ID, env project.EnvironmentID, cl fleet.Cluster, name string) {
		t.Helper()
		d := fleet.DesiredResource{Organization: o, Project: pr, Environment: env, Cluster: cl.ID, Name: name,
			Kind: resourcekind.Postgres, Generation: 1, StorageGi: 5}
		if changed, err := repo.PutResource(ctx, d); err != nil || !changed {
			t.Fatalf("put %s: %v %v", name, changed, err)
		}
	}
	put(org, proj, environment, c, "db")
	put(org, proj, environment, c, "cache")
	put(other, theirProj, theirEnv, theirs, "db")
	mine, theirSession := fleet.NewSession(), fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, mine, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSession(ctx, other, theirs.ID, theirSession, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	ready := func(env project.EnvironmentID, names ...string) []fleet.ResourceStatus {
		var out []fleet.ResourceStatus
		for _, n := range names {
			out = append(out, fleet.ResourceStatus{Environment: env, Name: n, ObservedGeneration: 1, Phase: fleet.ResourceReady})
		}
		return out
	}
	observe := func(o identity.OrganizationID, cl fleet.Cluster, s fleet.Session, full bool, rs []fleet.ResourceStatus) {
		t.Helper()
		if live, _, err := repo.ObserveResources(ctx, o, cl.ID, s, fleet.ResourceReport{Full: full, Resources: rs}); err != nil || !live {
			t.Fatalf("observe: %v %v", live, err)
		}
	}
	has := func(o identity.OrganizationID, env project.EnvironmentID, name string) bool {
		_, err := repo.FindResourceStatus(ctx, o, env, name)
		return err == nil
	}
	observe(org, c, mine, true, ready(environment, "db", "cache"))
	observe(other, theirs, theirSession, true, ready(theirEnv, "db"))

	// a partial report that omits a resource keeps its status
	observe(org, c, mine, false, ready(environment, "db"))
	if !has(org, environment, "cache") {
		t.Fatal("a partial report forgot a status")
	}
	// a stale tunnel's full report forgets nothing
	if live, _, err := repo.ObserveResources(ctx, org, c.ID, fleet.NewSession(), fleet.ResourceReport{Full: true}); err != nil || live {
		t.Fatalf("a stale tunnel was live: %v %v", live, err)
	}
	if !has(org, environment, "db") || !has(org, environment, "cache") {
		t.Fatal("a stale tunnel forgot a status")
	}
	// another tenant's full (empty) report does not touch this tenant's
	observe(other, theirs, theirSession, true, nil)
	if !has(org, environment, "db") || has(other, theirEnv, "db") {
		t.Fatal("a full report crossed tenants, or did not forget its own")
	}
	// the cluster's full report without "cache" forgets it, and only it
	observe(org, c, mine, true, ready(environment, "db"))
	if has(org, environment, "cache") || !has(org, environment, "db") {
		t.Fatal("the full report did not forget exactly the resource it left out")
	}
	// its desired state stays: the agent can bring it back
	if _, err := repo.FindResource(ctx, org, c.ID, environment, "cache"); err != nil {
		t.Fatalf("forgetting the status dropped the desired resource: %v", err)
	}
	observe(org, c, mine, true, ready(environment, "db", "cache"))
	if !has(org, environment, "cache") {
		t.Fatal("a reappearing resource was not stored")
	}
}

// A deletion pending on a cluster that is gone follows the environment when
// IT moves it: the new cluster receives the row with deleted=true.
func TestMovePlacementCarriesPendingDeletions(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org := identity.NewOrganizationID()
	from, to := e.create(org, "lima"), e.create(org, "arequipa")
	e.enrol(from, "s1")
	e.enrol(to, "s2")
	proj, environment := project.NewID(), project.NewEnvironmentID()
	if _, err := repo.Place(ctx, org, proj, environment, from.ID); err != nil {
		t.Fatal(err)
	}
	d := fleet.DesiredResource{Organization: org, Project: proj, Environment: environment, Cluster: from.ID, Name: "db",
		Kind: resourcekind.Postgres, Generation: 3, StorageGi: 5, Deleted: true}
	if _, err := repo.PutResource(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MovePlacement(ctx, org, environment, from.ID, to.ID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindResource(ctx, org, to.ID, environment, "db")
	if err != nil || !got.Deleted || got.Generation != 3 {
		t.Fatalf("the pending deletion did not move: %+v %v", got, err)
	}
}
