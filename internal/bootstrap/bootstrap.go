// Package bootstrap is the composition root: it reads the configuration,
// opens the infrastructure, assembles every domain module and starts the
// HTTP server. It is the only package that imports *module packages.
package bootstrap

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/project/projectmodule"
	"github.com/Abraxas-365/odyssey/internal/server"
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
		{Owner: projectmodule.Schema, Files: projectmodule.Migrations()},
	}
}

// New opens the database, migrates it and assembles the HTTP app.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	db, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a := &App{closers: []func() error{db.Close}}
	if err := migrate.Apply(ctx, db, Migrations()...); err != nil {
		a.Close()
		return nil, err
	}
	iam := authclient.New(cfg.IAM.URL, authclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	a.HTTP = NewServer(db, cfg, httpx.Authenticate(iam, cfg.IAM))
	return a, nil
}

// NewServer assembles the modules behind auth. Tests call it with a fake
// authenticator; production passes httpx.Authenticate.
func NewServer(db *sqlx.DB, cfg config.Config, auth fiber.Handler) *fiber.App {
	projects := projectmodule.New(projectmodule.Deps{DB: db})
	return server.New(
		func() error { return db.Ping() },
		server.PublicConfig{IAMURL: cfg.IAM.PublicURL, ClientID: cfg.IAM.WebClientID, Environment: cfg.IAM.Environment},
		server.Options{CORSOrigins: cfg.CORSOrigins},
		[]fiber.Handler{auth},
		projects.HTTP,
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
