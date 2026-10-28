// Package tunnel is the agent's connection to the control plane: it enrols
// the cluster with a one-time token, keeps its identity in a Secret, holds
// an outbound mutual-TLS gRPC stream to odyssey-gateway (reconnecting with
// jittered backoff), renews its certificate before it expires, applies the
// desired apps it receives and reports what the cluster observes.
//
// Nothing listens in the cluster: the agent only dials out, and it trusts
// only a gateway whose certificate chains to the pinned fleet CA.
package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/util/workqueue"
)

// Desired is what the tunnel does in the cluster (agent/desired).
type Desired interface {
	Apply(ctx context.Context, app *fleetv1.App) error
	Status(ctx context.Context, environment, name string) (*fleetv1.AppStatus, error)
	Statuses(ctx context.Context) ([]*fleetv1.AppStatus, error)
	Capabilities(ctx context.Context) *fleetv1.Capabilities
}

// Options configure a Client.
type Options struct {
	Store   Store
	Desired Desired
	// IsRejected tells a refused app (reported failed) from a transient
	// error (retried).
	IsRejected func(error) bool
	// Rejected is the status reported for a refused app.
	Rejected func(*fleetv1.App, error) *fleetv1.AppStatus
	// Version is the agent's, sent in Hello.
	Version string
	Log     logr.Logger
	// MinBackoff and MaxBackoff bound the wait between connection attempts
	// (defaults 1 s and 1 min).
	MinBackoff, MaxBackoff time.Duration
	// ReportEvery resends every app's status (default 5 min).
	ReportEvery time.Duration
	// Workers apply desired apps concurrently (default 2).
	Workers int
	// Dial, when set, is the address connected to instead of the gateway's
	// own (the certificate is still checked against the gateway's name): an
	// agent run outside the cluster reaches it by another route.
	Dial string
}

type key struct{ environment, name string }

// Client is the agent's tunnel. Start runs it (a manager.Runnable: with
// leader election only the leader connects).
type Client struct {
	o Options

	queue workqueue.TypedRateLimitingInterface[key]

	mu       sync.Mutex
	apps     map[key]*fleetv1.App       // newest desired revision per app, until applied
	rejected map[key]*fleetv1.AppStatus // refused apps (their failure is reported)
	changed  map[key]bool               // statuses to report
	wake     chan struct{}
	cert     *tls.Certificate // current client certificate
}

func New(o Options) *Client {
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Minute
	}
	if o.ReportEvery <= 0 {
		o.ReportEvery = 5 * time.Minute
	}
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.IsRejected == nil {
		o.IsRejected = func(error) bool { return false }
	}
	return &Client{
		o: o,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[key](500*time.Millisecond, 30*time.Second),
			workqueue.TypedRateLimitingQueueConfig[key]{Name: "odyssey-desired-apps"}),
		apps: map[key]*fleetv1.App{}, rejected: map[key]*fleetv1.AppStatus{}, changed: map[key]bool{},
		wake: make(chan struct{}, 1),
	}
}

// Changed marks an app whose status may have changed (the agent's watch on
// OdysseyApps calls it); it is reported on the open tunnel, coalesced.
func (c *Client) Changed(environment, name string) {
	c.mu.Lock()
	c.changed[key{environment, name}] = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Start connects and stays connected until ctx ends.
func (c *Client) Start(ctx context.Context) error {
	defer c.queue.ShutDown()
	for range c.o.Workers {
		go c.work(ctx)
	}
	backoff := c.o.MinBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRenewed) {
			c.o.Log.Info("reconnecting with the renewed certificate")
			backoff = c.o.MinBackoff
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = c.o.MinBackoff // it was up for a while: reconnect quickly
		}
		wait := jitter(backoff)
		c.logFailure(err, wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, c.o.MaxBackoff)
	}
	return nil
}

func (c *Client) logFailure(err error, wait time.Duration) {
	switch status.Code(err) {
	case codes.PermissionDenied:
		c.o.Log.Error(err, "the control plane no longer accepts this cluster; reconnect it from the console (new token)", "retry_in", wait)
	case codes.Unauthenticated:
		c.o.Log.Error(err, "enrolment refused: the token is not valid or expired; generate a new one in the console", "retry_in", wait)
	case codes.Aborted:
		c.o.Log.Info("tunnel superseded by another connection of this cluster", "retry_in", wait)
	default:
		c.o.Log.Info("tunnel down", "err", err, "retry_in", wait)
	}
}

