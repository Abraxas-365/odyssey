package fleetpg_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

func TestRetiredApps(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c, theirs := e.create(org, "lima"), e.create(other, "lima")
	e.enrol(c, "s1")
	e.enrol(theirs, "s2")
	mine, theirSession := fleet.NewSession(), fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, mine, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSession(ctx, other, theirs.ID, theirSession, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	proj, environment := project.NewID(), project.NewEnvironmentID()
	if _, err := repo.Place(ctx, org, proj, environment, c.ID); err != nil {
		t.Fatal(err)
	}
	app := fleet.App{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "web", Revision: 2,
		Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1, Access: fleet.AccessPublic}
	if _, err := repo.PutApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	report := func(o identity.OrganizationID, cluster fleet.ClusterID, session fleet.Session, rev int64, phase fleet.Phase) []fleet.App {
		t.Helper()
		_, removed, err := repo.ObserveApps(ctx, o, cluster, session, fleet.Report{Apps: []fleet.AppStatus{{Environment: environment, Name: "web",
			ObservedRevision: rev, Phase: phase}}})
		if err != nil {
			t.Fatal(err)
		}
		return removed
	}
	report(org, c.ID, mine, 2, fleet.PhaseReady)
	delivery := fleet.SecretDelivery{ID: fleet.NewSecretDeliveryID(), Organization: org, Project: proj, Environment: environment, Cluster: c.ID,
		App: "web", Name: "API_KEY", Version: 1, KeyID: "k", Sealed: bytes.Repeat([]byte{1}, 64), CreatedBy: e.user, CreatedAt: time.Now().UTC()}
	if _, err := repo.PutSecretDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}

	retire := fleet.Retirement{Organization: org, Project: proj, Environment: environment, Name: "web", Revision: 3}
	// another tenant (or project) cannot retire it
	for _, r := range []fleet.Retirement{
		{Organization: other, Project: proj, Environment: environment, Name: "web", Revision: 9},
		{Organization: org, Project: project.NewID(), Environment: environment, Name: "web", Revision: 9},
	} {
		if _, ok, err := repo.RetireApp(ctx, r); err != nil || ok {
			t.Fatalf("retired by a stranger %+v: %v %v", r, ok, err)
		}
	}
	got, ok, err := repo.RetireApp(ctx, retire)
	if err != nil || !ok || !got.Deleted || got.Revision != 3 || got.Cluster != c.ID {
		t.Fatalf("retire %+v %v %v", got, ok, err)
	}
	if _, ok, _ := repo.RetireApp(ctx, retire); ok {
		t.Fatal("the same retirement counted twice")
	}
	if list, _ := repo.ListSecretDeliveries(ctx, org, c.ID, time.Now().Add(-time.Hour)); len(list) != 0 {
		t.Errorf("values waiting for a retired app must go: %+v", list)
	}
	if a, err := repo.FindApp(ctx, org, c.ID, environment, "web"); err != nil || !a.Deleted || a.Revision != 3 {
		t.Fatalf("find retired %+v %v", a, err)
	}
	if cl, _ := repo.FindCluster(ctx, org, c.ID); cl.Apps != 0 {
		t.Errorf("a retired app is not counted as running there: %d", cl.Apps)
	}
	// an older deployment never revives it
	if changed, _ := repo.PutApp(ctx, app); changed {
		t.Fatal("an older deployment revived the retired app")
	}

	// "deleted" reports that do not prove the retirement change nothing:
	// another tenant's, an older revision's
	if removed := report(other, theirs.ID, theirSession, 3, fleet.PhaseDeleted); len(removed) != 0 {
		t.Fatalf("another tenant forgot our app: %+v", removed)
	}
	if removed := report(org, c.ID, mine, 2, fleet.PhaseDeleted); len(removed) != 0 {
		t.Fatalf("an older revision forgot the retired app: %+v", removed)
	}
	if s, err := repo.FindAppStatus(ctx, org, environment, "web"); err != nil || s.Phase != fleet.PhaseReady {
		t.Fatalf("a deleted report is never stored as a status: %+v %v", s, err)
	}
	if removed := report(org, c.ID, fleet.NewSession(), 3, fleet.PhaseDeleted); len(removed) != 0 {
		t.Fatalf("a stale tunnel forgot the app: %+v", removed)
	}
	removed := report(org, c.ID, mine, 3, fleet.PhaseDeleted)
	if len(removed) != 1 || removed[0].Name != "web" || removed[0].Revision != 3 || removed[0].Project != proj {
		t.Fatalf("removed %+v", removed)
	}
	notFound(t, "forgotten app", func() error { _, err := repo.FindApp(ctx, org, c.ID, environment, "web"); return err }())
	notFound(t, "forgotten status", func() error { _, err := repo.FindAppStatus(ctx, org, environment, "web"); return err }())

	// a later deployment of the name starts over
	app.Revision = 4
	if changed, err := repo.PutApp(ctx, app); err != nil || !changed {
		t.Fatalf("redeploy %v %v", changed, err)
	}
	if a, _ := repo.FindApp(ctx, org, c.ID, environment, "web"); a.Deleted || a.Revision != 4 {
		t.Fatalf("redeployed %+v", a)
	}
	// and a deployment after a retirement revives it in place
	if _, ok, _ := repo.RetireApp(ctx, fleet.Retirement{Organization: org, Project: proj, Environment: environment, Name: "web", Revision: 5}); !ok {
		t.Fatal("retire again")
	}
	app.Revision = 6
	if changed, err := repo.PutApp(ctx, app); err != nil || !changed {
		t.Fatalf("revive %v %v", changed, err)
	}
	if a, _ := repo.FindApp(ctx, org, c.ID, environment, "web"); a.Deleted || a.Revision != 6 || a.Image != app.Image {
		t.Fatalf("revived %+v", a)
	}
	if removed := report(org, c.ID, mine, 6, fleet.PhaseDeleted); len(removed) != 0 {
		t.Fatalf("a running app was forgotten: %+v", removed)
	}
}

