// Package inventorymodule assembles the inventory: the service over the read
// sides of the other domains and its HTTP handler. It has no schema, no
// events and no subscription: it only reads. Only the composition root
// imports it.
package inventorymodule

import (
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/inventory/adapters/inventoryhttp"
	"github.com/Abraxas-365/odyssey/internal/inventory/inventorysvc"
)

// Deps are the Queries of the domains, which satisfy the read ports as they
// are.
type Deps struct {
	Projects  inventory.Projects
	Releases  inventory.Releases
	Builds    inventory.Builds
	Fleet     inventory.Fleet
	Approvals inventory.Approvals
}

type Module struct {
	Queries inventory.Queries
	HTTP    *inventoryhttp.Handler
}

func New(deps Deps) Module {
	svc := inventorysvc.New(deps.Projects, deps.Releases, deps.Builds, deps.Fleet, deps.Approvals)
	return Module{Queries: svc, HTTP: inventoryhttp.New(svc)}
}
