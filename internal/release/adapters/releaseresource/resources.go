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

// Statuses lists the environment's resources with how the cluster sees each.
func (r *Resources) Statuses(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, names []string) ([]release.ResourceState, error) {
	l, err := r.queries.Named(ctx, org, env, names)
	if err != nil {
		return nil, err
	}
	out := make([]release.ResourceState, 0, len(l))
	for _, v := range l {
		out = append(out, release.ResourceState{Name: v.Name, Kind: v.Kind, Status: statusOf(v), Message: v.Message})
	}
	return out, nil
}

func statusOf(v resource.View) release.ResourceStatus {
	switch {
	case v.Status == resource.StatusDeleting || v.Phase == resource.PhaseDeleted:
		return release.ResourceDeleting
	case v.Phase == resource.PhaseReady:
		return release.ResourceReady
	case v.Phase == resource.PhaseFailed:
		return release.ResourceFailed
	}
	return release.ResourcePreparing
}
