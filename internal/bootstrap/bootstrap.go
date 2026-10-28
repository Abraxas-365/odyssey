// Package bootstrap is the composition root: it reads the configuration,
// opens the infrastructure, assembles every domain module and starts the
// HTTP server. It is the only package that imports *module packages.
package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/audit/auditmodule"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/fleetmodule"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/project/projectmodule"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/releasemodule"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/server"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Run loads the configuration and serves until SIGINT/SIGTERM.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "port", cfg.Port)
		errc <- app.HTTP.Listen(":" + cfg.Port)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		return app.HTTP.ShutdownWithTimeout(10 * time.Second)
	}
}

// RunWorker runs odyssey-worker until SIGINT/SIGTERM: it delivers the
// outbox to its subscribers and executes workflows and scheduled tasks. Any
// number of replicas may run; they share the work through Postgres.
func RunWorker() error {
	cfg, err := config.LoadWorker()
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
	p, err := Assemble(ctx, db, Options{Workflow: workflowpg.Options{ListenURL: cfg.DatabaseURL}, ClusterConfig: cfg.Cluster})
	if err != nil {
		return err
	}
	defer p.Close()
	return Work(ctx, p, cfg, outboxpg.Options{ListenURL: cfg.DatabaseURL})
}

// Deliveries is the operator command for stuck events
// (`odyssey-worker deliveries dead` / `deliveries retry <subscriber>`):
// a dead delivery of an ordered subscriber (the audit trail) holds back its
// organization until it is retried, after the cause is fixed.
func Deliveries(args []string, out io.Writer) error {
	cfg, err := config.LoadWorker()
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
	p, err := Assemble(ctx, db, Options{})
	if err != nil {
		return err
	}
	defer p.Close()
	d := p.Dispatcher(outboxpg.Options{})
	switch {
	case len(args) == 1 && args[0] == "dead":
		dead, err := d.Dead(ctx, 1000)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		for _, x := range dead {
			if err := enc.Encode(x); err != nil {
				return err
			}
		}
		return nil
	case len(args) == 2 && args[0] == "retry":
		n, err := d.RetryDead(ctx, args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "requeued %d dead deliveries of %s\n", n, args[1])
		return err
	}
	return errx.Validation("usage: deliveries dead | deliveries retry <subscriber>")
}

// Work runs the worker loops over p until ctx ends; the first loop to fail
// stops the others.
func Work(ctx context.Context, p *Platform, cfg config.Worker, outbox outboxpg.Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	loops := map[string]func(context.Context) error{
		"outbox":    p.Dispatcher(outbox).Run,
		"workflows": p.Workflows.Run,
	}
	if cfg.HealthPort != "" {
		loops["health"] = func(ctx context.Context) error { return serveHealth(ctx, p, cfg.HealthPort) }
	}
	errc := make(chan error, len(loops))
	for name, loop := range loops {
		go func() {
			err := loop(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("worker loop stopped", "loop", name, "err", err)
			}
			cancel()
			errc <- err
		}()
	}
	slog.Info("worker running", "health_port", cfg.HealthPort)
	var first error
	for range loops {
		if err := <-errc; err != nil && first == nil && !errors.Is(err, context.Canceled) {
			first = err
		}
	}
	return first
}

// serveHealth answers GET /health with the database status and the outbox
// backlog (deliveries by subscriber and status).
func serveHealth(ctx context.Context, p *Platform, port string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stats, err := p.Dispatcher(outboxpg.Options{}).Stats(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "deliveries": stats})
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return errx.Wrap(err, "worker health server", errx.TypeInternal)
	}
	return nil
}

// App is the running control plane.
type App struct {
	HTTP    *fiber.App
	closers []func() error
}

// Close releases everything New opened, newest first.
func (a *App) Close() error {
	var first error
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i](); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Migrations are every module's migration sets, in dependency order.
func Migrations() []migrate.Set {
	return []migrate.Set{
		{Owner: outboxpg.Schema, Files: outboxpg.Migrations()},
		{Owner: projectmodule.Schema, Files: projectmodule.Migrations()},
		{Owner: releasemodule.Schema, Files: releasemodule.Migrations()},
		{Owner: fleetmodule.Schema, Files: fleetmodule.Migrations()},
		{Owner: auditmodule.Schema, Files: auditmodule.Migrations()},
		{Owner: workflowpg.Schema, Files: workflowpg.Migrations()},
	}
}

