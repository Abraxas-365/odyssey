// Package projectmodule assembles the project domain: repository, service
// and HTTP handler. Only the composition root imports it.
package projectmodule

import (
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/project"
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
}

func New(deps Deps) Module {
	svc := projectsvc.New(projectpg.New(deps.DB), txn.Postgres{DB: deps.DB}, deps.Events)
	return Module{Commands: svc, Queries: svc, HTTP: projecthttp.New(svc, svc)}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = projectpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return projectpg.Migrations() }
