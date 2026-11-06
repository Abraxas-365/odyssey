package inventory

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Queries are what inventory answers, implemented by inventorysvc.Service.
// Everything reads on behalf of the actor on the context (REST or MCP put it
// there); who may ask is the transport's permission check.
type Queries interface {
	// List is the inventory: one Row per environment of every active app of
	// the organization, in app-name order, filtered and paged. The rows of the
	// page carry what the app uses; with NeedsAttention (and for a CSV) the
	// uses of every candidate are read, a few at a time (UsesWorkers).
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (Page, error)
	// Export is every row of the inventory that matches (at most MaxApps
	// apps; Truncated says there were more): the CSV. Items, Truncated and
	// Unavailable are the Page's fields it fills.
	Export(ctx context.Context, organization identity.OrganizationID, filter Filter) (Page, error)
	// Attention is the list "Necesita tu atención" for the actor on ctx:
	// TI's items for whoever holds the permissions that watch them, and the
	// actor's own apps and requests for everyone.
	Attention(ctx context.Context, organization identity.OrganizationID) (Attention, error)
	// NeedingAttention says, for each of the apps (at most MaxApps), whether
	// any of its environments needs somebody (a last deployment that failed
	// or critical vulnerabilities; never what the uses list: that is
	// Attention's). Scoped to the actor on ctx like Attention: TI
	// (inventory:read) judges every app, anyone else only the apps they
	// created. An app that cannot be judged, or that the actor is not told
	// about, is absent.
	NeedingAttention(ctx context.Context, organization identity.OrganizationID, apps []project.ID) (map[project.ID]bool, error)
	// States are how the apps stand in their production environment (at
	// most MaxStates; unknown ids are absent), read in batches: status, URL,
	// who can open them and where they run. Anyone who reads the apps may
	// ask (it says what the app page says).
	States(ctx context.Context, organization identity.OrganizationID, apps []project.ID) (States, error)
}

// The ports below are the read sides of the domains inventory composes. Each
// is a subset of that domain's Queries, so the composition root passes the
// Queries as they are and inventory can neither write nor see anything else
// (architecture test TestInventoryIsReadOnly).

// Projects are the apps and their environments.
type Projects interface {
	List(ctx context.Context, organization identity.OrganizationID, filter project.Filter, page query.Pagination) (query.Paginated[project.Project], error)
	EnvironmentsOf(ctx context.Context, organization identity.OrganizationID, projects []project.ID) ([]project.Environment, error)
	FindMany(ctx context.Context, organization identity.OrganizationID, projects []project.ID) ([]project.Project, error)
}

// Releases are what is online and what each app uses.
type Releases interface {
	Serving(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) ([]release.Serving, error)
	Uses(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (release.Uses, error)
}

// Builds are the scans of the images.
type Builds interface {
	Scans(ctx context.Context, organization identity.OrganizationID, builds []build.ID) ([]build.BuildScan, error)
}

// Fleet are the clusters.
type Fleet interface {
	List(ctx context.Context, organization identity.OrganizationID, filter fleet.Filter, page query.Pagination) (query.Paginated[fleet.Cluster], error)
	Placements(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (map[project.EnvironmentID]fleet.ClusterID, error)
	Egress(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID) (fleet.EgressStatus, error)
}

// Approvals are the requests to TI, read as the viewer they are asked for.
type Approvals interface {
	List(ctx context.Context, organization identity.OrganizationID, viewer approval.Viewer, filter approval.Filter, page query.Pagination) (query.Paginated[approval.View], error)
}
