// Package fleetgrpc is odyssey-gateway's side of the fleet protocol
// (proto/fleet/v1): agents enrol, renew their certificate and keep their
// tunnel here. The agent's identity always comes from its verified client
// certificate, never from what it sends.
//
// Each tunnel pushes its cluster's desired state: all of it when the tunnel
// opens (and every ResyncEvery, and when the change feed was interrupted),
// and each app as soon as a change is committed (fleet.Signals). Pending
// changes are coalesced per app, so a slow agent gets the latest state, not
// a backlog.
package fleetgrpc

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Options tune the tunnels.
type Options struct {
	// HeartbeatEvery refreshes a connected cluster's last_seen_at (default fleet.HeartbeatEvery).
	HeartbeatEvery time.Duration
	// ResyncEvery resends the whole desired state (default 5 min): the
	// safety net if a change signal was ever missed.
	ResyncEvery time.Duration
	// HelloTimeout is how long a new stream may take to say Hello (default 10 s).
	HelloTimeout time.Duration
	// SendTimeout ends a tunnel whose agent stops reading (default 1 min).
	SendTimeout time.Duration
	// ReportsPerSecond and ReportBurst bound what one tunnel may report
	// (default 5/s, burst 20): a looping agent cannot flood the database.
	ReportsPerSecond float64
	ReportBurst      int
	// EnrollsPerSecond and EnrollBurst bound the calls without a client
	// certificate (Enroll, Recover) across the gateway (default 2/s, burst
	// 10); AddressPerSecond and AddressBurst bound them per client address
	// (default one every 5 s, burst 5), so one source cannot starve the rest.
	EnrollsPerSecond float64
	EnrollBurst      int
	AddressPerSecond float64
	AddressBurst     int
}

// Server implements fleetv1.FleetServer.
type Server struct {
	fleetv1.UnimplementedFleetServer
	agents  fleet.Agents
	signals fleet.Signals
	opts    Options
	public  *perAddress

	mu      sync.Mutex
	tunnels map[fleet.ClusterID]*tunnel
	closing bool // CloseAll ran: new tunnels are refused
}

func New(agents fleet.Agents, signals fleet.Signals, opts Options) *Server {
	if opts.HeartbeatEvery <= 0 {
		opts.HeartbeatEvery = fleet.HeartbeatEvery
	}
	if opts.ResyncEvery <= 0 {
		opts.ResyncEvery = 5 * time.Minute
	}
	if opts.HelloTimeout <= 0 {
		opts.HelloTimeout = 10 * time.Second
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = time.Minute
	}
	if opts.ReportsPerSecond <= 0 {
		opts.ReportsPerSecond, opts.ReportBurst = 5, 20
	}
	if opts.EnrollsPerSecond <= 0 {
		opts.EnrollsPerSecond, opts.EnrollBurst = 2, 10
	}
	if opts.AddressPerSecond <= 0 {
		opts.AddressPerSecond, opts.AddressBurst = 0.2, 5
	}
	return &Server{agents: agents, signals: signals, opts: opts, tunnels: map[fleet.ClusterID]*tunnel{},
		public: newPerAddress(rate.Limit(opts.AddressPerSecond), opts.AddressBurst,
			rate.NewLimiter(rate.Limit(opts.EnrollsPerSecond), opts.EnrollBurst))}
}

// Register adds the service and its authentication to g. The server's TLS
// config must request (not require) client certificates: Enroll and
// Recover have none.
func (s *Server) Register(g *grpc.Server) { fleetv1.RegisterFleetServer(g, s) }

// ServerOptions are the interceptors that authenticate every call but
// Enroll and Recover (rate limited per client address instead).
func (s *Server) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if info.FullMethod == fleetv1.Fleet_Enroll_FullMethodName || info.FullMethod == fleetv1.Fleet_Recover_FullMethodName {
				if !s.public.allow(address(ctx), time.Now()) {
					return nil, status.Error(codes.ResourceExhausted, "too many requests from this address; retry shortly")
				}
				return next(ctx, req)
			}
			ref, err := s.authenticate(ctx)
			if err != nil {
				return nil, err
			}
			return next(withAgent(ctx, ref), req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			ref, err := s.authenticate(ss.Context())
			if err != nil {
				return err
			}
			return next(srv, &agentStream{ServerStream: ss, ctx: withAgent(ss.Context(), ref)})
		}),
	}
}

