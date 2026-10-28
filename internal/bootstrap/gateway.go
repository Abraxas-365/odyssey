package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetgrpc"
	"github.com/Abraxas-365/odyssey/internal/fleet/fleetmodule"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// RunGateway runs odyssey-gateway until SIGINT/SIGTERM: the endpoint
// agents enrol with and keep their tunnels to. Any number of replicas may
// run behind a TCP (not TLS-terminating) load balancer; changes reach the
// replica holding a cluster's tunnel through Postgres.
func RunGateway() error {
	cfg, err := config.LoadGateway()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		return err
	}
	ca, err := fleetmodule.LoadAuthority(cfg.CACert, cfg.CAKey)
	if err != nil {
		return err
	}
	p, err := Assemble(ctx, db, Options{Authority: ca, Workflow: workflowpg.Options{}})
	if err != nil {
		return err
	}
	defer p.Close()
	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errx.Wrap(err, "gateway listen", errx.TypeInternal)
	}
	return Gateway(ctx, p, ca, cfg, lis, fleetgrpc.Options{})
}

// InitFleetCA writes a new fleet CA (ten years) to certFile and keyFile,
// refusing to overwrite: replacing the CA disconnects every cluster.
func InitFleetCA(certFile, keyFile string) error {
	certPEM, keyPEM, err := fleetca.Generate("Odyssey fleet CA", time.Now(), 10*365*24*time.Hour)
	if err != nil {
		return err
	}
	for _, f := range []string{keyFile, certFile} { // all or nothing: never a key without its certificate
		if _, err := os.Stat(f); err == nil {
			return errx.Conflict(f + " already exists; replacing the fleet CA disconnects every cluster")
		}
	}
	if err := writeNew(keyFile, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeNew(certFile, certPEM, 0o644); err != nil {
		os.Remove(keyFile)
		return err
	}
	slog.Info("fleet CA created", "certificate", certFile, "key", keyFile)
	return nil
}

func writeNew(name string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if os.IsExist(err) {
			return errx.Conflict(name + " already exists; replacing the fleet CA disconnects every cluster")
		}
		return errx.Wrap(err, "create "+name, errx.TypeInternal)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name) // never leave a half-written key behind
		return errx.Wrap(err, "write "+name, errx.TypeInternal)
	}
	if err := f.Close(); err != nil {
		return errx.Wrap(err, "write "+name, errx.TypeInternal)
	}
	return nil
}

// Gateway serves the fleet protocol on lis until ctx ends (tests call it
// with their own listener and short timings).
func Gateway(ctx context.Context, p *Platform, ca *fleetca.Authority, cfg config.Gateway, lis net.Listener, opts fleetgrpc.Options) error {
	certs, err := newServingCert(ca, cfg.Hosts)
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: certs.get,
		// Enroll comes without a client certificate; every other call is
		// refused by the interceptors without a verified one.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  ca.Pool(),
	}
	server := p.Fleet.Gateway(cfg.DatabaseURL, opts)
	g := grpc.NewServer(append(server.ServerOptions(),
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 15 * time.Second, PermitWithoutStream: true}),
		grpc.MaxRecvMsgSize(4<<20),
		// unauthenticated callers reach the handshake: bound what they hold
		grpc.ConnectionTimeout(10*time.Second),
		grpc.MaxConcurrentStreams(16),
	)...)
	server.Register(g)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errc := make(chan error, 4)
	run := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fn()
			if err != nil && ctx.Err() == nil {
				slog.Error("gateway loop stopped", "loop", name, "err", err)
				errc <- err
			}
			cancel()
		}()
	}
	run("signals", func() error { return server.Run(ctx) })
	run("certificate", func() error { return certs.rotate(ctx) })
	run("grpc", func() error { return g.Serve(lis) })
	if cfg.HealthPort != "" {
		run("health", func() error { return serveGatewayHealth(ctx, p, cfg.HealthPort) })
	}
	slog.Info("gateway listening", "addr", lis.Addr().String(), "hosts", cfg.Hosts)
	<-ctx.Done()
	// tunnels never end on their own: end them first so their handlers
	// record the disconnects (while the database is still open) and
	// GracefulStop can finish; agents reconnect to another replica
	server.CloseAll()
	stopped := make(chan struct{})
	go func() { g.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		g.Stop()
	}
	wg.Wait()
	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
}

// servingCert is the gateway's own certificate, signed by the fleet CA (the
// one agents pin) and renewed daily.
type servingCert struct {
	ca    *fleetca.Authority
	hosts []string
	mu    sync.RWMutex
	cert  *tls.Certificate
}

const servingTTL = 7 * 24 * time.Hour

func newServingCert(ca *fleetca.Authority, hosts []string) (*servingCert, error) {
	s := &servingCert{ca: ca, hosts: hosts}
	return s, s.renew()
}

func (s *servingCert) renew() error {
	c, err := s.ca.ServerCertificate(s.hosts, time.Now(), servingTTL)
	if err != nil {
		return errx.Wrap(err, "gateway certificate", errx.TypeInternal)
	}
	s.mu.Lock()
	s.cert = &c
	s.mu.Unlock()
	return nil
}

func (s *servingCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cert, nil
}

func (s *servingCert) rotate(ctx context.Context) error {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := s.renew(); err != nil {
				slog.Error("renew gateway certificate", "err", err) // the current one is valid for days
			}
		}
	}
}

func serveGatewayHealth(ctx context.Context, p *Platform, port string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := p.DB.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return errx.Wrap(err, "gateway health server", errx.TypeInternal)
	}
	return nil
}
