package catalog

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Commands are IT's edits of the organization's catalog, implemented by
// catalogsvc.Service. The caller needs odyssey:catalog:manage (the HTTP
// adapter gates it).
type Commands interface {
	// UpdateOffering replaces what the organization offers of recipe. The
	// version must be one of the recipe's; a tier where another offering of
	// the kind is already the default is 409 CATALOG_DEFAULT_TAKEN. It is
	// not retroactive: resources keep their recipe@version and parameters.
	UpdateOffering(ctx context.Context, organization identity.OrganizationID, recipe string, input UpdateOffering) (Entry, error)
	// RegisterRecipe publishes version 1 of a recipe of the organization's
	// own (a resource the company already has) with an inactive offering:
	// nobody can create it until IT offers it. An id already taken is 409.
	// A connection that reads secret stores needs input.MayReadStores (403)
	// and stores some cluster of the organization announces (422
	// CATALOG_STORE_UNKNOWN).
	RegisterRecipe(ctx context.Context, organization identity.OrganizationID, input RegisterRecipe) (Entry, error)
	// PublishVersion publishes the next version of the organization's recipe
	// (same kind and engine; the stores as in RegisterRecipe). It is not
	// retroactive and not offered until IT moves the offering to it.
	PublishVersion(ctx context.Context, organization identity.OrganizationID, recipe string, input PublishVersion) (Entry, error)
}

// Queries read the catalog. The first read of an organization seeds the
// factory offerings it lacks (lazy and idempotent, like IT's built-in rules).
type Queries interface {
	// List is every recipe the organization knows (the factory's and its
	// own; its offered version, or the latest when not offered) with the
	// organization's offering.
	List(ctx context.Context, organization identity.OrganizationID) ([]Entry, error)
	// Find is one recipe with the organization's offering (404 unknown).
	Find(ctx context.Context, organization identity.OrganizationID, recipe string) (Entry, error)
	// Available are the entries offered in tier to the asker's teams, those
	// the capabilities can honour, in kind order with each kind's default
	// first.
	Available(ctx context.Context, organization identity.OrganizationID, tier project.Tier, capabilities []string, asker identity.UserID) ([]Entry, error)
	// Teams are the organization's teams an offering can be limited to
	// (Available false: Odicea has no IAMKit directory to read them).
	Teams(ctx context.Context, organization identity.OrganizationID) (Teams, error)
	// TeamsOf are the ids of the teams user is in, read only when an active
	// offering is limited to teams (nil otherwise, and without a directory:
	// such offerings are then nobody's).
	TeamsOf(ctx context.Context, organization identity.OrganizationID, user identity.UserID) ([]string, error)
	// Resolve picks the recipe for the request (its own, or the kind's
	// default in the tier for the asker's teams), checks it is offered to the
	// asker and the cluster can honour it, and normalizes the parameters
	// against the offering. Outside the hard limits is 422
	// CATALOG_OUT_OF_LIMITS; outside the pre-approved range is
	// Resolved.OutOfRange.
	Resolve(ctx context.Context, organization identity.OrganizationID, request Request) (Resolved, error)
	// ResolveChange checks a change of a resource's parameters against its
	// pinned recipe@version as offered now: an immutable parameter that
	// changes, or one that only grows going down, is refused. Only the
	// values that change are judged (limits, pre-approved range); the rest
	// stay as they are (a parameter the resource lacks takes its default,
	// which is not a change). Change.Tier is not consulted: an offering
	// withdrawn from the tier does not stop changing what exists.
	ResolveChange(ctx context.Context, organization identity.OrganizationID, change Change) (Resolved, error)
	// Version is recipe@version as the organization knows it: a factory
	// version or one it published (false: neither). It is how a resource's
	// pinned version is read; another organization's recipe is never found.
	Version(ctx context.Context, organization identity.OrganizationID, recipe string, version int) (RecipeVersion, bool, error)
	// Stores are the secret stores the organization's clusters announce
	// (active clusters only), by name, with which clusters read each: what
	// a recipe of its own may read from. Empty without a Fleet.
	Stores(ctx context.Context, organization identity.OrganizationID) ([]Store, error)
}

// Repository is the persistence contract of catalog.offerings and
// catalog.recipe_versions; only the service depends on it. Every statement
// filters by organization.
type Repository interface {
	List(ctx context.Context, organization identity.OrganizationID) ([]Offering, error)
	Find(ctx context.Context, organization identity.OrganizationID, recipe string) (Offering, error)
	// LockCatalog serializes writers of the organization's offerings (the
	// one-default-per-tier check stays true) until the transaction ends.
	LockCatalog(ctx context.Context, organization identity.OrganizationID) error
	// Update stores the offering's draft and moves its revision up by one.
	Update(ctx context.Context, offering Offering) error
	// SeedFactory inserts the offerings whose recipe the organization lacks
	// (ON CONFLICT DO NOTHING) and returns how many it added.
	SeedFactory(ctx context.Context, offerings []Offering) (int, error)
	// Publish stores a version of the organization's recipe. A published
	// version is never changed or removed (the table refuses it); one that
	// already exists is a conflict.
	Publish(ctx context.Context, recipe OrgRecipe) error
	// Published are the organization's versions, by recipe then version.
	Published(ctx context.Context, organization identity.OrganizationID) ([]OrgRecipe, error)
	// AddOffering inserts the offering of a recipe the organization just
	// registered; one that exists is a conflict.
	AddOffering(ctx context.Context, offering Offering) error
}

// Fleet is what the catalog asks of the organization's clusters (the
// catalogfleet adapter; catalog never imports fleet): the secret stores they
// read from. Optional: without it no recipe can read a store.
type Fleet interface {
	SecretStores(ctx context.Context, organization identity.OrganizationID) ([]Store, error)
}

// Directory is the organization's teams (IAMKit groups), read by the
// catalogiam adapter as Odicea's service account. Optional: without it no
// offering can name teams.
type Directory interface {
	// Teams are the organization's groups.
	Teams(ctx context.Context, organization identity.OrganizationID) ([]Team, error)
	// TeamsOf are the ids of the groups user is in.
	TeamsOf(ctx context.Context, organization identity.OrganizationID, user identity.UserID) ([]string, error)
}