// Run delivers committed changes to the tunnels until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	return s.signals.Listen(ctx, s.signal, func() {
		slog.Warn("fleet change feed interrupted: resending every desired state")
		s.each(func(t *tunnel) { t.kick(key{}, true) })
	})
}

func (s *Server) signal(sig fleet.Signal) {
	s.mu.Lock()
	t := s.tunnels[sig.Cluster]
	s.mu.Unlock()
	if t == nil || t.agent.Organization != sig.Organization {
		return
	}
	switch {
	case sig.Revoked:
		t.close(fleet.Rejected())
		return
	case sig.Reenrolled:
		// the new key reconnects on its own; this tunnel's key is the old one
		t.close(fleet.Superseded())
		return
	}
	t.kick(key{environment: sig.Environment, name: sig.Name}, false)
}

func (s *Server) each(fn func(*tunnel)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tunnels {
		fn(t)
	}
}

// ---- unary ------------------------------------------------------------

func (s *Server) Enroll(ctx context.Context, req *fleetv1.EnrollRequest) (*fleetv1.EnrollResponse, error) {
	csr, err := parseCSR(req.GetCsr())
	if err != nil {
		return nil, toStatus(err)
	}
	e, err := s.agents.Enroll(ctx, req.GetToken(), csr)
	if err != nil {
		return nil, toStatus(err)
	}
	slog.Info("cluster enrolled", "organization", e.Organization, "cluster", e.Cluster, "address", address(ctx))
	return &fleetv1.EnrollResponse{ClusterId: e.Cluster.String(), OrganizationId: e.Organization.String(),
		Certificate: &fleetv1.Certificate{Pem: e.Certificate.PEM, CaPem: e.Certificate.CAPEM}}, nil
}

func (s *Server) Renew(ctx context.Context, req *fleetv1.RenewRequest) (*fleetv1.RenewResponse, error) {
	csr, err := parseCSR(req.GetCsr())
	if err != nil {
		return nil, toStatus(err)
	}
	issued, err := s.agents.Renew(ctx, agentOf(ctx), csr)
	if err != nil {
		return nil, toStatus(err)
	}
	return &fleetv1.RenewResponse{Certificate: &fleetv1.Certificate{Pem: issued.PEM, CaPem: issued.CAPEM}}, nil
}

func (s *Server) Recover(ctx context.Context, req *fleetv1.RecoverRequest) (*fleetv1.RecoverResponse, error) {
	csr, err := parseCSR(req.GetCsr())
	if err != nil {
		return nil, toStatus(err)
	}
	e, err := s.agents.Recover(ctx, fleet.Recover{Token: req.GetToken(), Organization: req.GetOrganizationId(), Cluster: req.GetClusterId()}, csr)
	if err != nil {
		return nil, toStatus(err)
	}
	slog.Info("agent recovered its certificate", "organization", e.Organization, "cluster", e.Cluster, "address", address(ctx))
	return &fleetv1.RecoverResponse{ClusterId: e.Cluster.String(), OrganizationId: e.Organization.String(),
		Certificate: &fleetv1.Certificate{Pem: e.Certificate.PEM, CaPem: e.Certificate.CAPEM}}, nil
}

// ---- the tunnel -------------------------------------------------------

type key struct {
	environment project.EnvironmentID
	name        string
}

type tunnel struct {
	agent   fleet.AgentRef
	session fleet.Session

	mu      sync.Mutex
	pending map[key]bool
	all     bool
	wake    chan struct{}
	done    chan struct{}
	err     error
	once    sync.Once
}

func (t *tunnel) kick(k key, all bool) {
	t.mu.Lock()
	if all {
		t.all = true
	} else {
		t.pending[k] = true
	}
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *tunnel) take() (map[key]bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, all := t.pending, t.all
	t.pending, t.all = map[key]bool{}, false
	return p, all
}

func (t *tunnel) close(err error) {
	t.once.Do(func() {
		t.err = err
		close(t.done)
	})
}

