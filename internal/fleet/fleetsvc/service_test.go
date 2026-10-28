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

// Revoking is final, but it must not strand the environments placed there:
// their next deployment moves them to the organization's default cluster.
func TestApplyMovesOffARevokedCluster(t *testing.T) {
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
	app.Revision = 2
	if err := e.svc.Apply(ctx, app); code(err) != fleet.CodeClusterRevoked {
		t.Fatalf("apply with no other cluster: %v", err)
	}
	second := e.enrolled(t, "lima-2")
	if err := e.svc.Apply(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.svc.DesiredApp(ctx, second, app.Environment, "web"); !ok || err != nil {
		t.Fatalf("the app did not move to the new cluster: %v %v", ok, err)
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, first, app.Environment, "web"); ok {
		t.Fatal("the revoked cluster still has the app")
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