// A deployment reviving a retired app while its "deleted" report is being
// stored: the report waits for the revival and then forgets nothing, neither
// the desired app nor its status.
func TestRetiredAppRevivedWhileForgotten(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org := identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")
	session := fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, session, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	proj, environment := project.NewID(), project.NewEnvironmentID()
	if _, err := repo.Place(ctx, org, proj, environment, c.ID); err != nil {
		t.Fatal(err)
	}
	app := fleet.App{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "web", Revision: 2,
		Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1, Access: fleet.AccessPublic}
	if _, err := repo.PutApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	status := func(rev int64, phase fleet.Phase) fleet.Report {
		return fleet.Report{Apps: []fleet.AppStatus{{Environment: environment, Name: "web", ObservedRevision: rev, Phase: phase}}}
	}
	if _, _, err := repo.ObserveApps(ctx, org, c.ID, session, status(2, fleet.PhaseReady)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.RetireApp(ctx, fleet.Retirement{Organization: org, Project: proj, Environment: environment, Name: "web", Revision: 3}); err != nil || !ok {
		t.Fatalf("retire %v %v", ok, err)
	}

	// The revival, left uncommitted.
	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	app.Revision = 4
	if changed, err := repo.PutApp(txn.With(ctx, tx), app); err != nil || !changed {
		t.Fatalf("revive %v %v", changed, err)
	}
	type result struct {
		removed []fleet.App
		err     error
	}
	done := make(chan result, 1)
	go func() {
		_, removed, err := repo.ObserveApps(ctx, org, c.ID, session, status(3, fleet.PhaseDeleted))
		done <- result{removed, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the deleted report did not wait for the revival: %+v %v", r.removed, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || len(r.removed) != 0 {
		t.Fatalf("a revived app was forgotten: %+v %v", r.removed, r.err)
	}
	if a, err := repo.FindApp(ctx, org, c.ID, environment, "web"); err != nil || a.Deleted || a.Revision != 4 {
		t.Fatalf("revived %+v %v", a, err)
	}
	if s, err := repo.FindAppStatus(ctx, org, environment, "web"); err != nil || s.Phase != fleet.PhaseReady {
		t.Fatalf("the revived app lost its status: %+v %v", s, err)
	}
}
