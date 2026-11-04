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
	"net/url"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/accessmodule"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/audit/auditmodule"
	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/blob/adapters/gocloudblob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/buildmodule"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/fleetmodule"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectmcp"
	"github.com/Abraxas-365/odyssey/internal/project/projectmodule"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasemcp"
	"github.com/Abraxas-365/odyssey/internal/release/releasemodule"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/server"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcemcp"
	"github.com/Abraxas-365/odyssey/internal/source/sourcemodule"
	"github.com/Abraxas-365/odyssey/internal/source/sourcesvc"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"github.com/Abraxas-365/odyssey/skills"
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
	blobs, err := gocloudblob.Open(ctx, cfg.Blob.URL)
	if err != nil {
		return err
	}
	defer blobs.Close()
	p, err := Assemble(ctx, db, Options{Workflow: workflowpg.Options{ListenURL: cfg.DatabaseURL}, ClusterConfig: cfg.Cluster,
		Blobs: blobs, BuilderConfig: cfg.Builder, GitHub: cfg.GitHub})
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
		{Owner: buildmodule.Schema, Files: buildmodule.Migrations()},
		{Owner: sourcemodule.Schema, Files: sourcemodule.Migrations()},
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
	Access    accessmodule.Module
	Builds    buildmodule.Module
	Sources   sourcemodule.Module
	// Blobs stores sources and archived build logs (the gateway streams
	// sources from it to the agents).
	Blobs blob.Store
	Fleet fleetmodule.Module
	Audit auditmodule.Module
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
	// adapter from ClusterConfig (ODYSSEY_CLUSTER: the worker applies
	// deployments with it, the API reads the cluster's secret key, sends
	// sealed secrets and checks access; the gateway leaves it as the fake).
	Cluster       release.Cluster
	ClusterConfig config.Cluster
	// Authority is the fleet CA: verify-only in the API, signing in the
	// gateway, nil where clusters are not registered (the worker).
	Authority fleet.Authority
	// Gateway is the host:port agents dial (the API puts it in tokens).
	Gateway string
	// CertificateTTL shortens the agents' certificates (tests watch real
	// renewals); zero is fleet.CertificateTTL.
	CertificateTTL time.Duration
	// Release tunes the deploy workflow (tests shorten its waits).
	Release releasesvc.Options
	// Blobs stores sources and build logs: the API and the worker open the
	// same ODYSSEY_BLOB_URL. Nil is an in-memory store, for tests and for
	// commands that never touch a source.
	Blobs blob.Store
	// Runner builds images; tests pass a configured fake. Nil picks the
	// adapter from BuilderConfig (the API only reads live logs through it).
	Runner        build.Runner
	BuilderConfig config.Builder
	// Build tunes the build service (tests shorten its polls).
	Build buildsvc.Options
	// GitProvider is the git host; tests pass a configured sourcefake. Nil
	// picks it from GitHub (the GitHub App, or disabled without one).
	GitProvider source.GitProvider
	GitHub      config.GitHub
	// Source tunes the import service.
	Source sourcesvc.Options
	// Access configures the sign-in of protected apps (zero value: off);
	// AccessIssuer is the API's public base URL (PublicAPIURL), IAM the
	// IAMKit settings it signs in with. AccessLogin replaces the IAMKit
	// client in tests.
	Access       config.Access
	AccessIssuer string
	IAM          config.IAM
	AccessLogin  access.Login
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
	fleets := fleetmodule.New(fleetmodule.Deps{DB: db, Events: events, Authority: opts.Authority, Gateway: opts.Gateway,
		CertificateTTL: opts.CertificateTTL})
	if opts.Blobs == nil {
		mem, err := gocloudblob.Open(ctx, "mem://")
		if err != nil {
			return nil, err
		}
		opts.Blobs = mem
	}
	builds, err := buildmodule.New(buildmodule.Deps{DB: db, Events: events, Projects: projects.Queries, Blobs: opts.Blobs,
		Engine: engine, Runner: opts.Runner, BuilderConfig: opts.BuilderConfig, Fleet: fleets.Builds, Options: opts.Build})
	if err != nil {
		return nil, err
	}
	releases, err := releasemodule.New(releasemodule.Deps{DB: db, Events: events, Projects: projects.Queries,
		Engine: engine, Cluster: opts.Cluster, ClusterConfig: opts.ClusterConfig, Fleet: fleets.Dispatch,
		BuildCommands: builds.Commands, BuildQueries: builds.Queries, Options: opts.Release,
		AccessLoginAvailable: opts.Access.Enabled() || opts.AccessLogin != nil})
	if err != nil {
		return nil, err
	}
	accesses, err := accessmodule.New(accessmodule.Deps{Targets: releases.Access, IAM: opts.IAM, Access: opts.Access,
		Issuer: opts.AccessIssuer, Login: opts.AccessLogin})
	if err != nil {
		return nil, err
	}
	sources, err := sourcemodule.New(sourcemodule.Deps{DB: db, Events: events, Projects: projects.Queries, Engine: engine,
		Provider: opts.GitProvider, GitHub: opts.GitHub, BuildCommands: builds.Commands,
		ReleaseCommands: releases.Commands, Options: opts.Source})
	if err != nil {
		return nil, err
	}
	p := &Platform{
		DB:       db,
		Bus:      bus,
		Events:   events,
		Projects: projects,
		Releases: releases,
		Access:   accesses,
		Builds:   builds,
		Sources:  sources,
		Blobs:    opts.Blobs,
		Fleet:    fleets,
		Audit:    auditmodule.New(auditmodule.Deps{DB: db}),
	}
	p.Audit.Subscribe(bus)
	p.Sources.Subscribe(bus)
	bus.Seal()
	p.Releases.Register(registry)
	p.Builds.Register(registry)
	p.Sources.Register(registry)
	registry.Every(workflow.Schedule{Name: "fleet.secret_sweep", Every: time.Hour, Run: func(ctx context.Context) error {
		n, err := p.Fleet.Commands.SweepSecrets(ctx)
		if n > 0 {
			slog.Info("dropped expired secret deliveries", "count", n)
		}
		return err
	}})

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
	blobs, err := gocloudblob.Open(ctx, cfg.Blob.URL)
	if err != nil {
		a.Close()
		return nil, err
	}
	a.closers = append(a.closers, blobs.Close)
	opts := Options{Release: releasesvc.Options{AntigravityClientID: cfg.MCP.AntigravityClientID},
		Source: sourcesvc.Options{AntigravityClientID: cfg.MCP.AntigravityClientID}, Gateway: cfg.Fleet.Gateway, Blobs: blobs, Access: cfg.Access, AccessIssuer: PublicAPIURL(cfg), IAM: cfg.IAM, BuilderConfig: cfg.Builder,
		Build: buildsvc.Options{SourceQuota: cfg.Blob.SourceQuota}, GitHub: cfg.GitHub, ClusterConfig: cfg.Cluster}
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
	var mcpAuth *httpx.MCPAuth
	if cfg.MCP.Enabled() {
		if mcpAuth, err = httpx.NewMCPAuth(cfg.IAM, cfg.MCP, permission.All); err != nil {
			a.Close()
			return nil, err
		}
		// The tools hand agents console links; a default of localhost on a
		// real host sends users nowhere.
		if u, perr := url.Parse(cfg.ConsoleURL); perr == nil && u.Hostname() == "localhost" && !strings.Contains(cfg.IAM.Audience, "://localhost") {
			slog.Warn("MCP tools link to the console at " + cfg.ConsoleURL + ": set ODYSSEY_CONSOLE_URL")
		}
	} else {
		slog.Warn("MCP is off: set IAMKIT_INTROSPECTION_CLIENT_ID and its secret (make seed provisions them)")
	}
	switch {
	case cfg.Access.Partial():
		slog.Warn("the access sign-in is off: it needs both ODYSSEY_ACCESS_SIGNING_KEY[_FILE] and IAMKIT_ACCESS_CLIENT_ID/_SECRET (make seed provisions the client)")
	case !cfg.Access.Enabled():
		slog.Warn("the access sign-in is off: apps cannot be protected with an Odicea login (set ODYSSEY_ACCESS_SIGNING_KEY[_FILE] and IAMKIT_ACCESS_CLIENT_ID/_SECRET)")
	}
	a.HTTP = NewServer(p, cfg, httpx.Authenticate(iam, cfg.IAM), mcpAuth)
	return a, nil
}

