// Package buildmodule assembles the build domain: repository, service, build
// workflows, HTTP handler and the Runner adapter. Only the composition root
// imports it, and only it picks the Runner adapter.
package buildmodule

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildhttp"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildpg"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB       *sqlx.DB
	Events   event.Publisher
	Projects project.Queries
	// Blobs stores sources and archived logs.
	Blobs blob.Store
	// Engine starts build runs (a workflow.Deferred, like release's).
	Engine workflow.Engine
	// Runner builds the images; tests pass a configured fake. Nil picks the
	// adapter from BuilderConfig.
	Runner        build.Runner
	BuilderConfig config.Builder
	Options       buildsvc.Options
}

type Module struct {
	Commands  build.Commands
	Queries   build.Queries
	HTTP      *buildhttp.Handler
	workflows build.Workflows
	commands  build.Commands
}

func New(deps Deps) (Module, error) {
	if deps.Blobs == nil {
		return Module{}, fmt.Errorf("the build module needs a blob store")
	}
	runner, err := runnerOf(deps)
	if err != nil {
		return Module{}, err
	}
	svc := buildsvc.New(buildpg.New(deps.DB), deps.Projects, runner, deps.Blobs, deps.Engine,
		txn.Postgres{DB: deps.DB}, deps.Events, deps.Options)
	return Module{Commands: svc, Queries: svc, HTTP: buildhttp.New(svc), workflows: svc, commands: svc}, nil
}

// runnerOf picks the Runner adapter.
func runnerOf(deps Deps) (build.Runner, error) {
	if deps.Runner != nil {
		return deps.Runner, nil
	}
	switch deps.BuilderConfig.Kind {
	case "", config.BuilderFake:
		// A few seconds, so the console shows the build's steps and log; a
		// source named "…unsupported…" fails (scripts/smoke.sh).
		return buildfake.New(buildfake.Options{Duration: 8 * time.Second, Behavior: buildfake.ByName}), nil
	case config.BuilderNone:
		// No builder yet (a fleet worker before phase 1b-2): every build is
		// refused, and the deployment that asked for it fails with NO_BUILDER.
		return buildfake.New(buildfake.Options{Behavior: func(build.Job) buildfake.Outcome {
			return buildfake.Outcome{Refuse: build.CodeNoBuilder}
		}}), nil
	}
	return nil, fmt.Errorf("unknown builder kind %q", deps.BuilderConfig.Kind)
}

// Register adds the build workflows and the source sweep to r.
func (m Module) Register(r *workflow.Registry) {
	for _, d := range m.workflows.Workflows() {
		r.Define(d)
	}
	r.Every(workflow.Schedule{Name: "build.sweep", Every: time.Hour, Run: func(ctx context.Context) error {
		n, err := m.commands.Sweep(ctx)
		if n > 0 {
			slog.Info("swept build sources", "count", n)
		}
		return err
	}})
}

// Schema is the module's Postgres schema and migration owner.
const Schema = buildpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return buildpg.Migrations() }
