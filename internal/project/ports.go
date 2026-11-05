// Package project is the root of everything a team ships on Odyssey: a
// project groups the app's environments, services, builds, releases and
// resources. Other domains reference a project by ID; this package owns its
// name, its URL slug and its lifecycle (active → archived).
package project

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Commands are the write operations, implemented by projectsvc.Service.
type Commands interface {
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (ID, error)
	Update(ctx context.Context, organization identity.OrganizationID, project ID, input Update) error
	Archive(ctx context.Context, organization identity.OrganizationID, project ID, input Archive) error
	// SetTier is IT's change of the app's tier (permission projects:tier).
	// The same tier again is a no-op.
	SetTier(ctx context.Context, organization identity.OrganizationID, project ID, input SetTier) error
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
	// Hold is Find that keeps the project as it is (SELECT … FOR SHARE) until
	// the caller's transaction ends: it cannot be archived meanwhile, so a
	// check of its status stays true while the caller writes. Call it inside
	// txn.Runner.Do.
	Hold(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
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
}
