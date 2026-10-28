package bootstrap_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetgrpc"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefleet"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// fakeCluster is the agent's view of a cluster, in memory: what tunnel
// applies and what it reports.
type fakeCluster struct {
	mu     sync.Mutex
	apps   map[string]*fleetv1.App // environment/name → newest applied
	status map[string]*fleetv1.AppStatus
	reject map[string]bool // names the "cluster" refuses
	notify func(environment, name string)
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{apps: map[string]*fleetv1.App{}, status: map[string]*fleetv1.AppStatus{}, reject: map[string]bool{}}
}

type rejection struct{ error }

func (f *fakeCluster) Apply(_ context.Context, a *fleetv1.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reject[a.GetName()] {
		return rejection{os.ErrPermission}
	}
	k := a.GetEnvironmentId() + "/" + a.GetName()
	if cur := f.apps[k]; cur == nil || cur.GetRevision() < a.GetRevision() {
		f.apps[k] = a
	}
	return nil
}

// observe is the app controller: the app at its revision is now in phase.
func (f *fakeCluster) observe(env, name, phase, url string) {
	f.mu.Lock()
	a := f.apps[env+"/"+name]
	f.status[env+"/"+name] = &fleetv1.AppStatus{EnvironmentId: env, Name: name, ObservedRevision: a.GetRevision(),
		Phase: phase, ReadyReplicas: 1, Url: url}
	notify := f.notify
	f.mu.Unlock()
	notify(env, name)
}

func (f *fakeCluster) applied(env, name string) *fleetv1.App {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps[env+"/"+name]
}

func (f *fakeCluster) Status(_ context.Context, env, name string) (*fleetv1.AppStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[env+"/"+name], nil
}