// jitter spreads reconnects (±20 %) so a restarted gateway is not stampeded.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// session enrols when there is a token, renews when due and serves one
// stream until it breaks.
func (c *Client) session(ctx context.Context) error {
	id, en, err := c.o.Store.Load(ctx)
	if err != nil {
		return err
	}
	if en.complete() {
		enrolled, err := c.enroll(ctx, en)
		switch {
		case err == nil:
			id = enrolled
		case len(id.CertPEM) == 0 || status.Code(err) != codes.Unauthenticated:
			return err
		default:
			// A stale token next to a working identity: keep the identity.
			c.o.Log.Error(err, "ignoring the enrolment token in the agent's secret (not valid); using the current identity")
		}
	}
	if len(id.CertPEM) == 0 {
		return errors.New("not enrolled: the agent's secret has no token; install the agent with the command the console shows")
	}
	pair, err := id.keyPair()
	if err != nil {
		return err
	}
	if time.Now().After(pair.Leaf.NotAfter) {
		// down for longer than the certificate's life: the gateway would only
		// fail the handshake with a generic TLS error
		return status.Error(codes.PermissionDenied, "the agent's certificate expired on "+pair.Leaf.NotAfter.Format(time.RFC3339)+
			"; it cannot renew it: generate a new token in the console")
	}
	c.setCert(&pair)

	conn, err := c.dial(id)
	if err != nil {
		return err
	}
	defer conn.Close()
	api := fleetv1.NewFleetClient(conn)
	if time.Now().After(renewAt(pair.Leaf)) {
		if id, err = c.renew(ctx, api, id); err != nil {
			return err
		}
		pair, _ = id.keyPair()
	}
	return c.stream(ctx, api, id, pair.Leaf)
}

func (c *Client) setCert(cert *tls.Certificate) {
	c.mu.Lock()
	c.cert = cert
	c.mu.Unlock()
}

func (c *Client) currentCert() *tls.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert
}

func (c *Client) target(gateway string) string {
	if c.o.Dial != "" {
		return c.o.Dial
	}
	return gateway
}

func (c *Client) dial(id Identity) (*grpc.ClientConn, error) {
	cfg, err := tlsConfig(id.Gateway, id.CAPin, c.currentCert)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient("passthrough:///"+c.target(id.Gateway),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	)
}