// NewServer assembles the modules behind auth. Tests call it with a fake
// authenticator; production passes httpx.Authenticate. mcpAuth nil leaves
// /mcp unmounted.
func NewServer(p *Platform, cfg config.Config, auth fiber.Handler, mcpAuth *httpx.MCPAuth) *fiber.App {
	skill := skills.Default()
	assistantRoutes := server.Assistant{
		Info:    assistant.Build(cfg.MCP.Enabled(), cfg.IAM.Audience, PublicAPIURL(cfg)+"/skills/"+skills.Name+".tar.gz", skill.Version, cfg.MCP.AntigravityClientID, cfg.MCP.Clients),
		Archive: skill.Archive, Version: skill.Version,
	}
	opts := server.Options{CORSOrigins: cfg.CORSOrigins, Public: []server.PublicRoutes{p.Sources.HTTP, p.Releases.HTTP, p.Access.HTTP, assistantRoutes}}
	if mcpAuth != nil {
		opts.MCP = &server.MCP{Auth: mcpAuth, Handler: NewMCPHandler(p, cfg)}
	}
	return server.New(
		func() error { return p.DB.Ping() },
		server.PublicConfig{IAMURL: cfg.IAM.PublicURL, ClientID: cfg.IAM.WebClientID, Environment: cfg.IAM.Environment},
		opts,
		[]fiber.Handler{auth},
		p.Projects.HTTP,
		p.Releases.HTTP,
		p.Builds.HTTP,
		p.Sources.HTTP,
		p.Fleet.HTTP,
		p.Audit.HTTP,
		assistantRoutes,
	)
}