// Connect serves one tunnel until the agent leaves, the cluster is revoked
// or a newer tunnel of the same cluster takes over.
func (s *Server) Connect(stream fleetv1.Fleet_ConnectServer) error {
	ctx := stream.Context()
	ref := agentOf(ctx)
	hello, err := s.hello(stream)
	if err != nil {
		return err
	}
	t := &tunnel{agent: ref, session: fleet.NewSession(), pending: map[key]bool{}, all: true,
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	if err := s.agents.Connect(ctx, ref, t.session, hello, address(ctx)); err != nil {
		return toStatus(err)
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.agents.Disconnect(dctx, ref, t.session) // recorded just above; nobody serves it
		return status.Error(codes.Unavailable, "the gateway is shutting down")
	}
	if old := s.tunnels[ref.Cluster]; old != nil {
		old.close(status.Error(codes.Aborted, "superseded by a newer connection of this cluster"))
	}
	s.tunnels[ref.Cluster] = t
	s.mu.Unlock()
	log := slog.With("organization", ref.Organization, "cluster", ref.Cluster, "session", t.session)
	log.Info("agent connected", "agent_version", hello.Version, "address", address(ctx))
	defer func() {
		s.mu.Lock()
		if s.tunnels[ref.Cluster] == t {
			delete(s.tunnels, ref.Cluster)
		}
		s.mu.Unlock()
		// the stream's context is done by now; the disconnect must still land
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.agents.Disconnect(dctx, ref, t.session); err != nil {
			log.Warn("record disconnect", "err", err)
		}
		log.Info("agent disconnected", "reason", t.err)
	}()

	go s.receive(stream, t, log)
	go s.send(ctx, stream, t)
	heartbeat := time.NewTicker(s.opts.HeartbeatEvery)
	defer heartbeat.Stop()
	resync := time.NewTicker(s.opts.ResyncEvery)
	defer resync.Stop()
	t.kick(key{}, true)
	for {
		select {
		case <-ctx.Done():
			t.close(ctx.Err())
			return nil
		case <-t.done:
			if errors.Is(t.err, io.EOF) {
				return nil // the agent closed its side
			}
			return toStatus(t.err)
		case <-heartbeat.C:
			// only a rejection ends the tunnel; a database hiccup must not cut
			// every cluster off (the cluster just looks offline for a while)
			if err := s.agents.Heartbeat(ctx, ref, t.session); isLost(err) {
				t.close(err)
			} else if err != nil {
				log.Warn("heartbeat", "err", err)
			}
		case <-resync.C:
			t.kick(key{}, true)
		}
	}
}

// CloseAll ends every tunnel (the gateway is shutting down: agents
// reconnect to another replica), so the handlers return and record their
// disconnects before the server stops.
func (s *Server) CloseAll() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.each(func(t *tunnel) { t.close(status.Error(codes.Unavailable, "the gateway is shutting down")) })
}

// send pushes desired state off the heartbeat loop: an agent that stops
// reading blocks only this goroutine, and a send stuck longer than
// SendTimeout ends the tunnel (ending the handler unblocks it).
func (s *Server) send(ctx context.Context, stream fleetv1.Fleet_ConnectServer, t *tunnel) {
	for {
		select {
		case <-t.done:
			return
		case <-t.wake:
		}
		stuck := time.AfterFunc(s.opts.SendTimeout, func() {
			t.close(status.Error(codes.DeadlineExceeded, "the agent is not reading its desired state"))
		})
		err := s.push(ctx, stream, t)
		stuck.Stop()
		if err != nil {
			t.close(err)
			return
		}
	}
}

func (s *Server) hello(stream fleetv1.Fleet_ConnectServer) (fleet.Agent, error) {
	type result struct {
		msg *fleetv1.AgentMessage
		err error
	}
	got := make(chan result, 1)
	go func() {
		m, err := stream.Recv()
		got <- result{m, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			return fleet.Agent{}, r.err
		}
		h := r.msg.GetHello()
		if h == nil {
			return fleet.Agent{}, status.Error(codes.InvalidArgument, "the first message must be Hello")
		}
		return agentFromProto(h), nil
	case <-time.After(s.opts.HelloTimeout):
		return fleet.Agent{}, status.Error(codes.DeadlineExceeded, "no Hello")
	}
}

// receive stores the agent's reports until the stream ends, at most
// ReportsPerSecond (with a burst): beyond that it stops reading and flow
// control slows the agent down.
func (s *Server) receive(stream fleetv1.Fleet_ConnectServer, t *tunnel, log *slog.Logger) {
	limit := rate.NewLimiter(rate.Limit(s.opts.ReportsPerSecond), s.opts.ReportBurst)
	for {
		m, err := stream.Recv()
		if err != nil {
			t.close(err)
			return
		}
		r := m.GetReport()
		if r == nil {
			continue // unknown or repeated Hello: ignored (newer agents may send more)
		}
		if err := limit.Wait(stream.Context()); err != nil {
			t.close(err)
			return
		}
		report, err := reportFromProto(r)
		if err == nil {
			err = s.agents.Report(stream.Context(), t.agent, t.session, report)
		}
		if errx.IsType(err, errx.TypeValidation) {
			log.Warn("agent report partly rejected", "err", err) // keep the tunnel: one bad app must not cut the cluster off
			continue
		}
		if err != nil {
			t.close(err)
			return
		}
	}
}

