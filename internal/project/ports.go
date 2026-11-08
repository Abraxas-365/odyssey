// Package project is the root of everything a team ships on Odyssey: a
// project groups the app's environments, services, builds, releases and
// resources. Other domains reference a project by ID; this package owns its
// name, its URL slug and its lifecycle (active → archived).
package project

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Commands are the write operations, implemented by projectsvc.Service.
type Commands interface {
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (ID, error)
	Update(ctx context.Context, organization identity.OrganizationID, project ID, input Update) error
	Archive(ctx context.Context, organization identity.OrganizationID, project ID, input Archive) error
	// SetTier is IT's change of the app's tier (permission projects:tier).
	// The same tier again is a no-op. A tier where a resource holding data
	// would have no offering of its recipe is refused (409
	// CodeTierResources, TierGuard): IT offers the recipe there first.
	SetTier(ctx context.Context, organization identity.OrganizationID, project ID, input SetTier) error
	// SetServices replaces the app's services (the next version of the app
	// takes them). The same list again is a no-op. A list that is not Simple
	// is refused with Business CodeServicesUnavailable unless the cluster of
	// every environment can run it (Runtime). IT's rules judge the change
	// (gate.ActionSetServices, the whole app: no environment) before it is
	// stored; held for approval, nothing changes (ServicesChange.Held).
	SetServices(ctx context.Context, organization identity.OrganizationID, project ID, input SetServices) (ServicesChange, error)
}

// Policy is what project asks IT's rules (the policyproject adapter; project
// never imports policy): may the app's services change like this?
type Policy interface {
	gate.Checker
}

// Approvals is where project holds a change of services IT's rules want
// approved (the approvalproject adapter; project never imports approval).
// The approved change re-runs as the requester with the approval's mark on
// ctx and redeems it (gate.Approver).
type Approvals interface {
	gate.Approver
}

// Runtime is what the project asks of where its environments run (the
// fleet, through adapter projectfleet): whether their clusters can run an
// app made of several services.
type Runtime interface {
	// CanRunServices is true when the cluster of every environment (or, for
	// one not placed yet, the organization's default) lists the services
	// capability; false when one cannot, or there is no cluster.
	CanRunServices(ctx context.Context, organization identity.OrganizationID, environments []EnvironmentID) (bool, error)
}

// TierGuard is what project asks the resources before the app changes tier
// (the resourceproject adapter; the resource module is built after this
// one, so it is wired late): the names of the resources holding data whose
// recipe is not offered in tier. Moving there would leave them outside the
// catalog.
type TierGuard interface {
	Stranded(ctx context.Context, organization identity.OrganizationID, project ID, tier Tier) ([]string, error)
}

// Queries are the read operations, implemented by projectsvc.Service.
type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	// FindBySlug is Find by the URL slug (unique per organization; archived
	// projects keep theirs): how agents and people name an app.
	FindBySlug(ctx context.Context, organization identity.OrganizationID, slug string) (Project, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Project], error)
	FindEnvironment(ctx context.Context, organization identity.OrganizationID, project ID, environment EnvironmentID) (Environment, error)
	// FindEnvironmentBySlug resolves "production" (or another slug) of a project.
	FindEnvironmentBySlug(ctx context.Context, organization identity.OrganizationID, project ID, slug string) (Environment, error)
	// ListEnvironments returns the project's environments, production first.
	ListEnvironments(ctx context.Context, organization identity.OrganizationID, project ID, filter EnvironmentFilter) ([]Environment, error)
	// FindMany reads several projects at once (the ones that exist in the
	// organization; any other id is simply absent): one query for a read
	// model that would otherwise ask once per app.
	FindMany(ctx context.Context, organization identity.OrganizationID, projects []ID) ([]Project, error)
	// EnvironmentsOf reads the environments of several projects at once, in
	// the order of ListEnvironments (project, then production first).
	EnvironmentsOf(ctx context.Context, organization identity.OrganizationID, projects []ID) ([]Environment, error)
	// Hold is Find that keeps the project as it is (SELECT … FOR SHARE) until
	// the caller's transaction ends: it cannot be archived meanwhile, so a
	// check of its status stays true while the caller writes. Call it inside
	// txn.Runner.Do.
	Hold(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	// Services are the app's services, in their order (at least one).
	Services(ctx context.Context, organization identity.OrganizationID, project ID) ([]Service, error)
}

// Repository is the persistence contract; only the service depends on it.
// It receives IDs, never generates them, trusts validated input and filters
// every statement by organization (another tenant's project is NotFound).
type Repository interface {
	Create(ctx context.Context, organization identity.OrganizationID, project ID, input Create) error
	Find(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	FindBySlug(ctx context.Context, organization identity.OrganizationID, slug string) (Project, error)
	// FindForUpdate is Find that locks the row until the transaction in ctx
	// ends (SELECT … FOR UPDATE); call it inside txn.Runner.Do.
	FindForUpdate(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	// FindForShare is Find that keeps the row from changing (SELECT … FOR
	// SHARE) until the transaction in ctx ends; call it inside txn.Runner.Do.
	FindForShare(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Project], error)
	Update(ctx context.Context, organization identity.OrganizationID, project ID, input Update) error
	Archive(ctx context.Context, organization identity.OrganizationID, project ID, input Archive) error
	SetTier(ctx context.Context, organization identity.OrganizationID, project ID, input SetTier) error
	CreateEnvironment(ctx context.Context, environment Environment) error
	FindEnvironment(ctx context.Context, organization identity.OrganizationID, project ID, environment EnvironmentID) (Environment, error)
	FindEnvironmentBySlug(ctx context.Context, organization identity.OrganizationID, project ID, slug string) (Environment, error)
	ListEnvironments(ctx context.Context, organization identity.OrganizationID, project ID, filter EnvironmentFilter) ([]Environment, error)
	FindMany(ctx context.Context, organization identity.OrganizationID, projects []ID) ([]Project, error)
	ListEnvironmentsOf(ctx context.Context, organization identity.OrganizationID, projects []ID) ([]Environment, error)
	// ListServices returns the project's services by position (none for
	// another tenant's project).
	ListServices(ctx context.Context, organization identity.OrganizationID, project ID) ([]Service, error)
	// ReplaceServices stores services as the project's whole list.
	ReplaceServices(ctx context.Context, organization identity.OrganizationID, project ID, services []Service) error
}
