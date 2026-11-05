// Package resourcemodule assembles the resource domain: repository, service,
// HTTP handler and the cross-domain adapters. Only the composition root
// imports it.
package resourcemodule

import (
	"context"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcefleet"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcehttp"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcepg"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcerelease"
	"github.com/Abraxas-365/odyssey/internal/resource/resourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB       *sqlx.DB
	Events   event.Publisher
	Projects project.Queries
	// Fleet runs the resources on the environment's cluster.
	Fleet fleet.Resources
	// The release module's ports: connections live in its settings.
	ReleaseCommands release.Commands
	ReleaseQueries  release.Queries
	ReleaseUsage    release.Usage
	// Policy is IT's rules for deleting (the policyresource adapter).
	Policy resource.Policy
}

type Module struct {
	Commands resource.Commands
	Queries  resource.Queries
	HTTP     *resourcehttp.Handler
	svc      *resourcesvc.Service
}

// UseApprovals wires where resource holds what a rule wants approved (the
// approvalresource adapter; the approval module is built after this one).
func (m Module) UseApprovals(a resource.Approvals) { m.svc.UseApprovals(a) }

func New(deps Deps) Module {
	svc := resourcesvc.New(resourcepg.New(deps.DB), resourcefleet.New(deps.Fleet),
		resourcerelease.New(deps.ReleaseCommands, deps.ReleaseQueries, deps.ReleaseUsage),
		deps.Projects, txn.Postgres{DB: deps.DB}, deps.Events, deps.Policy)
	return Module{Commands: svc, Queries: svc, HTTP: resourcehttp.New(svc, svc), svc: svc}
}

// ForgetOnRemoved is the subscription that forgets a resource once its
// cluster erased it.
const ForgetOnRemoved = "resource.forget_on_removed"

// Subscribe wires who the resource domain reacts to. Only the composition
// root calls it, before the bus is sealed.
func (m Module) Subscribe(bus *event.Bus) {
	bus.Add(event.Subscription{Name: ForgetOnRemoved, Types: []string{fleet.EventResourceRemoved}, Handle: m.removed})
}

// removed is the handler of cluster.resource_removed: the cluster erased a
// deleting resource, so the resource domain forgets it. The composition root
// subscribes it.
func (m Module) removed(ctx context.Context, e event.Event) error {
	if e.Version != 1 {
		return nil
	}
	p, err := event.Decode[fleet.ResourceRemoved](e)
	if err != nil {
		return err
	}
	env, err := project.ParseEnvironmentID(p.Environment)
	if err != nil {
		return err
	}
	return m.Commands.Forget(ctx, e.Organization, env, p.Name)
}

// Schema is the module's Postgres schema and migration owner.
const Schema = resourcepg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return resourcepg.Migrations() }