func (f *fakeCluster) Statuses(context.Context) ([]*fleetv1.AppStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*fleetv1.AppStatus{}
	for _, s := range f.status {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeCluster) Capabilities(context.Context) *fleetv1.Capabilities {
	return &fleetv1.Capabilities{KubernetesVersion: "v1.34.0+test", Apis: []string{"postgresql.cnpg.io"}, IngressClasses: []string{"traefik (default)"}}
}

// fleetHarness is a platform with a running gateway on localhost.
type fleetHarness struct {
	platform *bootstrap.Platform
	ca       *fleetca.Authority
	gateway  string
	cancel   context.CancelFunc
	done     chan error
}

func startFleet(t *testing.T) *fleetHarness {
	t.Helper()
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
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gw := "localhost:" + strings.TrimPrefix(lis.Addr().String(), "127.0.0.1:")
	p, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Authority: ca, Gateway: gw, Workflow: workflowpg.Options{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	var dbName string
	if err := db.Get(&dbName, `SELECT current_database()`); err != nil {
		t.Fatal(err)
	}
	gctx, cancel := context.WithCancel(ctx)
	h := &fleetHarness{platform: p, ca: ca, gateway: gw, cancel: cancel, done: make(chan error, 1)}
	go func() {
		h.done <- bootstrap.Gateway(gctx, p, ca, config.Gateway{DatabaseURL: pgtest.URL(t, dbName), Hosts: []string{"localhost"}}, lis,
			fleetgrpc.Options{HeartbeatEvery: 200 * time.Millisecond, ResyncEvery: time.Hour})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-h.done; err != nil {
			t.Errorf("gateway: %v", err)
		}
	})
	return h
}

func (h *fleetHarness) agent(t *testing.T, store tunnel.Store, cluster *fakeCluster) (stop func()) {
	t.Helper()
	c := tunnel.New(tunnel.Options{
		Store: store, Desired: cluster, Version: "test",
		IsRejected: func(err error) bool { _, ok := err.(rejection); return ok },
		Rejected: func(a *fleetv1.App, err error) *fleetv1.AppStatus {
			return &fleetv1.AppStatus{EnvironmentId: a.GetEnvironmentId(), Name: a.GetName(), ObservedRevision: a.GetRevision(),
				Phase: "failed", Reason: "config", Message: "refused: " + err.Error()}
		},
		Log: logr.Discard(), MinBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
	})
	cluster.mu.Lock()
	cluster.notify = c.Changed
	cluster.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Start(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestFleetTunnel is phase 1a-a3's protocol, end to end over real gRPC and
// mutual TLS: a cluster enrols with its one-time token, connects, receives
// desired state (pushed on change and resent after reconnecting), reports
// what it observes back to release, and is cut off when revoked.
func TestFleetTunnel(t *testing.T) {
	h := startFleet(t)
	ctx := context.Background()
	f := h.platform.Fleet
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())

	// No cluster yet: release learns it cannot apply.
	rf := releasefleet.New(f.Dispatch)
	target := release.Target{Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}
	app := func(rev int64) release.DesiredApp {
		return release.DesiredApp{Name: "portal", Revision: rev, Image: "r.test/p@sha256:" + strings.Repeat("a", 64),
			Port: 8080, HealthPath: "/health", Replicas: 1, Env: map[string]string{"TZ": "America/Lima"}}
	}
	if err := rf.Apply(ctx, target, app(1)); !strings.Contains(errMessage(err), "todavía no tiene un clúster") {
		t.Fatalf("apply without a cluster: %v", err)
	}

	created, err := f.Commands.Create(ctx, org, fleet.Create{Name: "lima-1", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	tok := created.Token
	if tok.Gateway != h.gateway || !strings.HasPrefix(tok.CAPin, "sha256:") {
		t.Fatalf("token %+v", tok)
	}

	// A wrong pin never reaches the gateway's Enroll (the agent refuses the TLS handshake).
	bad := tunnel.NewMemoryStore(tunnel.Enrollment{Token: tok.Value, Gateway: tok.Gateway, CAPin: "sha256:" + strings.Repeat("0", 64)})
	stopBad := h.agent(t, bad, newFakeCluster())
	time.Sleep(300 * time.Millisecond)
	stopBad()
	if bad.Identity().Cluster != "" {
		t.Fatal("enrolled through a gateway whose CA does not match the pin")
	}

	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: tok.Value, Gateway: tok.Gateway, CAPin: tok.CAPin})
	cluster := newFakeCluster()
	stop := h.agent(t, store, cluster)
	eventually(t, "the cluster to connect", func() bool {
		c, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Status == fleet.StatusActive && c.Connected
	})
	c, _ := f.Queries.Find(ctx, org, created.Cluster.ID)
	if c.Agent.Version != "test" || c.Agent.Capabilities.KubernetesVersion != "v1.34.0+test" || c.Certificate.Serial == "" || c.TokenExpiresAt != nil {
		t.Fatalf("connected cluster %+v", c)
	}
	id := store.Identity()
	if id.Cluster != created.Cluster.ID.String() || id.Organization != org.String() {
		t.Fatalf("identity %+v", id)
	}
	certOf := func(id tunnel.Identity) *x509.Certificate {
		b, _ := pem.Decode(id.CertPEM)
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	if u := certOf(id).URIs; len(u) != 1 || u[0].String() != fleet.AgentURI(org, created.Cluster.ID) {
		t.Fatalf("agent URI %v", u)
	}

	// The token is spent.
	if _, err := h.platform.Fleet.Agents.Enroll(ctx, tok.Value, csrFor(t)); err == nil {
		t.Fatal("the token enrolled twice")
	}

	// Desired state is pushed and the observed state comes back.
	if err := rf.Apply(ctx, target, app(1)); err != nil {
		t.Fatal(err)
	}
	env := target.Environment.String()
	eventually(t, "the app to reach the cluster", func() bool { return cluster.applied(env, "portal") != nil })
	if a := cluster.applied(env, "portal"); a.GetEnv()["TZ"] != "America/Lima" || a.GetOrganizationId() != org.String() || a.GetProjectId() != target.Project.String() {
		t.Fatalf("applied %+v", a)
	}
	cluster.observe(env, "portal", "ready", "http://portal.apps.test")
	eventually(t, "release to see it ready", func() bool {
		s, err := rf.Status(ctx, target, "portal")
		return err == nil && s.Phase == release.PhaseReady && s.ObservedRevision == 1 && s.URL == "http://portal.apps.test"
	})

	// Changes made while the tunnel is down arrive after reconnecting.
	stop()
	if err := rf.Apply(ctx, target, app(2)); err != nil {
		t.Fatal(err)
	}
	stop = h.agent(t, store, cluster)
	eventually(t, "revision 2 after reconnecting", func() bool { return cluster.applied(env, "portal").GetRevision() == 2 })

	// A refused app is reported failed, with the cluster's words.
	cluster.mu.Lock()
	cluster.reject["portal"] = true
	cluster.mu.Unlock()
	if err := rf.Apply(ctx, target, app(3)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the refusal to be reported", func() bool {
		s, err := rf.Status(ctx, target, "portal")
		return err == nil && s.Phase == release.PhaseFailed && s.Reason == release.FailureConfig && s.ObservedRevision == 3
	})

	// Another organization sees nothing of this cluster.
	if _, err := f.Queries.Find(ctx, identity.NewOrganizationID(), created.Cluster.ID); err == nil {
		t.Fatal("another organization found the cluster")
	}

	// Revoking closes the tunnel and refuses the certificate from then on.
	if err := f.Commands.Revoke(ctx, org, created.Cluster.ID, fleet.Revoke{RevokedBy: user}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the revoked cluster to drop", func() bool {
		c, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && !c.Connected && c.Status == fleet.StatusRevoked
	})
	stop()
	conn := dialAs(t, h, store.Identity())
	stream, err := fleetv1.NewFleetClient(conn).Connect(ctx)
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked certificate: %v", err)
	}
	if err := rf.Apply(ctx, target, app(4)); !strings.Contains(errMessage(err), "desconectado") {
		t.Fatalf("apply on a revoked cluster: %v", err)
	}
	list, err := f.Queries.List(ctx, org, fleet.Filter{}, query.Pagination{Limit: 10})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
}

