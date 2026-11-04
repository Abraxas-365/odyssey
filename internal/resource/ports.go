package resource

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Commands are the write operations, implemented by resourcesvc.Service.
type Commands interface {
	// Create adds the resource, has the environment's cluster prepare it and,
	// when input.Connect is set, connects it to the app (a config deployment
	// when something is online). A cluster that cannot prepare the kind
	// refuses it (Business RESOURCES_UNAVAILABLE) and nothing is stored.
	Create(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Create) (Result, error)
	// Resize grows the resource (it never shrinks). The same size changes nothing.
	Resize(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, input Resize) (View, error)
	// Delete has the cluster erase the resource and its data. A resource the
	// app uses is refused (409 RESOURCE_IN_USE): disconnect it first. The row
	// stays, as deleting, until the cluster confirms (Forget).
	Delete(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, input Delete) (View, error)
	// Connect connects the resource to the environment's app (changing the
	// prefix of an existing connection).
	Connect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Connect) (Result, error)
	// Disconnect stops the app from receiving the resource; it keeps existing.
	Disconnect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, by identity.UserID) (Result, error)
	// Forget drops the row of a deleting resource the cluster confirmed gone
	// (the composition root calls it on cluster.resource_removed). Anything
	// else is left alone; forgetting twice is fine.
	Forget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) error
}

// Queries are the read operations, implemented by resourcesvc.Service.
type Queries interface {
	// List is the environment's resources with their state, oldest first.
	List(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Listing, error)
	// Get is one resource with its state (NotFound when it is not the environment's).
	Get(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string) (View, error)
	// Find is the stored resource of the environment (what release asks).
	Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Resource, error)
}

// Repository is the persistence contract; only the service depends on it.
// Every statement filters by organization (another tenant's resource is
// NotFound).
type Repository interface {
	// LockEnvironment serializes the writers that count an environment's
	// resources (Create) until the transaction in ctx ends.
	LockEnvironment(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) error
	// Create stores the resource; a name taken in the environment is a Conflict.
	Create(ctx context.Context, resource Resource) error
	Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Resource, error)
	// FindForUpdate is Find that locks the row until the transaction ends.
	FindForUpdate(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Resource, error)
	// List returns the environment's resources, oldest first.
	List(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]Resource, error)
	// Count is how many resources the environment has (deleting included).
	Count(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (int, error)
	// Resize stores a bigger size and the new generation.
	Resize(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string, storageGi int, generation int64) error
	// MarkDeleting sets the resource deleting at a new generation.
	MarkDeleting(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string, generation int64) error
	// Forget deletes the row when (and only when) it is deleting, returning it.
	Forget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Resource, bool, error)
}

// Cluster is where resources run (the resourcefleet adapter).
type Cluster interface {
	// Apply stores the desired resource for the environment's cluster and has
	// it sent. It joins the caller's transaction and fails closed with
	// Business RESOURCES_UNAVAILABLE when the cluster cannot prepare the kind.
	Apply(ctx context.Context, desired Desired) error
	// Observed is what the cluster last reported (an empty Phase: nothing yet).
	Observed(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Observed, error)
	// Kinds are the kinds the environment's cluster can prepare.
	Kinds(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]resourcekind.Kind, error)
}

// Usage is how the environment's app uses resources (the resourcerelease
// adapter): release stores the connections in the settings it deploys.
type Usage interface {
	// Connections are the resources the app is connected to.
	Connections(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) ([]Connection, error)
	// Connect connects the resource (409 VARIABLE_TAKEN when a variable it
	// defines is taken); Disconnect removes the connection.
	Connect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, c Connection, by identity.UserID) (Applied, error)
	Disconnect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, by identity.UserID) (Applied, error)
	// WithConnectionsLocked runs fn holding the lock connecting takes, with
	// the connections at that moment: a deletion that checks "not in use"
	// inside fn cannot race a connection.
	WithConnectionsLocked(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID,
		fn func(ctx context.Context, connected []Connection) error) error
}
