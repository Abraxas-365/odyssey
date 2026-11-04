// Package releaseresource is the release.Resources adapter on the resource
// domain: release asks whether a resource exists and is usable through
// resource's Queries port only (never resourcesvc).
package releaseresource

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/resource"
)

type Resources struct{ queries resource.Queries }

func New(q resource.Queries) *Resources { return &Resources{queries: q} }

var _ release.Resources = (*Resources)(nil)

func (r *Resources) Find(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) (release.ResourceInfo, error) {
	x, err := r.queries.Find(ctx, org, env, name)
	if err != nil {
		return release.ResourceInfo{}, err
	}
	return release.ResourceInfo{Name: x.Name, Kind: x.Kind, Deleting: x.Status == resource.StatusDeleting}, nil
}