// TestFleetReenrolmentReplacesTheCertificate: a cluster whose agent lost
// its key enrols again with a new token; the old certificate stops working.
// TestClustersOverHTTP drives the cluster routes through the real server and
// checks every response against api/openapi.yaml, including an agent
// enrolling between the calls.
func TestClustersOverHTTP(t *testing.T) {
	h := startFleet(t)
	app := bootstrap.NewServer(h.platform, config.Config{}, httpxtest.Auth())
	org := identity.NewOrganizationID()
	manage := httpxtest.Token(org, permission.ClustersRead, permission.ClustersManage)
	read := httpxtest.Token(org, permission.ClustersRead)

	res := checked(t, app, http.MethodPost, "/api/v1/clusters", manage, map[string]any{"name": "lima-1"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[fleet.Created](t, res)
	if res.Header.Get("Location") == "" || res.Header.Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(created.Token.Value, "odyssey-enroll-") || created.Token.Gateway != h.gateway {
		t.Fatalf("created %+v, headers %v", created, res.Header)
	}
	id := created.Cluster.ID.String()
	res = checked(t, app, http.MethodPost, "/api/v1/clusters", manage, map[string]any{"name": "lima-1"})
	if httpxtest.Want(t, res, http.StatusConflict); httpxtest.ErrorField(res) != "name" {
		t.Fatalf("name conflict without its field: %s", res.Body)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/clusters", manage, map[string]any{"name": ""}), http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/clusters", read, map[string]any{"name": "x"}), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/clusters/"+id, httpxtest.Token(identity.NewOrganizationID(), permission.ClustersRead), nil), http.StatusNotFound)

	// a new token replaces the first; the agent enrols with it and connects
	res = checked(t, app, http.MethodPost, "/api/v1/clusters/"+id+"/enrollment-token", manage, nil)
	httpxtest.Want(t, res, http.StatusOK)
	tok := httpxtest.JSON[fleet.Token](t, res)
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: tok.Value, Gateway: tok.Gateway, CAPin: tok.CAPin})
	h.agent(t, store, newFakeCluster())
	eventually(t, "the agent to connect", func() bool {
		c, err := h.platform.Fleet.Queries.Find(context.Background(), org, created.Cluster.ID)
		return err == nil && c.Connected
	})
	res = checked(t, app, http.MethodGet, "/api/v1/clusters/"+id, read, nil)
	if c := httpxtest.JSON[fleet.Cluster](t, res); c.Status != fleet.StatusActive || c.Certificate.Serial == "" || c.Agent.Version != "test" {
		t.Fatalf("enrolled %+v", c)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/clusters?status=active", read, nil)
	if page := httpxtest.JSON[query.Paginated[fleet.Cluster]](t, res); page.Page.Total != 1 {
		t.Fatalf("list %+v", page)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/clusters?status=nope", read, nil), http.StatusBadRequest)

	res = checked(t, app, http.MethodPost, "/api/v1/clusters/"+id+"/revoke", manage, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if c := httpxtest.JSON[fleet.Cluster](t, res); c.Status != fleet.StatusRevoked || c.Connected {
		t.Fatalf("revoked %+v", c)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/clusters/"+id+"/revoke", manage, nil), http.StatusOK)
	res = checked(t, app, http.MethodPost, "/api/v1/clusters/"+id+"/enrollment-token", manage, nil)
	if httpxtest.Want(t, res, http.StatusUnprocessableEntity); httpxtest.ErrorCode(res) != fleet.CodeClusterRevoked {
		t.Fatalf("token for a revoked cluster: %s", res.Body)
	}
}

func TestFleetReenrolmentReplacesTheCertificate(t *testing.T) {
	h := startFleet(t)
	ctx := context.Background()
	f := h.platform.Fleet
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	created, err := f.Commands.Create(ctx, org, fleet.Create{Name: "lima-1", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	first := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: h.gateway, CAPin: created.Token.CAPin})
	stop := h.agent(t, first, newFakeCluster())
	eventually(t, "the first enrolment", func() bool { return first.Identity().Cluster != "" })
	stop()

	tok, err := f.Commands.IssueToken(ctx, org, created.Cluster.ID, fleet.IssueToken{IssuedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	second := tunnel.NewMemoryStore(tunnel.Enrollment{Token: tok.Value, Gateway: h.gateway, CAPin: tok.CAPin})
	h.agent(t, second, newFakeCluster())
	eventually(t, "the second agent to connect", func() bool {
		c, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected && second.Identity().Cluster != ""
	})
	conn := dialAs(t, h, first.Identity())
	_, err = fleetv1.NewFleetClient(conn).Renew(ctx, &fleetv1.RenewRequest{Csr: csrDER(t)})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("the replaced certificate still works: %v", err)
	}
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func dialAs(t *testing.T, h *fleetHarness, id tunnel.Identity) *grpc.ClientConn {
	t.Helper()
	pair, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient("passthrough:///"+h.gateway, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		ServerName: "localhost", RootCAs: h.ca.Pool(), Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func csrDER(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func csrFor(t *testing.T) *x509.CertificateRequest {
	t.Helper()
	c, err := x509.ParseCertificateRequest(csrDER(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
