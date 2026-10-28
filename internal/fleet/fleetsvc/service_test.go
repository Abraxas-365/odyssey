package fleetsvc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetpg"
	"github.com/Abraxas-365/odyssey/internal/fleet/fleetsvc"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
)

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type env struct {
	svc    *fleetsvc.Service
	events *eventtest.Recorder
	clock  *clock
	org    identity.OrganizationID
	user   identity.UserID
}

func setup(t *testing.T) *env {
	t.Helper()
	db := pgtest.DB(t, migrate.Set{Owner: fleetpg.Schema, Files: fleetpg.Migrations()})
	certPEM, keyPEM, err := fleetca.Generate("test", time.Now().Add(-time.Hour), 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := fleetca.Parse(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Now()}
	events := &eventtest.Recorder{}
	svc := fleetsvc.New(fleetpg.New(db), ca, txn.Postgres{DB: db}, events, fleetsvc.Options{Gateway: "gw:8443", Now: c.Now})
	return &env{svc: svc, events: events, clock: c, org: identity.NewOrganizationID(), user: identity.MustParseUserID(uuid.NewString())}
}

func csr(t *testing.T, key any) *x509.CertificateRequest {
	t.Helper()
	if key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// enrolled creates and enrols a cluster named name.
func (e *env) enrolled(t *testing.T, name string) fleet.AgentRef {
	t.Helper()
	ctx := context.Background()
	created, err := e.svc.Create(ctx, e.org, fleet.Create{Name: name, CreatedBy: e.user})
	if err != nil {
		t.Fatal(err)
	}
	en, err := e.svc.Enroll(ctx, created.Token.Value, csr(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	return fleet.AgentRef{Organization: e.org, Cluster: en.Cluster, Serial: en.Certificate.Serial}
}

func code(err error) string {
	if e, ok := err.(*errx.Error); ok {
		return e.Code
	}
	return ""
}

// A renewal whose answer is lost leaves the agent with the certificate it
// renewed from: it must still be able to renew with it (until the overlap
// ends), or the agent is bricked. A young certificate cannot be renewed
// (an agent looping on Renew must not flood the CA and the audit trail).
func TestRenewal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")

	if _, err := e.svc.Renew(ctx, agent, csr(t, nil)); code(err) != fleet.CodeRenewTooEarly {
		t.Fatalf("renewal of a fresh certificate: %v", err)
	}

	e.clock.add(fleet.RenewAfter + time.Minute)
	lost, err := e.svc.Renew(ctx, agent, csr(t, nil)) // the agent never receives it
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Authenticate(ctx, agent); err != nil {
		t.Fatalf("the certificate renewed from is refused within the overlap: %v", err)
	}
	// an immediate retry is refused (a looping agent), one later succeeds
	if _, err := e.svc.Renew(ctx, agent, csr(t, nil)); code(err) != fleet.CodeRenewTooEarly {
		t.Fatalf("immediate retry from the previous certificate: %v", err)
	}
	e.clock.add(fleet.RenewRetryAfter + time.Second)
	retried, err := e.svc.Renew(ctx, agent, csr(t, nil)) // the agent retries with what it holds
	if err != nil {
		t.Fatalf("retry after a lost answer: %v", err)
	}
	if retried.Serial == lost.Serial {
		t.Fatal("same serial issued twice")
	}
	current := fleet.AgentRef{Organization: agent.Organization, Cluster: agent.Cluster, Serial: retried.Serial}
	if err := e.svc.Authenticate(ctx, current); err != nil {
		t.Fatal(err)
	}
	// the lost one was never used: it is neither current nor previous
	if err := e.svc.Authenticate(ctx, fleet.AgentRef{Organization: agent.Organization, Cluster: agent.Cluster, Serial: lost.Serial}); code(err) != fleet.CodeRevoked {
		t.Fatalf("the lost certificate is accepted: %v", err)
	}
	// the renewed-from certificate stays accepted for the overlap (its end
	// is RenewalOverlap after the renewal; fleetpg's tests check it lapses)
	if err := e.svc.Authenticate(ctx, agent); err != nil {
		t.Fatalf("previous within the overlap: %v", err)
	}
	if got := len(e.events.Types()); got != 1+1+2 { // created, enrolled, 2 renewals
		t.Fatalf("events %v", e.events.Types())
	}
}

func TestCSRKeys(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, err := e.svc.Create(ctx, e.org, fleet.Create{Name: "lima", CreatedBy: e.user})
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]any{"rsa": rsaKey, "p384": p384} {
		if _, err := e.svc.Enroll(ctx, created.Token.Value, csr(t, key)); !errx.IsType(err, errx.TypeValidation) {
			t.Fatalf("%s key accepted: %v", name, err)
		}
	}
	if _, err := e.svc.Enroll(ctx, created.Token.Value, csr(t, nil)); err != nil {
		t.Fatalf("a refused CSR spent the token: %v", err)
	}
}

// Revoking is final, and nothing moves on its own: a deployment to an
// environment of a revoked cluster fails (telling TI to move its apps) until
// TI moves them, even with another cluster connected.
func TestApplyStaysOffARevokedCluster(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	first := e.enrolled(t, "lima-1")
	app := fleet.App{Organization: e.org, Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "web", Revision: 1,
		Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1}
	if err := e.svc.Apply(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	second := e.enrolled(t, "lima-2")
	app.Revision = 2
	if err := e.svc.Apply(ctx, app); code(err) != fleet.CodeClusterRevoked {
		t.Fatalf("apply on a revoked cluster: %v", err)
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, second, app.Environment, "web"); ok {
		t.Fatal("the app moved on its own")
	}
	if _, err := e.svc.Move(ctx, e.org, first.Cluster, fleet.Move{To: second.Cluster, MovedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Apply(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := e.svc.DesiredApp(ctx, second, app.Environment, "web"); !ok || err != nil || got.Revision != 2 {
		t.Fatalf("the moved app was not updated: %+v %v %v", got, ok, err)
	}
	// a new environment goes to the default (active) cluster
	other := app
	other.Environment, other.Revision = project.NewEnvironmentID(), 1
	if err := e.svc.Apply(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, second, other.Environment, "web"); !ok {
		t.Fatal("a new environment was not placed on the active cluster")
	}
}

// A deployment racing a move never stores its app for the cluster the
// environment just left: it waits for the move, then retries (or lands on
// the new cluster), and every app ends up where its environment is.
func TestApplyRacingAMove(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	first := e.enrolled(t, "lima-1")
	environment := project.NewEnvironmentID()
	app := func(rev int64) fleet.App {
		return fleet.App{Organization: e.org, Project: project.NewID(), Environment: environment, Name: "web", Revision: rev,
			Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1}
	}
	if err := e.svc.Apply(ctx, app(1)); err != nil {
		t.Fatal(err)
	}
	second := e.enrolled(t, "lima-2")
	to := second.Cluster

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &to, RevokedBy: e.user})
	}()
	for rev := int64(2); rev < 12; rev++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for { // the workflow retries a PLACEMENT_MOVED
				if err := e.svc.Apply(ctx, app(rev)); code(err) != fleet.CodePlacementMoved {
					errs <- err // CLUSTER_REVOKED would mean it saw the revocation without the move
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, first, environment, "web"); ok {
		t.Fatal("an app was stored for the cluster its environment left")
	}
	got, ok, err := e.svc.DesiredApp(ctx, second, environment, "web")
	if !ok || err != nil || got.Revision != 11 {
		t.Fatalf("on the new cluster %+v %v %v", got, ok, err)
	}
}

// Two revocations that move to each other do not deadlock: one wins, the
// other finds its target revoked.
func TestCrossedRevokesDoNotDeadlock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a, b := e.enrolled(t, "lima-1").Cluster, e.enrolled(t, "lima-2").Cluster
	for range 5 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, pair := range [][2]fleet.ClusterID{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				to := pair[1]
				errs[i] = e.svc.Revoke(ctx, e.org, pair[0], fleet.Revoke{MoveTo: &to, RevokedBy: e.user})
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if code(err) != fleet.CodeInvalidMoveTarget {
				t.Fatalf("crossed revoke: %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d crossed revocations won; want 1 (%v)", ok, errs)
		}
		a, b = e.enrolled(t, "lima-a"+uuid.NewString()[:4]).Cluster, e.enrolled(t, "lima-b"+uuid.NewString()[:4]).Cluster
	}
}

// TI moves the apps of a revoked cluster explicitly: at revocation or after,
// only to another active cluster of its organization, recorded once.
func TestMoveAppsOffARevokedCluster(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	first := e.enrolled(t, "lima-1")
	envs := []project.EnvironmentID{project.NewEnvironmentID(), project.NewEnvironmentID()}
	for i, name := range []string{"web", "api", "jobs"} {
		if err := e.svc.Apply(ctx, fleet.App{Organization: e.org, Project: project.NewID(), Environment: envs[i%2], Name: name, Revision: 1,
			Image: "r.local/x@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1}); err != nil {
			t.Fatal(err)
		}
	}
	second := e.enrolled(t, "lima-2")
	if c, _ := e.svc.Find(ctx, e.org, first.Cluster); c.Apps != 3 {
		t.Fatalf("apps on the cluster: %d", c.Apps)
	}
	move := fleet.Move{To: second.Cluster, MovedBy: e.user}
	if _, err := e.svc.Move(ctx, e.org, first.Cluster, move); code(err) != fleet.CodeClusterNotRevoked {
		t.Fatalf("move off an active cluster: %v", err)
	}
	self := first.Cluster
	if err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &self, RevokedBy: e.user}); code(err) != fleet.CodeInvalidMoveTarget {
		t.Fatalf("revoke moving onto itself: %v", err)
	}
	if c, _ := e.svc.Find(ctx, e.org, first.Cluster); c.Status != fleet.StatusActive {
		t.Fatal("a refused move still revoked the cluster")
	}
	theirs := fleet.NewClusterID()
	if err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &theirs, RevokedBy: e.user}); code(err) != fleet.CodeInvalidMoveTarget {
		t.Fatalf("revoke moving to an unknown cluster: %v", err)
	}

	e.events.Reset()
	to := second.Cluster
	if err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &to, RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if got := e.events.Types(); len(got) != 2 || got[0] != fleet.EventRevoked || got[1] != fleet.EventAppsMoved {
		t.Fatalf("events %v", got)
	}
	for _, name := range []string{"web", "jobs"} {
		if _, ok, _ := e.svc.DesiredApp(ctx, second, envs[0], name); !ok {
			t.Fatalf("%s did not move", name)
		}
	}
	if c, _ := e.svc.Find(ctx, e.org, first.Cluster); c.Apps != 0 || c.Status != fleet.StatusRevoked {
		t.Fatalf("revoked cluster %+v", c)
	}

	// moving again (already empty) changes and records nothing
	e.events.Reset()
	if moved, err := e.svc.Move(ctx, e.org, first.Cluster, move); err != nil || moved.Apps != 0 || len(e.events.Types()) != 0 {
		t.Fatalf("second move %+v %v %v", moved, err, e.events.Types())
	}
	// nor to a revoked cluster
	if err := e.svc.Revoke(ctx, e.org, second.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Move(ctx, e.org, second.Cluster, fleet.Move{To: first.Cluster, MovedBy: e.user}); code(err) != fleet.CodeInvalidMoveTarget {
		t.Fatalf("move to a revoked cluster: %v", err)
	}
}

// A report keeps its valid statuses when some are invalid, and a bogus
// revision in a batch does not hide the real one for the same app.
func TestReportKeepsTheValidStatuses(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := fleet.NewSession()
	if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1"}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	environment := project.NewEnvironmentID()
	for _, name := range []string{"web", "api"} {
		if err := e.svc.Apply(ctx, fleet.App{Organization: e.org, Project: project.NewID(), Environment: environment, Name: name, Revision: 3,
			Image: "r.local/x@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1}); err != nil {
			t.Fatal(err)
		}
	}
	err := e.svc.Report(ctx, agent, session, fleet.Report{Apps: []fleet.AppStatus{
		{Environment: environment, Name: "web", ObservedRevision: 99999, Phase: fleet.PhaseReady}, // never desired
		{Environment: environment, Name: "web", ObservedRevision: 3, Phase: fleet.PhaseReady},
		{Environment: environment, Name: "api", ObservedRevision: 3, Phase: fleet.PhaseReady, URL: "javascript:alert(1)"},
	}})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("a report with an invalid status: %v", err)
	}
	if s, _ := e.svc.Status(ctx, e.org, environment, "web"); s.ObservedRevision != 3 || s.Phase != fleet.PhaseReady {
		t.Fatalf("web status %+v (the valid status was lost)", s)
	}
	if s, _ := e.svc.Status(ctx, e.org, environment, "api"); s.ObservedRevision != 0 || s.URL != "" {
		t.Fatalf("api status %+v (the invalid status was stored)", s)
	}
}

// An agent whose Enroll or Renew answer was lost gets the certificate back
// by proving it holds the key; nobody else learns anything, nothing is
// signed or recorded.
func TestRecover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, err := e.svc.Create(ctx, e.org, fleet.Create{Name: "lima", CreatedBy: e.user})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	byToken := fleet.Recover{Token: created.Token.Value}
	if _, err := e.svc.Recover(ctx, byToken, csr(t, key)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover before enrolling: %v", err)
	}
	en, err := e.svc.Enroll(ctx, created.Token.Value, csr(t, key)) // the answer is "lost"
	if err != nil {
		t.Fatal(err)
	}
	before := len(e.events.Events())
	got, err := e.svc.Recover(ctx, byToken, csr(t, key)) // the spent token still names the cluster
	if err != nil || string(got.Certificate.PEM) != string(en.Certificate.PEM) || got.Cluster != en.Cluster || len(got.Certificate.CAPEM) == 0 {
		t.Fatalf("recover the enrolment: %+v, %v", got, err)
	}
	if _, err := e.svc.Recover(ctx, byToken, csr(t, nil)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover with another key: %v", err)
	}

	// a renewal whose answer is lost, recovered by ids after the overlap
	e.clock.add(fleet.RenewAfter + time.Second)
	next, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	agent := fleet.AgentRef{Organization: e.org, Cluster: en.Cluster, Serial: en.Certificate.Serial}
	renewed, err := e.svc.Renew(ctx, agent, csr(t, next))
	if err != nil {
		t.Fatal(err)
	}
	e.clock.add(2 * fleet.RenewalOverlap)
	byIDs := fleet.Recover{Organization: e.org.String(), Cluster: en.Cluster.String()}
	got, err = e.svc.Recover(ctx, byIDs, csr(t, next))
	if err != nil || got.Certificate.Serial != renewed.Serial {
		t.Fatalf("recover the renewal: %+v, %v", got, err)
	}
	if _, err := e.svc.Recover(ctx, byIDs, csr(t, key)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover with the replaced key: %v", err)
	}
	other := fleet.Recover{Organization: identity.NewOrganizationID().String(), Cluster: en.Cluster.String()}
	if _, err := e.svc.Recover(ctx, other, csr(t, next)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover through another organization: %v", err)
	}
	if n := len(e.events.Events()) - before; n != 1 { // the renewal only
		t.Fatalf("%d events; recovering must record nothing", n)
	}

	// expired or revoked: nothing to recover
	e.clock.add(fleet.CertificateTTL)
	if _, err := e.svc.Recover(ctx, byIDs, csr(t, next)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover an expired certificate: %v", err)
	}
	if err := e.svc.Revoke(ctx, e.org, en.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Recover(ctx, byIDs, csr(t, next)); code(err) != fleet.CodeNothingToRecover {
		t.Fatalf("recover a revoked cluster: %v", err)
	}
	if _, err := e.svc.Recover(ctx, fleet.Recover{Organization: "x"}, csr(t, next)); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("recover without a cluster: %v", err)
	}
}