// MCPTools are every domain's tools for coding agents: the one MCP server
// (mcpx) lists them, each still gated by its permissions.
func MCPTools(p *Platform, cfg config.Config) []mcpx.Tool {
	github := cfg.GitHub.Enabled()
	sources := p.Sources.Queries
	return slices.Concat(
		projectmcp.Tools(p.Projects.Commands, p.Projects.Queries, projectmcp.Options{ConsoleURL: cfg.ConsoleURL,
			GitHubEnabled: func(context.Context) bool { return github }}),
		releasemcp.Tools(p.Projects.Queries, p.Releases.Commands, p.Releases.Queries, p.Builds.Queries, releasemcp.Options{
			ConsoleURL: cfg.ConsoleURL, APIURL: PublicAPIURL(cfg),
			// release does not know where a source came from; the
			// composition root tells get_app which repository publishes it.
			Repository: func(ctx context.Context, org identity.OrganizationID, app project.ID) (*releasemcp.ConnectedRepo, error) {
				c, err := sources.FindConnection(ctx, org, app)
				if errx.IsType(err, errx.TypeNotFound) {
					return nil, nil
				}
				if err != nil {
					return nil, err
				}
				return &releasemcp.ConnectedRepo{Repository: c.Repository, Branch: c.Branch, Root: c.Root, LastCommit: c.LastCommit}, nil
			}}),
		sourcemcp.Tools(p.Projects.Queries, p.Sources.Commands, sources, sourcemcp.Options{ConsoleURL: cfg.ConsoleURL}),
	)
}

// PublicAPIURL is the API's address as agents reach it: the MCP resource
// (IAMKIT_AUDIENCE, https://<odicea>/mcp) without /mcp. Upload links point
// there ("" when MCP is not configured).
func PublicAPIURL(cfg config.Config) string {
	return strings.TrimSuffix(strings.TrimRight(cfg.IAM.Audience, "/"), httpx.MCPPath)
}

// NewMCPHandler serves MCPTools (the caller authenticates first). A tool
// requiring a permission outside iam/permissions.json is a programming error
// (nobody could ever hold it): it panics at start, and tests catch it.
func NewMCPHandler(p *Platform, cfg config.Config) http.Handler {
	tools := MCPTools(p, cfg)
	for _, t := range tools {
		for _, perm := range t.Permissions {
			if !slices.Contains(permission.All, perm) {
				panic(fmt.Sprintf("bootstrap: MCP tool %s requires %q, which is not in iam/permissions.json", t.Name, perm))
			}
		}
	}
	reg, err := mcpx.NewRegistry(tools)
	if err != nil {
		panic(err) // a programming error: the tools are static, tests catch it
	}
	return mcpx.NewHandler(reg, mcpx.Options{})
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
