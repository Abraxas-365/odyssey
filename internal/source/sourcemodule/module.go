// Package sourcemodule assembles the source domain: repository, the
// GitProvider adapter (GitHub, or Disabled without an App), the service and
// its import workflow, and the HTTP handler. Only the composition root
// imports it.
package sourcemodule

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
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
	if opts.ConsoleURL == "" {
		opts.ConsoleURL = deps.GitHub.ConsoleURL
	}
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

// Register adds the module's workflows (the import workflow) and the poll
// that catches the commits webhooks missed.
func (m Module) Register(r *workflow.Registry) {
	for _, d := range m.workflows.Workflows() {
		r.Define(d)
	}
	r.Every(workflow.Schedule{Name: "source.reconcile", Every: source.ReconcileEvery, Run: func(ctx context.Context) error {
		n, err := m.Commands.Reconcile(ctx)
		if n > 0 {
			slog.Info("queued commits found by polling", "count", n)
		}
		return err
	}})
}

// CommitStatuses is the subscription that shows a push's progress on its
// commit in the git host.
const CommitStatuses = "source.commit_status"

// Subscribe wires the commit statuses: the push and its deployment's events
// become statuses on the commit, in order per organization (so an older
// status never lands after a newer one).
func (m Module) Subscribe(bus *event.Bus) {
	report := func(ctx context.Context, e event.Event, r source.Report) error {
		return m.Commands.Report(ctx, e.Organization, r)
	}
	bus.Add(event.Subscription{
		Name:    CommitStatuses,
		Ordered: true,
		Types: []string{source.EventPushReceived, source.EventPushFailed, release.EventDeploymentStarted,
			release.EventDeploymentReady, release.EventDeploymentFailed, release.EventDeploymentCancelled},
		Handle: func(ctx context.Context, e event.Event) error {
			if e.Version != 1 {
				return nil
			}
			switch e.Type {
			case source.EventPushReceived:
				p, err := event.Decode[source.PushReceived](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Push: p.Push, Status: source.PreparingStatus()})
			case source.EventPushFailed:
				p, err := event.Decode[source.PushFailed](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Push: p.Push, Status: source.PushFailedStatus(p.Message)})
			case release.EventDeploymentStarted:
				p, err := event.Decode[release.DeploymentStarted](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Deployment: p.Deployment, Status: source.PublishingStatus()})
			case release.EventDeploymentReady:
				p, err := event.Decode[release.DeploymentReady](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Deployment: p.Deployment, Status: source.LiveStatus()})
			case release.EventDeploymentFailed:
				p, err := event.Decode[release.DeploymentFailed](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Deployment: p.Deployment, Status: source.DeploymentFailedStatus(p.Message)})
			case release.EventDeploymentCancelled:
				p, err := event.Decode[release.DeploymentCancelled](e)
				if err != nil {
					return decodeError(e, err)
				}
				return report(ctx, e, source.Report{Deployment: p.Deployment, Status: source.ReplacedStatus()})
			}
			return nil
		},
	})
}

func decodeError(e event.Event, err error) error {
	return errx.Wrap(err, fmt.Sprintf("decode %s v%d", e.Type, e.Version), errx.TypeInternal)
}

// Schema is the module's Postgres schema and migration owner.
const Schema = sourcepg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return sourcepg.Migrations() }
