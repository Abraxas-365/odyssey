// Package releasemodule assembles the release domain: repository, service,
// deploy workflow, HTTP handler and the Cluster adapter. Only the
// composition root imports it, and only it picks the Cluster adapter.
package releasemodule

import (
	"fmt"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasebuild"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefleet"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasehttp"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB       *sqlx.DB
	Events   event.Publisher
	Projects project.Queries
	// Engine starts deploy runs. The composition root passes a
	// workflow.Deferred: the engine is built after this module's workflows
	// are registered.
	Engine workflow.Engine
	// Cluster runs the apps; tests pass a configured fake. Nil picks the
	// adapter from ClusterConfig.
	Cluster release.Cluster
	// ClusterConfig selects the adapter when Cluster is nil (the zero value
	// is the in-memory fake: the API, which never calls the cluster).
	ClusterConfig config.Cluster
	// Fleet runs apps on the organization's enrolled clusters (the fleet
	// adapter).
	Fleet fleet.Dispatch
	// Builds build deployments from source (the build module's ports).
	BuildCommands build.Commands
	BuildQueries  build.Queries
	Options       releasesvc.Options
}

type Module struct {
	Commands  release.Commands
	Queries   release.Queries
	HTTP      *releasehttp.Handler
	workflows release.Workflows
}

func New(deps Deps) (Module, error) {
	cluster, err := clusterOf(deps)
	if err != nil {
		return Module{}, err
	}
	if deps.BuildCommands == nil || deps.BuildQueries == nil {
		return Module{}, fmt.Errorf("the release module needs the build module")
	}
	svc := releasesvc.New(releasepg.New(deps.DB), deps.Projects, cluster,
		releasebuild.New(deps.BuildCommands, deps.BuildQueries), deps.Engine,
		txn.Postgres{DB: deps.DB}, deps.Events, deps.Options)
	return Module{Commands: svc, Queries: svc, HTTP: releasehttp.New(svc, svc), workflows: svc}, nil
}

// clusterOf picks the Cluster adapter.
func clusterOf(deps Deps) (release.Cluster, error) {
	if deps.Cluster != nil {
		return deps.Cluster, nil
	}
	switch deps.ClusterConfig.Kind {
	case "", config.ClusterFake:
		return releasefake.New(releasefake.Options{}), nil
	case config.ClusterFleet:
		if deps.Fleet == nil {
			return nil, fmt.Errorf("the fleet cluster adapter needs the fleet module")
		}
		return releasefleet.New(deps.Fleet), nil
	}
	return nil, fmt.Errorf("unknown cluster kind %q", deps.ClusterConfig.Kind)
}

// Register adds the module's workflows (the deploy workflow) to r.
func (m Module) Register(r *workflow.Registry) {
	for _, d := range m.workflows.Workflows() {
		r.Define(d)
	}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = releasepg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return releasepg.Migrations() }