// push sends what changed (or everything).
func (s *Server) push(ctx context.Context, stream fleetv1.Fleet_ConnectServer, t *tunnel) error {
	pending, all := t.take()
	var apps []fleet.App
	if all {
		desired, err := s.agents.Desired(ctx, t.agent)
		if err != nil {
			return err
		}
		apps = desired
	} else {
		for k := range pending {
			a, ok, err := s.agents.DesiredApp(ctx, t.agent, k.environment, k.name)
			if err != nil {
				return err
			}
			if ok {
				apps = append(apps, a)
			}
		}
	}
	for _, a := range apps {
		if err := stream.Send(&fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_Apply{Apply: appToProto(a)}}); err != nil {
			return err
		}
	}
	return nil
}

// ---- authentication ---------------------------------------------------

type agentKey struct{}

func withAgent(ctx context.Context, ref fleet.AgentRef) context.Context {
	return context.WithValue(ctx, agentKey{}, ref)
}

func agentOf(ctx context.Context) fleet.AgentRef {
	ref, _ := ctx.Value(agentKey{}).(fleet.AgentRef)
	return ref
}

type agentStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (a *agentStream) Context() context.Context { return a.ctx }

// authenticate reads the agent from the client certificate the TLS layer
// verified against the fleet CA, and checks it is still accepted.
func (s *Server) authenticate(ctx context.Context) (fleet.AgentRef, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return fleet.AgentRef{}, status.Error(codes.Unauthenticated, "no peer")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return fleet.AgentRef{}, status.Error(codes.Unauthenticated, "a client certificate is required")
	}
	leaf := info.State.VerifiedChains[0][0]
	if len(leaf.URIs) != 1 {
		return fleet.AgentRef{}, status.Error(codes.Unauthenticated, "the certificate names no agent")
	}
	org, cluster, err := fleet.ParseAgentURI(leaf.URIs[0])
	if err != nil {
		return fleet.AgentRef{}, status.Error(codes.Unauthenticated, "the certificate names no agent")
	}
	ref := fleet.AgentRef{Organization: org, Cluster: cluster, Serial: fleet.Serial(leaf.SerialNumber)}
	if err := s.agents.Authenticate(ctx, ref); err != nil {
		return fleet.AgentRef{}, toStatus(err)
	}
	return ref, nil
}

func parseCSR(der []byte) (*x509.CertificateRequest, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, errx.Validation("the certificate request is not valid")
	}
	return csr, nil
}

func address(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
		return host
	}
	return p.Addr.String()
}

// toStatus maps the one error system to gRPC codes; internal details stay
// in the gateway's log.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && !isErrx(err) {
		return err
	}
	var e *errx.Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.Canceled) {
			return status.Error(codes.Canceled, "canceled")
		}
		slog.Error("fleet gateway", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
	switch {
	case e.Code == errx.CodeForbidden || e.Code == fleet.CodeRevoked:
		return status.Error(codes.PermissionDenied, e.Message)
	case e.Type == errx.TypeAuthorization:
		return status.Error(codes.Unauthenticated, e.Message)
	case e.Type == errx.TypeValidation:
		return status.Error(codes.InvalidArgument, e.Message)
	case e.Type == errx.TypeNotFound:
		return status.Error(codes.NotFound, e.Message)
	case e.Type == errx.TypeConflict:
		return status.Error(codes.Aborted, e.Message)
	case e.Type == errx.TypeBusiness:
		return status.Error(codes.FailedPrecondition, e.Message)
	}
	slog.Error("fleet gateway", "err", err)
	return status.Error(codes.Internal, "internal error")
}

// isLost: the tunnel is no longer the cluster's (rejected or superseded).
func isLost(err error) bool {
	var e *errx.Error
	return errors.As(err, &e) && (e.Code == fleet.CodeRevoked || e.Code == fleet.CodeSuperseded)
}

func isErrx(err error) bool {
	var e *errx.Error
	return errors.As(err, &e)
}