func (c *Client) enroll(ctx context.Context, en Enrollment) (Identity, error) {
	cfg, err := tlsConfig(en.Gateway, en.CAPin, nil)
	if err != nil {
		return Identity{}, err
	}
	conn, err := grpc.NewClient("passthrough:///"+c.target(en.Gateway), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		return Identity{}, err
	}
	defer conn.Close()
	keyPEM, csr, err := newKey()
	if err != nil {
		return Identity{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := fleetv1.NewFleetClient(conn).Enroll(cctx, &fleetv1.EnrollRequest{Token: en.Token, Csr: csr})
	if err != nil {
		return Identity{}, err
	}
	if err := checkCA(res.GetCertificate().GetCaPem(), en.CAPin); err != nil {
		return Identity{}, err
	}
	id := Identity{
		Organization: res.GetOrganizationId(), Cluster: res.GetClusterId(), KeyPEM: keyPEM,
		CertPEM: res.GetCertificate().GetPem(), CAPEM: res.GetCertificate().GetCaPem(), Gateway: en.Gateway, CAPin: en.CAPin,
	}
	if _, err := id.keyPair(); err != nil {
		return Identity{}, err
	}
	// The token is spent now: losing this identity means a new token from
	// the console, so the save is retried before giving up.
	if err := c.saveSpent(ctx, id); err != nil {
		c.o.Log.Error(err, "enrolled, but the identity could not be stored; the token is spent: generate a new one in the console")
		return Identity{}, err
	}
	c.o.Log.Info("cluster enrolled", "organization", id.Organization, "cluster", id.Cluster)
	return id, nil
}

// saveSpent stores an identity the control plane already counts as issued.
func (c *Client) saveSpent(ctx context.Context, id Identity) error {
	var err error
	for wait := time.Second; wait <= 16*time.Second; wait *= 2 {
		if err = c.o.Store.Save(ctx, id); err == nil {
			return nil
		}
		c.o.Log.Error(err, "storing the agent identity failed; retrying", "retry_in", wait)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
	return err
}

// renew trades the current certificate for a new one with a new key, and
// keeps it before using it (a crash in between leaves the old one, accepted
// for fleet's renewal overlap: the next renewal retries with it).
func (c *Client) renew(ctx context.Context, api fleetv1.FleetClient, id Identity) (Identity, error) {
	keyPEM, csr, err := newKey()
	if err != nil {
		return id, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := api.Renew(cctx, &fleetv1.RenewRequest{Csr: csr})
	if err != nil {
		return id, fmt.Errorf("renew certificate: %w", err)
	}
	if err := checkCA(res.GetCertificate().GetCaPem(), id.CAPin); err != nil {
		return id, err
	}
	next := id
	next.KeyPEM, next.CertPEM, next.CAPEM = keyPEM, res.GetCertificate().GetPem(), res.GetCertificate().GetCaPem()
	pair, err := next.keyPair()
	if err != nil {
		return id, err
	}
	if err := c.o.Store.Save(ctx, next); err != nil {
		return id, err
	}
	c.setCert(&pair)
	c.o.Log.Info("certificate renewed", "expires", pair.Leaf.NotAfter)
	return next, nil
}

// stream serves one tunnel: Hello, a full report, then applies what
// arrives and reports what changes until the stream breaks.
func (c *Client) stream(ctx context.Context, api fleetv1.FleetClient, id Identity, leaf *x509.Certificate) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := api.Connect(ctx)
	if err != nil {
		return err
	}
	var sendMu sync.Mutex
	send := func(m *fleetv1.AgentMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return st.Send(m)
	}
	hello := &fleetv1.Hello{AgentVersion: c.o.Version, Capabilities: c.o.Desired.Capabilities(ctx)}
	if err := send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		return err
	}
	if err := c.reportAll(ctx, send); err != nil {
		return err
	}
	c.o.Log.Info("tunnel up", "gateway", id.Gateway, "cluster", id.Cluster)

	errc := make(chan error, 2)
	go func() { errc <- c.receive(st) }()
	go func() { errc <- c.reportLoop(ctx, send) }()

	renewal := time.NewTimer(time.Until(renewAt(leaf)))
	defer renewal.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = st.CloseSend()
			return nil
		case err := <-errc:
			return err
		case <-renewal.C:
			// The open stream keeps its handshake; the new certificate is
			// used from the next connection on.
			if _, err := c.renew(ctx, api, id); err != nil {
				c.o.Log.Error(err, "certificate renewal failed; retrying in 10 min")
				renewal.Reset(10 * time.Minute)
				continue
			}
			// The open stream was authenticated with the certificate renewed
			// from, accepted only for a short overlap: reconnect with the new
			// one now rather than be cut off when the overlap ends.
			_ = st.CloseSend()
			return errRenewed
		}
	}
}

// errRenewed ends a stream so the agent reconnects with its new certificate.
var errRenewed = errors.New("reconnecting with the renewed certificate")

// receive queues the desired apps the gateway sends.
func (c *Client) receive(st fleetv1.Fleet_ConnectClient) error {
	for {
		m, err := st.Recv()
		if err != nil {
			return err
		}
		a := m.GetApply()
		if a == nil {
			continue // a newer gateway's message this agent does not know
		}
		k := key{a.GetEnvironmentId(), a.GetName()}
		c.mu.Lock()
		if cur := c.apps[k]; cur == nil || cur.GetRevision() <= a.GetRevision() {
			c.apps[k] = a
		}
		c.mu.Unlock()
		c.queue.Add(k)
	}
}