// Migrate brings the database up to date: every module's sets, then River's
// own tables, under the same advisory lock (replicas boot concurrently).
func Migrate(ctx context.Context, db *sqlx.DB) error {
	if err := migrate.Apply(ctx, db, Migrations()...); err != nil {
		return err
	}
	return migrate.Lock(ctx, db, func(ctx context.Context) error { return workflowpg.MigrateRiver(ctx, db) })
}

// Platform is every module assembled over one database, with the event bus
// already subscribed and the workflows registered. The API and the worker
// build the same Platform: the API's publisher must know every subscriber
// to create its deliveries, and its engine every workflow it may start.
type Platform struct {
	DB        *sqlx.DB
	Bus       *event.Bus
	Events    event.Publisher
	Workflows *workflowpg.Engine
	Projects  projectmodule.Module
	Releases  releasemodule.Module
	Fleet     fleetmodule.Module
	Audit     auditmodule.Module
}

// Options are the process-specific parts of a Platform.
type Options struct {
	// Workflow tunes the engine; the worker sets ListenURL, the API (which
	// only starts runs) leaves it empty.
	Workflow workflowpg.Options
	// EventRetention is how long delivered events stay in the outbox (default 30 days).
	EventRetention time.Duration
	// Register adds workflows after the platform's own (tests use it to run
	// a workflow through the real worker). Production leaves it nil.
	Register func(*workflow.Registry)
	// Cluster runs the apps; tests pass a configured fake. Nil picks the
	// adapter from ClusterConfig (the worker's ODYSSEY_CLUSTER; the API never
	// calls the cluster and leaves it as the in-memory fake).
	Cluster       release.Cluster
	ClusterConfig config.Cluster
	// Authority is the fleet CA: verify-only in the API, signing in the
	// gateway, nil where clusters are not registered (the worker).
	Authority fleet.Authority
	// Gateway is the host:port agents dial (the API puts it in tokens).
	Gateway string
	// Release tunes the deploy workflow (tests shorten its waits).
	Release releasesvc.Options
}

// Assemble builds the modules and wires who reacts to what. This is the only
// place subscriptions are made and workflows registered (architecture tests
// enforce it).
func Assemble(ctx context.Context, db *sqlx.DB, opts Options) (*Platform, error) {
	if opts.EventRetention <= 0 {
		opts.EventRetention = 30 * 24 * time.Hour
	}
	bus := event.NewBus()
	events := outboxpg.New(bus)
	registry := workflow.NewRegistry()
	engine := &workflow.Deferred{}
	projects := projectmodule.New(projectmodule.Deps{DB: db, Events: events})
	fleets := fleetmodule.New(fleetmodule.Deps{DB: db, Events: events, Authority: opts.Authority, Gateway: opts.Gateway})
	releases, err := releasemodule.New(releasemodule.Deps{DB: db, Events: events, Projects: projects.Queries,
		Engine: engine, Cluster: opts.Cluster, ClusterConfig: opts.ClusterConfig, Fleet: fleets.Dispatch, Options: opts.Release})
	if err != nil {
		return nil, err
	}
	p := &Platform{
		DB:       db,
		Bus:      bus,
		Events:   events,
		Projects: projects,
		Releases: releases,
		Fleet:    fleets,
		Audit:    auditmodule.New(auditmodule.Deps{DB: db}),
	}
	p.Audit.Subscribe(bus)
	bus.Seal()
	p.Releases.Register(registry)

	registry.Every(workflow.Schedule{Name: "event.purge", Every: 6 * time.Hour, Run: func(ctx context.Context) error {
		n, err := p.Dispatcher(outboxpg.Options{}).Purge(ctx, time.Now().Add(-opts.EventRetention))
		if n > 0 {
			slog.Info("purged delivered events", "count", n)
		}
		return err
	}})
	// Dead deliveries need a person: say so loudly until someone retries them.
	registry.Every(workflow.Schedule{Name: "event.dead_report", Every: 15 * time.Minute, Run: func(ctx context.Context) error {
		dead, err := p.Dispatcher(outboxpg.Options{}).Dead(ctx, 20)
		for _, x := range dead {
			slog.Error("dead event delivery: fix the cause, then run `odyssey-worker deliveries retry "+x.Subscriber+"`",
				"subscriber", x.Subscriber, "event", x.Event, "type", x.Type, "organization", x.Organization,
				"attempts", x.Attempts, "error", x.LastError)
		}
		return err
	}})
	if opts.Register != nil {
		opts.Register(registry)
	}
	wf, err := workflowpg.New(ctx, db, registry, opts.Workflow)
	if err != nil {
		return nil, err
	}
	engine.Set(wf)
	p.Workflows = wf
	return p, nil
}

