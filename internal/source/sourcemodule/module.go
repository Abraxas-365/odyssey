// Package sourcemodule assembles the source domain: repository, the
// GitProvider adapter (GitHub, or Disabled without an App), the service and
// its import workflow, and the HTTP handler. Only the composition root
// imports it.
package sourcemodule

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcebuild"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcegithub"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcehttp"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcepg"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcerelease"
	"github.com/Abraxas-365/odyssey/internal/source/sourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB       *sqlx.DB
	Events   event.Publisher
	Projects project.Queries
	// Engine starts import runs (a workflow.Deferred from the composition root).
	Engine workflow.Engine
	// Provider is the git host; tests pass a configured sourcefake. Nil picks
	// it from GitHub: the GitHub adapter when the App is configured,
	// sourcefake.Disabled otherwise.
	Provider source.GitProvider
	GitHub   config.GitHub
	// The build and release modules' ports.
	BuildCommands   build.Commands
	ReleaseCommands release.Commands
	Options         sourcesvc.Options
}

type Module struct {
	Commands  source.Commands
	Queries   source.Queries
	HTTP      *sourcehttp.Handler
	workflows source.Workflows
}

func New(deps Deps) (Module, error) {
	if deps.BuildCommands == nil || deps.ReleaseCommands == nil {
		return Module{}, fmt.Errorf("the source module needs the build and release modules")
	}
	provider := deps.Provider
	if provider == nil {
		var err error
		if provider, err = providerOf(deps.GitHub); err != nil {
			return Module{}, err
		}
	}
	opts := deps.Options
	if provider.Enabled() && len(opts.StateKey) == 0 {
		key, err := stateKey(deps.GitHub)
		if err != nil {
			return Module{}, err
		}
		opts.StateKey = key
	}
	svc := sourcesvc.New(sourcepg.New(deps.DB), provider, sourcebuild.New(deps.BuildCommands),
		sourcerelease.New(deps.ReleaseCommands), deps.Projects, deps.Engine,
		txn.Postgres{DB: deps.DB}, deps.Events, opts)
	return Module{Commands: svc, Queries: svc, HTTP: sourcehttp.New(svc, svc, deps.GitHub.ConsoleURL), workflows: svc}, nil
}

func providerOf(cfg config.GitHub) (source.GitProvider, error) {
	if !cfg.Enabled() {
		return sourcefake.Disabled(), nil
	}
	p, err := sourcegithub.New(cfg, sourcegithub.Options{})
	if err != nil {
		return nil, fmt.Errorf("github app: %w", err)
	}
	return p, nil
}

// stateKey reads ODYSSEY_STATE_KEY_FILE or, without one, derives a key
// from the webhook secret (a separate purpose, so knowing one signature
// never forges the other).
func stateKey(cfg config.GitHub) ([]byte, error) {
	file, derive := cfg.StateKeyFile, false
	if file == "" {
		file, derive = cfg.WebhookSecretFile, true
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read the install state key: %w", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) < 16 {
		return nil, fmt.Errorf("the install state key (%s) is shorter than 16 bytes", file)
	}
	if !derive {
		return secret, nil
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("odyssey.github-install-state"))
	return m.Sum(nil), nil
}

// Register adds the module's workflows (the import workflow) to r.
func (m Module) Register(r *workflow.Registry) {
	for _, d := range m.workflows.Workflows() {
		r.Define(d)
	}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = sourcepg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return sourcepg.Migrations() }
