// Package projectmodule assembles the project domain: repository, service
// and HTTP handler. Only the composition root imports it.
package projectmodule

import (
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectfleet"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projecthttp"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectpg"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB     *sqlx.DB
	Events event.Publisher
}

type Module struct {
	Commands project.Commands
	Queries  project.Queries
	HTTP     *projecthttp.Handler
	svc      *projectsvc.Service
}

func New(deps Deps) Module {
	svc := projectsvc.New(projectpg.New(deps.DB), txn.Postgres{DB: deps.DB}, deps.Events)
	return Module{Commands: svc, Queries: svc, HTTP: projecthttp.New(svc, svc), svc: svc}
}

// UseFleet wires the clusters' capabilities (the fleet module is built after
// this one): an app with several services needs clusters that run them.
func (m Module) UseFleet(f fleet.Dispatch) { m.svc.UseRuntime(projectfleet.New(f)) }

// UsePolicy wires IT's rules (the policy module is built after this one):
// a change of the app's services is judged.
func (m Module) UsePolicy(p project.Policy) { m.svc.UsePolicy(p) }

// UseApprovals wires where a held change of services waits for IT (the
// approval module is built last).
func (m Module) UseApprovals(a project.Approvals) { m.svc.UseApprovals(a) }

// Schema is the module's Postgres schema and migration owner.
const Schema = projectpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return projectpg.Migrations() }