// work applies queued apps: a refused one is reported failed, a transient
// error is retried with backoff.
func (c *Client) work(ctx context.Context) {
	for {
		k, shutdown := c.queue.Get()
		if shutdown {
			return
		}
		c.mu.Lock()
		a := c.apps[k]
		c.mu.Unlock()
		if a != nil {
			err := c.o.Desired.Apply(ctx, a)
			switch {
			case err == nil:
				c.applied(k, a, nil)
				c.queue.Forget(k)
			case c.o.IsRejected(err):
				c.o.Log.Error(err, "app refused", "environment", k.environment, "app", k.name, "revision", a.GetRevision())
				c.applied(k, a, err)
				c.queue.Forget(k)
			case ctx.Err() == nil:
				c.o.Log.Info("apply failed, retrying", "environment", k.environment, "app", k.name, "err", err)
				c.queue.AddRateLimited(k)
			}
		}
		c.queue.Done(k)
	}
}

// applied clears the app unless a newer revision arrived meanwhile.
func (c *Client) applied(k key, a *fleetv1.App, refused error) {
	c.mu.Lock()
	if c.apps[k] == a {
		delete(c.apps, k)
	}
	if refused != nil && c.o.Rejected != nil {
		c.rejected[k] = c.o.Rejected(a, refused)
	} else {
		delete(c.rejected, k)
	}
	c.mu.Unlock()
	c.Changed(k.environment, k.name)
}

// reportLoop sends what changed (coalesced) and, every ReportEvery, everything.
func (c *Client) reportLoop(ctx context.Context, send func(*fleetv1.AgentMessage) error) error {
	full := time.NewTicker(c.o.ReportEvery)
	defer full.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-full.C:
			if err := c.reportAll(ctx, send); err != nil {
				return err
			}
		case <-c.wake:
			// let a burst of changes (a rollout) settle into one report
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(100 * time.Millisecond):
			}
			if err := c.reportChanged(ctx, send); err != nil {
				return err
			}
		}
	}
}

func (c *Client) reportChanged(ctx context.Context, send func(*fleetv1.AgentMessage) error) error {
	c.mu.Lock()
	keys := c.changed
	c.changed = map[key]bool{}
	c.mu.Unlock()
	var apps []*fleetv1.AppStatus
	for k := range keys {
		s, err := c.o.Desired.Status(ctx, k.environment, k.name)
		if err != nil {
			c.Changed(k.environment, k.name) // try again with the next change
			c.o.Log.Info("read app status", "err", err)
			continue
		}
		if s = c.withRejected(k, s); s != nil {
			apps = append(apps, s)
		}
	}
	return sendReport(send, false, apps)
}

func (c *Client) reportAll(ctx context.Context, send func(*fleetv1.AgentMessage) error) error {
	apps, err := c.o.Desired.Statuses(ctx)
	if err != nil {
		c.o.Log.Info("read app statuses", "err", err)
		apps = nil
	}
	seen := map[key]bool{}
	for i, s := range apps {
		k := key{s.GetEnvironmentId(), s.GetName()}
		seen[k] = true
		apps[i] = c.withRejected(k, s)
	}
	c.mu.Lock()
	for k, s := range c.rejected {
		if !seen[k] {
			apps = append(apps, s)
		}
	}
	c.mu.Unlock()
	return sendReport(send, err == nil, apps)
}

// withRejected prefers a refusal of a newer revision over what the cluster
// runs (an older revision).
func (c *Client) withRejected(k key, s *fleetv1.AppStatus) *fleetv1.AppStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.rejected[k]; r != nil && (s == nil || r.GetObservedRevision() > s.GetObservedRevision()) {
		return r
	}
	return s
}

// maxReportApps matches the gateway's limit (fleet.MaxReportApps).
const maxReportApps = 1000

func sendReport(send func(*fleetv1.AgentMessage) error, full bool, apps []*fleetv1.AppStatus) error {
	if len(apps) == 0 && !full {
		return nil
	}
	for len(apps) > maxReportApps {
		if err := send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_Report{Report: &fleetv1.AppReport{Apps: apps[:maxReportApps]}}}); err != nil {
			return err
		}
		apps = apps[maxReportApps:]
	}
	return send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_Report{Report: &fleetv1.AppReport{Full: full, Apps: apps}}})
}
