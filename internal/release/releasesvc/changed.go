package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// fillChanged sets Changed on every deployment of ds with one read of the
// services of all the releases involved (never one read per deployment).
// Changed is computed here, from the stored releases, so deployments made
// before it existed have it too: the services the deployment's release
// changes against the release serving when it was asked for (Previous; none:
// every service is new). It stays nil for a deployment without a release
// (still building, or its build failed) and for one with neither a Previous
// nor services in its release.
func (s *Service) fillChanged(ctx context.Context, organization identity.OrganizationID, ds ...*release.Deployment) error {
	seen := map[release.ID]bool{}
	var ids []release.ID
	for _, d := range ds {
		for _, id := range []*release.ID{d.Release, d.Previous} {
			if id != nil && !seen[*id] {
				seen[*id] = true
				ids = append(ids, *id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	services, err := s.repo.ReleaseServices(ctx, organization, ids)
	if err != nil {
		return err
	}
	for _, d := range ds {
		d.Changed = changedOf(d, services)
	}
	return nil
}

func changedOf(d *release.Deployment, services map[release.ID][]release.Service) []string {
	if d.Release == nil {
		return nil
	}
	next := services[*d.Release]
	var prev *release.Release
	if d.Previous != nil {
		prev = &release.Release{ID: *d.Previous, Services: services[*d.Previous]}
	} else if len(next) == 0 {
		return nil
	}
	return release.Compare(next, prev).Changed
}

// ServiceStates joins the services of the release the environment serves
// with what its cluster reports of the app (release.ServiceStates). Nothing
// serving is an empty list. It reads the cluster's status, which changes
// nothing.
func (s *Service) ServiceStates(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) ([]release.ServiceState, error) {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return nil, err
	}
	state, err := s.repo.FindState(ctx, organization, env)
	if err != nil {
		return nil, err
	}
	if state.Current == nil {
		return []release.ServiceState{}, nil
	}
	services, err := s.repo.ReleaseServices(ctx, organization, []release.ID{*state.Current})
	if err != nil {
		return nil, err
	}
	status, err := s.cluster.Status(ctx, target(p, e), state.App)
	if err != nil {
		return nil, err
	}
	return release.ServiceStates(services[*state.Current], status), nil
}
