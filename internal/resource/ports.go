package resource

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Commands are the write operations, implemented by resourcesvc.Service.
type Commands interface {
	// Create adds the resource, has the environment's cluster prepare it and,
	// when input.Connect is set, connects it to the app (a config deployment
	// when something is online). The catalog decides what fulfils it (recipe,
	// version, parameters; docs/catalog.md): outside the hard limits it is
	// refused (422), outside the pre-approved range it needs IT's approval
	// (202), as when IT's rules ask (create_resource, and connect_resource
	// when it connects: one request for the whole operation). A cluster that
	// cannot prepare the recipe refuses it (Business RESOURCES_UNAVAILABLE or
	// CATALOG_UNAVAILABLE) and nothing is stored.
	Create(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Create) (Result, error)
	// Change changes the resource's parameters within its recipe@version
	// (a size only grows; the same values change nothing), judged like Create
	// under change_resource.
	Change(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, input Change) (View, error)
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
	// Named are the resources of the environment with those names and how
	// the cluster sees each (the caller already placed the environment): one
	// store read and one Observed per name, no Kinds, no connections.
	// Names that are not the environment's resources are left out. Views
	// carry no connection (Connected is false).
	Named(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, names []string) ([]View, error)
	// Find is the stored resource of the environment (what release asks).
	Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (Resource, error)
	// Stranded are the resources of the project holding data whose pinned
	// recipe is not offered in tier (what moving the app there would leave
	// without an offering; the resourceproject adapter answers project with
	// it).
	Stranded(ctx context.Context, organization identity.OrganizationID, project project.ID, tier project.Tier) ([]Resource, error)
	// Offered is the catalog as the environment sees it for asker: the
	// recipes its app's tier is offered (to the asker's teams) and its
	// cluster can honour, each with its limits, the variables the app gets
	// and the verdict IT's rules give the caller (on ctx) for creating it
	// with its defaults; the kinds nothing fulfils, with why. It writes
	// nothing.
	Offered(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, asker identity.UserID) (Offered, error)
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
	// ListProject returns the resources of every environment of the project,
	// oldest first.
	ListProject(ctx context.Context, organization identity.OrganizationID, project project.ID) ([]Resource, error)
	// Count is how many resources the environment has (deleting included).
	Count(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (int, error)
	// Change stores the new parameters (and storage_gi, params' size) at a
	// new generation. A size never goes down.
	Change(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string, params map[string]int, generation int64) error
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
	// Capabilities are what the environment's cluster announced it can
	// honour (resourcespec capabilities), what the catalog matches a recipe's
	// Requires against.
	Capabilities(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]string, error)
}

// Catalog is what resource asks the catalog (catalog.Queries, wired as is:
// catalog depends on no other domain, so resource imports its root).
type Catalog interface {
	Resolve(ctx context.Context, organization identity.OrganizationID, request catalog.Request) (catalog.Resolved, error)
	ResolveChange(ctx context.Context, organization identity.OrganizationID, change catalog.Change) (catalog.Resolved, error)
	List(ctx context.Context, organization identity.OrganizationID) ([]catalog.Entry, error)
	TeamsOf(ctx context.Context, organization identity.OrganizationID, user identity.UserID) ([]string, error)
	// Version is a resource's pinned recipe version as its organization
	// knows it (a factory version or one it published).
	Version(ctx context.Context, organization identity.OrganizationID, recipe string, version int) (catalog.RecipeVersion, bool, error)
}

// Policy is what resource asks IT's rules (the policyresource adapter;
// resource never imports policy): may this resource be created, changed or
// deleted? Connecting an existing resource is judged where it is applied,
// in release (release.Policy); a create that connects asks release for that
// verdict (Usage.JudgeConnect) and settles both as one request.
type Policy interface {
	// Check decides the request for the organization, as the actor on ctx.
	Check(ctx context.Context, organization identity.OrganizationID, request gate.Request) (gate.Verdict, error)
}

// Approvals is where resource holds an action IT's rules want approved (the
// approvalresource adapter; resource never imports approval). The approved
// action re-runs as the requester with the approval's mark on ctx and
// redeems it (gate.Approver).
type Approvals interface {
	gate.Approver
}

// Usage is how the environment's app uses resources (the resourcerelease
// adapter): release stores the connections in the settings it deploys.
type Usage interface {
	// Connections are the resources the app is connected to.
	Connections(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) ([]Connection, error)
	// Connect connects the resource (409 VARIABLE_TAKEN when a variable it
	// defines is taken); Disconnect removes the connection.
	Connect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, l Link, by identity.UserID) (Applied, error)
	Disconnect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, name string, by identity.UserID) (Applied, error)
	// JudgeConnect checks the services a connection would reach and returns
	// IT's rules' verdict on making it, changing nothing (not even an
	// approval request): Create combines it with its own verdicts, settles
	// the whole operation once and then connects with Link.Judged.
	JudgeConnect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, ask Ask) (gate.Verdict, error)
	// WithConnectionsLocked runs fn holding the lock connecting takes, with
	// the connections at that moment: a deletion that checks "not in use"
	// inside fn cannot race a connection.
	WithConnectionsLocked(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID,
		fn func(ctx context.Context, connected []Connection) error) error
}
