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
}

// Queries are the read operations, implemented by projectsvc.Service.
type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Project], error)
}

// Repository is the persistence contract; only the service depends on it.
// It receives IDs, never generates them, trusts validated input and filters
// every statement by organization (another tenant's project is NotFound).
type Repository interface {
	Create(ctx context.Context, organization identity.OrganizationID, project ID, input Create) error
	Find(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	// FindForUpdate is Find that locks the row until the transaction in ctx
	// ends (SELECT … FOR UPDATE); call it inside txn.Runner.Do.
	FindForUpdate(ctx context.Context, organization identity.OrganizationID, project ID) (Project, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Project], error)
	Update(ctx context.Context, organization identity.OrganizationID, project ID, input Update) error
	Archive(ctx context.Context, organization identity.OrganizationID, project ID, input Archive) error
}
