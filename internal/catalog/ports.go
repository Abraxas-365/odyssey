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
}

// Queries read the catalog. The first read of an organization seeds the
// factory offerings it lacks (lazy and idempotent, like IT's built-in rules).
type Queries interface {
	// List is every recipe the binary knows (its offered version, or the
	// latest when not offered) with the organization's offering.
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
}

// Repository is the persistence contract of catalog.offerings; only the
// service depends on it. Every statement filters by organization.
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
