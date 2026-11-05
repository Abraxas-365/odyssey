// Package aimodule assembles the ai domain: repository, service, workflows,
// HTTP handler and the cross-domain adapters. Only the composition root
// imports it, and only it picks the Provider adapter.
package aimodule

import (
	"context"
	"io/fs"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifreerouter"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aihttp"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aipg"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/airelease"
	"github.com/Abraxas-365/odyssey/internal/ai/aisvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB       *sqlx.DB
	Events   event.Publisher
	Projects project.Queries
	// Engine starts the AI workflows (a workflow.Deferred, like release's).
	Engine workflow.Engine
	// Release is the release module's AI port.
	Release release.AI
	// Config selects the gateway; empty leaves the capability absent.
	Config config.AI
	// Provider replaces the configured gateway (tests pass a fake).
	Provider ai.Provider
	// Policy is IT's rules for turning the AI on (the policyai adapter).
	Policy  ai.Policy
	Options aisvc.Options
}

type Module struct {
	Commands  ai.Commands
	Queries   ai.Queries
	HTTP      *aihttp.Handler
	workflows ai.Workflows
	svc       *aisvc.Service
}

// UseApprovals wires where ai holds what a rule wants approved (the
// approvalai adapter; the approval module is built after this one).
func (m Module) UseApprovals(a ai.Approvals) { m.svc.UseApprovals(a) }

func New(deps Deps) Module {
	provider := deps.Provider
	if provider == nil && deps.Config.Enabled() {
		provider = aifreerouter.New(aifreerouter.Options{URL: deps.Config.URL, Key: deps.Config.Key})
	}
	opts := deps.Options
	if opts.BaseURL == "" {
		opts.BaseURL = deps.Config.AppURL
	}
	svc := aisvc.New(aipg.New(deps.DB), provider, airelease.New(deps.Release), deps.Projects, deps.Engine,
		txn.Postgres{DB: deps.DB}, deps.Events, deps.Policy, opts)
	return Module{Commands: svc, Queries: svc, HTTP: aihttp.New(svc, svc), workflows: svc, svc: svc}
}

// Register adds the module's workflows to r, and the sweep that retries the
// revocations that failed (ai.revoke_sweep: one replica per tick; it does
// nothing while no gateway is connected).
func (m Module) Register(r *workflow.Registry) {
	for _, d := range m.workflows.Workflows() {
		r.Define(d)
	}
	r.Every(workflow.Schedule{Name: "ai.revoke_sweep", Every: 5 * time.Minute, Run: func(ctx context.Context) error {
		n, err := m.Commands.Sweep(ctx)
		if n > 0 {
			slog.Info("retried AI key revocations", "environments", n)
		}
		return err
	}})
}

// Schema is the module's Postgres schema and migration owner.
const Schema = aipg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return aipg.Migrations() }
