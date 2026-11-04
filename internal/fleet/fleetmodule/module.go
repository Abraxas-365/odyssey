// Package fleetmodule assembles the fleet domain: repository, service,
// certificate authority, HTTP handler and the gateway's gRPC server. Only
// the composition root imports it, and only it picks the adapters.
package fleetmodule

import (
	"io/fs"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetgrpc"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleethttp"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetpg"
	"github.com/Abraxas-365/odyssey/internal/fleet/fleetsvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB     *sqlx.DB
	Events event.Publisher
	// Authority signs (the gateway) or only vouches for the pin (the API).
	// Nil: the fleet is not configured (clusters cannot be registered).
	Authority fleet.Authority
	// Gateway is the host:port agents dial (in enrolment tokens).
	Gateway string
	// CertificateTTL overrides the agents' certificate lifetime (tests).
	CertificateTTL time.Duration
}

type Module struct {
	Commands fleet.Commands
	Queries  fleet.Queries
	Dispatch fleet.Dispatch
	Builds   fleet.Builds
	// Resources is what the resource domain's resourcefleet adapter uses.
	Resources fleet.Resources
	Agents    fleet.Agents
	HTTP      *fleethttp.Handler
}

func New(deps Deps) Module {
	ca := deps.Authority
	if ca == nil {
		ca = fleetca.Disabled{}
	}
	svc := fleetsvc.New(fleetpg.New(deps.DB), ca, txn.Postgres{DB: deps.DB}, deps.Events,
		fleetsvc.Options{Gateway: deps.Gateway, CertificateTTL: deps.CertificateTTL})
	return Module{Commands: svc, Queries: svc, Dispatch: svc, Builds: svc, Resources: svc, Agents: svc, HTTP: fleethttp.New(svc, svc)}
}

// LoadAuthority reads the fleet CA (keyFile "" for verify-only).
func LoadAuthority(certFile, keyFile string) (*fleetca.Authority, error) {
	return fleetca.Load(certFile, keyFile)
}

// Gateway is the gRPC server agents connect to; changes are signalled
// through Postgres at listenURL. sources is the build domain's blob store
// (where FetchSource reads; nil: this gateway serves no build sources).
func (m Module) Gateway(listenURL string, sources blob.Store, opts fleetgrpc.Options) *fleetgrpc.Server {
	return fleetgrpc.New(m.Agents, fleetpg.NewSignals(listenURL), sources, opts)
}

// Schema is the module's Postgres schema and migration owner.
const Schema = fleetpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return fleetpg.Migrations() }
