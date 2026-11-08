// Package catalogmodule assembles the catalog domain: repository, service
// and HTTP handler. Only the composition root imports it.
package catalogmodule

import (
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/cataloghttp"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/catalogpg"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogsvc"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB     *sqlx.DB
	Events event.Publisher
	// Directory reads the organization's teams (catalogiam; nil: offerings
	// cannot name teams).
	Directory catalog.Directory
}

type Module struct {
	Commands catalog.Commands
	Queries  catalog.Queries
	HTTP     *cataloghttp.Handler
}

func New(deps Deps) Module {
	svc := catalogsvc.New(catalogpg.New(deps.DB), txn.Postgres{DB: deps.DB}, deps.Events)
	if deps.Directory != nil {
		svc.UseDirectory(deps.Directory)
	}
	return Module{Commands: svc, Queries: svc, HTTP: cataloghttp.New(svc, svc)}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = catalogpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return catalogpg.Migrations() }
