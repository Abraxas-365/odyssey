package fleetpg_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetpg"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type env struct {
	t    *testing.T
	ctx  context.Context
	db   *sqlx.DB
	repo *fleetpg.Repository
	user identity.UserID
}

func setup(t *testing.T) *env {
	db := pgtest.DB(t, migrate.Set{Owner: fleetpg.Schema, Files: fleetpg.Migrations()})
	return &env{t: t, ctx: context.Background(), db: db, repo: fleetpg.New(db), user: identity.MustParseUserID(uuid.NewString())}
}

// create registers a pending cluster whose token is "tok-<name>".
func (e *env) create(org identity.OrganizationID, name string) fleet.Cluster {
	e.t.Helper()
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	c := fleet.Cluster{ID: fleet.NewClusterID(), Organization: org, Name: name, Status: fleet.StatusPending,
		CreatedBy: e.user, TokenExpiresAt: &exp}
	if err := e.repo.CreateCluster(e.ctx, c, []byte("tok-"+name)); err != nil {
		e.t.Fatal(err)
	}
	return c
}

// enrol spends the cluster's token and gives it a certificate.
func (e *env) enrol(c fleet.Cluster, serial string) {
	e.t.Helper()
	if _, err := e.repo.ConsumeToken(e.ctx, c.Organization, c.ID, []byte("tok-"+c.Name)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.repo.SetCertificate(e.ctx, c.Organization, c.ID, fleet.Issued{Serial: serial, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		e.t.Fatal(err)
	}
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("%s: want NotFound, got %v", what, err)
	}
}

func TestClusters(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()

	c := e.create(org, "Lima-1")
	got, err := repo.FindCluster(ctx, org, c.ID)
	if err != nil || got.Status != fleet.StatusPending || got.TokenExpiresAt == nil || got.Connected || got.Certificate.Serial != "" {
		t.Fatalf("find %+v %v", got, err)
	}
	_, err = repo.FindCluster(ctx, other, c.ID)
	notFound(t, "cross-tenant find", err)

	// names are unique per organization (case-insensitive), not across them
	exp := time.Now().Add(time.Hour)
	dup := fleet.Cluster{ID: fleet.NewClusterID(), Organization: org, Name: "lima-1", Status: fleet.StatusPending, CreatedBy: e.user, TokenExpiresAt: &exp}
	err = repo.CreateCluster(ctx, dup, []byte("x"))
	if e, ok := err.(*errx.Error); !ok || e.Type != errx.TypeConflict || e.Details["field"] != "name" {
		t.Fatalf("duplicate name: %v", err)
	}
	e.create(other, "lima-1")

	// a token is spent once, only by its cluster's organization, only unexpired
	if _, err := repo.ConsumeToken(ctx, other, c.ID, []byte("tok-Lima-1")); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant token: %v", err)
	}
	if _, err := repo.ConsumeToken(ctx, org, c.ID, []byte("wrong")); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	if got, err := repo.ConsumeToken(ctx, org, c.ID, []byte("tok-Lima-1")); err != nil || got.TokenExpiresAt != nil {
		t.Fatalf("consume %+v %v", got, err)
	}
	_, err = repo.ConsumeToken(ctx, org, c.ID, []byte("tok-Lima-1"))
	notFound(t, "token reuse", err)
	if err := repo.SetToken(ctx, org, c.ID, []byte("late"), time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = repo.ConsumeToken(ctx, org, c.ID, []byte("late"))
	notFound(t, "expired token", err)
	notFound(t, "cross-tenant set token", repo.SetToken(ctx, other, c.ID, []byte("x"), time.Now().Add(time.Hour)))

	// enrolment activates it; renewal keeps the presented serial for the overlap
	notFound(t, "renew before enrolment", repo.RenewCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s0", ExpiresAt: time.Now().Add(time.Hour)}, "", time.Now().Add(time.Hour)))
	notFound(t, "cross-tenant enrolment", repo.SetCertificate(ctx, other, c.ID, fleet.Issued{Serial: "s0", ExpiresAt: time.Now().Add(time.Hour)}))
	if err := repo.SetCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if serials, err := repo.FindAcceptedSerials(ctx, org, c.ID); err != nil || len(serials) != 1 || serials[0] != "s1" {
		t.Fatalf("serials after enrolment %v %v", serials, err)
	}
	if err := repo.RenewCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s2", ExpiresAt: time.Now().Add(2 * time.Hour)}, "s1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if serials, err := repo.FindAcceptedSerials(ctx, org, c.ID); err != nil || len(serials) != 2 || serials[0] != "s2" || serials[1] != "s1" {
		t.Fatalf("serials after renewal %v %v", serials, err)
	}
	// a retry with the previous certificate (the answer was lost) keeps it
	// previous, never extending its overlap
	if err := repo.RenewCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s2b", ExpiresAt: time.Now().Add(2 * time.Hour)}, "s1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var until time.Time
	if err := e.db.Get(&until, `SELECT previous_expires_at FROM fleet.clusters WHERE id = $1`, c.ID); err != nil || time.Until(until) > 2*time.Minute {
		t.Fatalf("overlap extended by a retry: %v %v", until, err)
	}
	if serials, _ := repo.FindAcceptedSerials(ctx, org, c.ID); len(serials) != 2 || serials[0] != "s2b" || serials[1] != "s1" {
		t.Fatalf("serials after retried renewal %v", serials)
	}
	// the overlap ends: only the current one is left
	if err := repo.RenewCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s2c", ExpiresAt: time.Now().Add(2 * time.Hour)}, "s2b", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if serials, _ := repo.FindAcceptedSerials(ctx, org, c.ID); len(serials) != 1 || serials[0] != "s2c" {
		t.Fatalf("serials after the overlap %v", serials)
	}
	_, err = repo.FindAcceptedSerials(ctx, other, c.ID)
	notFound(t, "cross-tenant serials", err)
	// a second enrolment (lost key) drops every earlier serial
	if err := repo.SetCertificate(ctx, org, c.ID, fleet.Issued{Serial: "s3", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if serials, _ := repo.FindAcceptedSerials(ctx, org, c.ID); len(serials) != 1 || serials[0] != "s3" {
		t.Fatalf("serials after re-enrolment %v", serials)
	}

	// sessions: connected while the current session beats; a stale one is ignored
	s1, s2 := fleet.NewSession(), fleet.NewSession()
	info := fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{KubernetesVersion: "v1.31.0", APIs: []string{"networking.k8s.io/v1", fleet.CapabilityAccess}}}
	notFound(t, "cross-tenant session", repo.SetSession(ctx, other, c.ID, s1, info))
	if err := repo.SetSession(ctx, org, c.ID, s1, info); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindCluster(ctx, org, c.ID); !got.Connected || got.Agent.Version != "v1" || got.Agent.Capabilities.KubernetesVersion != "v1.31.0" || !got.Agent.Capabilities.CanProtect() || got.LastSeenAt == nil {
		t.Fatalf("connected %+v", got)
	}
	if ok, err := repo.ObserveHeartbeat(ctx, org, c.ID, s1, "s3"); !ok || err != nil {
		t.Fatalf("heartbeat %v %v", ok, err)
	}
	if ok, _ := repo.ObserveHeartbeat(ctx, org, c.ID, s1, "s1"); ok {
		t.Fatal("heartbeat of a certificate no longer accepted counted")
	}
	if ok, _ := repo.ObserveHeartbeat(ctx, other, c.ID, s1, "s3"); ok {
		t.Fatal("cross-tenant heartbeat counted")
	}
	if err := repo.SetSession(ctx, org, c.ID, s2, info); err != nil { // a newer tunnel supersedes
		t.Fatal(err)
	}
	if ok, _ := repo.ObserveHeartbeat(ctx, org, c.ID, s1, "s3"); ok {
		t.Fatal("superseded session's heartbeat counted")
	}
	if err := repo.ObserveDisconnect(ctx, org, c.ID, s1); err != nil { // the old tunnel closing changes nothing
		t.Fatal(err)
	}
	if got, _ := repo.FindCluster(ctx, org, c.ID); !got.Connected {
		t.Fatal("old session's disconnect dropped the new one")
	}
	if err := repo.ObserveDisconnect(ctx, other, c.ID, s2); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindCluster(ctx, org, c.ID); !got.Connected {
		t.Fatal("cross-tenant disconnect")
	}
	if err := repo.ObserveDisconnect(ctx, org, c.ID, s2); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindCluster(ctx, org, c.ID); got.Connected {
		t.Fatal("still connected after disconnect")
	}

	// default cluster: the first enrolled active one
	second := e.create(org, "lima-2")
	e.enrol(second, "t1")
	if d, err := repo.FindDefaultCluster(ctx, org); err != nil || d.ID != c.ID {
		t.Fatalf("default %v %v", d.ID, err)
	}
	_, err = repo.FindDefaultCluster(ctx, identity.NewOrganizationID())
	notFound(t, "default without clusters", err)

	// list: by name, revoked last; filter; tenant
	notFound(t, "cross-tenant revoke", repo.Revoke(ctx, other, c.ID, e.user))
	if err := repo.Revoke(ctx, org, c.ID, e.user); err != nil {
		t.Fatal(err)
	}
	notFound(t, "revoke twice", repo.Revoke(ctx, org, c.ID, e.user))
	if got, _ := repo.FindCluster(ctx, org, c.ID); got.Status != fleet.StatusRevoked || got.RevokedAt == nil || got.Connected {
		t.Fatalf("revoked %+v", got)
	}
	_, err = repo.FindAcceptedSerials(ctx, org, c.ID)
	notFound(t, "serials of a revoked cluster", err)
	notFound(t, "token for a revoked cluster", repo.SetToken(ctx, org, c.ID, []byte("x"), time.Now().Add(time.Hour)))
	if d, _ := repo.FindDefaultCluster(ctx, org); d.ID != second.ID {
		t.Fatalf("default after revoke %v", d.ID)
	}
	e.create(org, "lima-1") // a revoked cluster's name is free again
	page, err := repo.ListClusters(ctx, org, fleet.Filter{}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 3 || page.Items[0].Name != "lima-1" || page.Items[1].Name != "lima-2" || page.Items[2].ID != c.ID {
		t.Fatalf("list %+v %v", page, err)
	}
	page, err = repo.ListClusters(ctx, org, fleet.Filter{Status: []fleet.Status{fleet.StatusActive}}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("active %+v %v", page, err)
	}
	if page, _ := repo.ListClusters(ctx, other, fleet.Filter{}, query.Pagination{Limit: 10}); page.Page.Total != 1 {
		t.Fatalf("other tenant sees %d", page.Page.Total)
	}

	// FindClusterForUpdate locks inside a transaction
	err = txn.Postgres{DB: e.db}.Do(ctx, func(ctx context.Context) error {
		_, err := repo.FindClusterForUpdate(ctx, org, second.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDesiredApps(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c, theirs := e.create(org, "lima"), e.create(other, "lima")
	e.enrol(c, "s1")
	e.enrol(theirs, "s2")
	proj, environment := project.NewID(), project.NewEnvironmentID()

	_, err := repo.FindPlacement(ctx, org, environment)
	notFound(t, "placement before the first app", err)
	if got, err := repo.Place(ctx, org, proj, environment, c.ID); err != nil || got != c.ID {
		t.Fatalf("place %v %v", got, err)
	}
	if got, err := repo.Place(ctx, org, proj, environment, theirs.ID); err != nil || got != c.ID { // the first placement wins
		t.Fatalf("second place %v %v", got, err)
	}
	_, err = repo.FindPlacement(ctx, other, environment)
	notFound(t, "cross-tenant placement", err)

	app := fleet.App{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "web", Revision: 2,
		Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1,
		Env: map[string]string{"TZ": "America/Lima"}, Labels: map[string]string{"team": "ventas"}, Access: fleet.AccessTeam}
	if changed, err := repo.PutApp(ctx, app); err != nil || !changed {
		t.Fatalf("put %v %v", changed, err)
	}
	older := app
	older.Revision, older.Image = 1, "r.local/web@sha256:old"
	if changed, err := repo.PutApp(ctx, older); err != nil || changed {
		t.Fatalf("older revision replaced the newer: %v %v", changed, err)
	}
	if changed, _ := repo.PutApp(ctx, app); changed {
		t.Fatal("same revision counted as a change")
	}
	hijack := app
	hijack.Organization, hijack.Cluster, hijack.Revision = other, theirs.ID, 9
	if changed, _ := repo.PutApp(ctx, hijack); changed {
		t.Fatal("another tenant overwrote the app")
	}
	got, err := repo.FindApp(ctx, org, c.ID, environment, "web")
	if err != nil || got.Revision != 2 || got.Image != app.Image || got.Env["TZ"] != "America/Lima" || got.Labels["team"] != "ventas" || got.Port != 8080 || got.Access != fleet.AccessTeam {
		t.Fatalf("find app %+v %v", got, err)
	}
	// an app of several services keeps them in its spec, with the settings'
	// services and each service's own env
	shop := fleet.App{Organization: org, Project: proj, Environment: environment, Cluster: c.ID, Name: "shop", Revision: 1,
		Services: []fleet.AppService{{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: "r.local/web@sha256:00", Port: 3000, HealthPath: "/", Replicas: 1},
			{Name: "worker", Kind: fleet.ServiceWorker, Image: "r.local/w@sha256:00", Replicas: 2, Env: map[string]string{"QUEUE": "jobs"}}},
		Resources: []fleet.ResourceRef{{Name: "main", Kind: "postgres", Services: []string{"worker"}}}}
	if changed, err := repo.PutApp(ctx, shop); err != nil || !changed {
		t.Fatalf("put shop %v %v", changed, err)
	}
	if got, err := repo.FindApp(ctx, org, c.ID, environment, "shop"); err != nil || len(got.Services) != 2 || !reflect.DeepEqual(got.Services[1], shop.Services[1]) ||
		!reflect.DeepEqual(got.Resources, shop.Resources) {
		t.Fatalf("find shop %+v %v", got, err)
	}
	_, err = repo.FindApp(ctx, other, theirs.ID, environment, "shop")
	notFound(t, "cross-tenant services", err)
	if _, err := e.db.Exec(`DELETE FROM fleet.desired_apps WHERE name = 'shop'`); err != nil {
		t.Fatal(err)
	}
	_, err = repo.FindApp(ctx, other, theirs.ID, environment, "web")
	notFound(t, "cross-tenant app", err)
	if apps, err := repo.ListApps(ctx, org, c.ID); err != nil || len(apps) != 1 {
		t.Fatalf("list apps %v %v", apps, err)
	}
	if apps, _ := repo.ListApps(ctx, other, theirs.ID); len(apps) != 0 {
		t.Fatalf("other tenant's cluster lists %v", apps)
	}

	// reports: only from the cluster's current tunnel, only for apps desired
	// on the reporting cluster, never beyond the desired revision, never
	// backwards
	mine, theirSession := fleet.NewSession(), fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, mine, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSession(ctx, other, theirs.ID, theirSession, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	report := func(org identity.OrganizationID, cluster fleet.ClusterID, session fleet.Session, rev int64, phase fleet.Phase) bool {
		t.Helper()
		live, _, err := repo.ObserveApps(ctx, org, cluster, session, fleet.Report{Apps: []fleet.AppStatus{{Environment: environment, Name: "web",
			ObservedRevision: rev, Phase: phase, ReadyReplicas: 1, URL: "http://web.apps.local"}}})
		if err != nil {
			t.Fatal(err)
		}
		return live
	}
	_, err = repo.FindAppStatus(ctx, org, environment, "web")
	notFound(t, "status before a report", err)
	report(other, theirs.ID, theirSession, 2, fleet.PhaseFailed) // another tenant's agent reporting our app
	_, err = repo.FindAppStatus(ctx, org, environment, "web")
	notFound(t, "status written by another tenant", err)
	if report(org, c.ID, fleet.NewSession(), 2, fleet.PhaseFailed) {
		t.Fatal("a stale tunnel's report counted as live")
	}
	_, err = repo.FindAppStatus(ctx, org, environment, "web")
	notFound(t, "status written by a stale tunnel", err)
	report(org, c.ID, mine, 99, fleet.PhaseFailed) // beyond the desired revision: would block every real report
	_, err = repo.FindAppStatus(ctx, org, environment, "web")
	notFound(t, "status of a revision never desired", err)
	if !report(org, c.ID, mine, 2, fleet.PhaseReady) {
		t.Fatal("the live tunnel's report was refused")
	}
	report(org, c.ID, mine, 1, fleet.PhaseFailed)
	s, err := repo.FindAppStatus(ctx, org, environment, "web")
	if err != nil || s.ObservedRevision != 2 || s.Phase != fleet.PhaseReady || s.URL != "http://web.apps.local" || s.Services == nil || len(s.Services) != 0 {
		t.Fatalf("status %+v %v", s, err)
	}
	// each service's part is kept with the app's
	svcs := []fleet.ServiceStatus{{Name: "web", Phase: fleet.PhaseReady, ReadyReplicas: 1}, {Name: "worker", Phase: fleet.PhaseFailed, Reason: fleet.ReasonCrash, Message: "restarted"}}
	if _, _, err := repo.ObserveApps(ctx, org, c.ID, mine, fleet.Report{Apps: []fleet.AppStatus{{Environment: environment, Name: "web",
		ObservedRevision: 2, Phase: fleet.PhaseFailed, Reason: fleet.ReasonCrash, URL: "http://web.apps.local", Services: svcs}}}); err != nil {
		t.Fatal(err)
	}
	if s, err := repo.FindAppStatus(ctx, org, environment, "web"); err != nil || len(s.Services) != 2 || s.Services[1] != svcs[1] {
		t.Fatalf("service status %+v %v", s, err)
	}
	report(org, c.ID, mine, 2, fleet.PhaseReady) // back to an agent without services
	if s, _ := repo.FindAppStatus(ctx, org, environment, "web"); len(s.Services) != 0 {
		t.Fatalf("stale service status %+v", s.Services)
	}
	_, err = repo.FindAppStatus(ctx, other, environment, "web")
	notFound(t, "cross-tenant status", err)

	// a revoked cluster's environment moves, apps and all, to another
	// (what the old cluster reported is gone; the apps are signalled there)
	next := e.create(org, "lima-b")
	e.enrol(next, "s9")
	_, err = repo.MovePlacement(ctx, other, environment, c.ID, next.ID)
	notFound(t, "cross-tenant move", err)
	if places, err := repo.ListPlacements(ctx, org, c.ID); err != nil || len(places) != 1 || places[0] != environment {
		t.Fatalf("placements before move %v %v", places, err)
	}
	if places, _ := repo.ListPlacements(ctx, other, c.ID); len(places) != 0 {
		t.Fatalf("cross-tenant placements %v", places)
	}
	if found, _ := repo.FindCluster(ctx, org, c.ID); found.Apps != 1 {
		t.Fatalf("apps on the cluster before move: %d", found.Apps)
	}
	// a value sealed to the old cluster's key waits there: the move drops it
	waiting := fleet.SecretDelivery{ID: fleet.NewSecretDeliveryID(), Organization: org, Project: project.NewID(), Environment: environment,
		Cluster: c.ID, App: "web", Name: "TOKEN", Version: 1, KeyID: "k3y", Sealed: make([]byte, 64), CreatedBy: e.user, CreatedAt: time.Now().UTC()}
	if ok, err := repo.PutSecretDelivery(ctx, waiting); err != nil || !ok {
		t.Fatalf("put delivery %v %v", ok, err)
	}
	if n, err := repo.MovePlacement(ctx, org, environment, c.ID, next.ID); err != nil || n != 1 {
		t.Fatalf("move %d %v", n, err)
	}
	if got, err := repo.ListSecretDeliveries(ctx, org, c.ID, time.Time{}); err != nil || len(got) != 0 {
		t.Fatalf("the old cluster's deliveries survived the move %+v %v", got, err)
	}
	if found, _ := repo.FindCluster(ctx, org, next.ID); found.Apps != 1 {
		t.Fatalf("apps on the new cluster after move: %d", found.Apps)
	}
	if got, err := repo.FindPlacement(ctx, org, environment); err != nil || got != next.ID {
		t.Fatalf("placement after move %v %v", got, err)
	}
	if apps, _ := repo.ListApps(ctx, org, next.ID); len(apps) != 1 || apps[0].Revision != 2 {
		t.Fatalf("apps after move %+v", apps)
	}
	if apps, _ := repo.ListApps(ctx, org, c.ID); len(apps) != 0 {
		t.Fatalf("old cluster still lists %+v", apps)
	}
	_, err = repo.FindAppStatus(ctx, org, environment, "web")
	notFound(t, "status from the old cluster after the move", err)

	// the schema itself refuses a row pointing at another tenant's cluster
	if _, err := e.db.Exec(`UPDATE fleet.placements SET cluster_id = $1 WHERE environment_id = $2`, theirs.ID, environment); err == nil {
		t.Fatal("a placement pointed at another tenant's cluster")
	}
}

func TestSignals(t *testing.T) {
	e := setup(t)
	var name string
	if err := e.db.Get(&name, `SELECT current_database()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	got := make(chan fleet.Signal, 8)
	done := make(chan error, 1)
	go func() {
		done <- fleetpg.NewSignals(pgtest.URL(t, name)).Listen(ctx, func(s fleet.Signal) { got <- s }, func() {})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	org := identity.NewOrganizationID()
	c := e.create(org, "lima")
	environment := project.NewEnvironmentID()
	// LISTEN is set up asynchronously: repeat the first write until heard
	var first fleet.Signal
	for i := int64(1); first.Name == ""; i++ {
		if i > 50 {
			t.Fatal("no app signal")
		}
		if i == 1 {
			e.enrol(c, "s1")
			if _, err := e.repo.Place(e.ctx, org, project.NewID(), environment, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.repo.PutApp(e.ctx, fleet.App{Organization: org, Project: project.NewID(), Environment: environment, Cluster: c.ID,
			Name: "web", Revision: i, Image: "img"}); err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if !s.Revoked && !s.Reenrolled {
				first = s
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if first.Organization != org || first.Cluster != c.ID || first.Environment != environment {
		t.Fatalf("app signal %+v", first)
	}
	if err := e.repo.Revoke(e.ctx, org, c.ID, e.user); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case s := <-got:
			if s.Revoked && s.Cluster == c.ID && s.Organization == org {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no revoked signal")
		}
	}
}

// The log cap counts every line at least minLine (so it bounds the rows
// too), and concurrent batches never store past it.
func TestBuildLogCap(t *testing.T) {
	e := setup(t)
	org := identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")
	session := fleet.NewSession()
	if err := e.repo.SetSession(e.ctx, org, c.ID, session, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	b := fleet.DesiredBuild{ID: fleet.MustParseBuildID(uuid.NewString()), Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID(),
		Cluster: c.ID, SourceKey: blob.TenantKey(org, "sources", "s.tar.gz"), SourceSHA256: strings.Repeat("a", 64), SourceSize: 1,
		ImageRepository: "zot:5000/o/p", CacheRef: "zot:5000/o/p:buildcache", Timeout: time.Minute}
	if _, err := e.repo.PutBuild(e.ctx, b); err != nil {
		t.Fatal(err)
	}
	const maxBytes, minLine = 1000, 100 // room for 10 lines, however short
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines := make([]fleet.LogLine, 5)
			for i := range lines {
				lines[i] = fleet.LogLine{Seq: int64(w*5 + i + 1), Stream: "build", Text: ""}
			}
			if live, err := e.repo.ObserveBuildLog(e.ctx, org, c.ID, session, fleet.BuildLog{Build: b.ID, Lines: lines}, maxBytes, minLine); err != nil || !live {
				t.Error(live, err)
			}
		}()
	}
	wg.Wait()
	page, err := e.repo.ListBuildLog(e.ctx, org, b.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 10 || !page.Truncated {
		t.Fatalf("%d lines stored, truncated %v; want 10 and the cut", len(page.Lines), page.Truncated)
	}
}