// Close releases what Assemble opened.
func (p *Platform) Close() error {
	p.Workflows.Close()
	return nil
}

// Dispatcher delivers the platform's events (the worker runs it).
func (p *Platform) Dispatcher(opts outboxpg.Options) *outboxpg.Dispatcher {
	return outboxpg.NewDispatcher(p.DB, p.Bus, opts)
}

// New opens the database, migrates it and assembles the HTTP app.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	db, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a := &App{closers: []func() error{db.Close}}
	if err := Migrate(ctx, db); err != nil {
		a.Close()
		return nil, err
	}
	opts := Options{Gateway: cfg.Fleet.Gateway}
	if cfg.Fleet.Enabled() {
		ca, err := fleetmodule.LoadAuthority(cfg.Fleet.CACert, "")
		if err != nil {
			a.Close()
			return nil, err
		}
		opts.Authority = ca
	}
	p, err := Assemble(ctx, db, opts)
	if err != nil {
		a.Close()
		return nil, err
	}
	a.closers = append(a.closers, p.Close)
	iam := authclient.New(cfg.IAM.URL, authclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	a.HTTP = NewServer(p, cfg, httpx.Authenticate(iam, cfg.IAM))
	return a, nil
}

// NewServer assembles the modules behind auth. Tests call it with a fake
// authenticator; production passes httpx.Authenticate.
func NewServer(p *Platform, cfg config.Config, auth fiber.Handler) *fiber.App {
	return server.New(
		func() error { return p.DB.Ping() },
		server.PublicConfig{IAMURL: cfg.IAM.PublicURL, ClientID: cfg.IAM.WebClientID, Environment: cfg.IAM.Environment},
		server.Options{CORSOrigins: cfg.CORSOrigins},
		[]fiber.Handler{auth},
		p.Projects.HTTP,
		p.Releases.HTTP,
		p.Fleet.HTTP,
		p.Audit.HTTP,
	)
}

// sessionTimeouts bound every pooled connection (docs/api.md §9). A value
// already in DATABASE_URL wins.
var sessionTimeouts = map[string]string{
	"statement_timeout":                   "30s",
	"lock_timeout":                        "10s",
	"idle_in_transaction_session_timeout": "60s",
	"TimeZone":                            "UTC",
}

// connect opens Postgres, retrying briefly so containers can start in any order.
func connect(ctx context.Context, url string) (*sqlx.DB, error) {
	cfg, err := pq.NewConfig(url)
	if err != nil {
		return nil, errx.Wrap(err, "parse database url", errx.TypeInternal)
	}
	if cfg.Runtime == nil {
		cfg.Runtime = map[string]string{}
	}
	for k, v := range sessionTimeouts {
		set := false
		for have := range cfg.Runtime {
			set = set || strings.EqualFold(have, k)
		}
		if !set {
			cfg.Runtime[k] = v
		}
	}
	connector, err := pq.NewConnectorConfig(cfg)
	if err != nil {
		return nil, errx.Wrap(err, "open database", errx.TypeInternal)
	}
	db := sqlx.NewDb(sql.OpenDB(connector), "postgres")
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	for attempt := 1; ; attempt++ {
		err = db.PingContext(ctx)
		if err == nil {
			return db, nil
		}
		if attempt == 10 || ctx.Err() != nil {
			db.Close()
			return nil, errx.Wrap(err, "connect database", errx.TypeExternal)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
}
