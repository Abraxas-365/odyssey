package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// checkScope is the check of a setting's services being written (MS-4):
// each one a service the app has now, and a cluster that can honor a scope.
// The whole app needs neither. It returns the app's services (IT's rules
// judge the ones reached).
func (s *Service) checkScope(ctx context.Context, p project.Project, e project.Environment, scope release.Scope) ([]project.Service, error) {
	topology, err := s.projects.Services(ctx, p.Organization, p.ID)
	if err != nil {
		return nil, err
	}
	if scope.Whole() {
		return topology, nil
	}
	if err := scope.Check("services", topology); err != nil {
		return nil, err
	}
	return topology, s.canScope(ctx, p, e)
}

// canScope refuses (409 SERVICE_SETTINGS_UNAVAILABLE) a scoped setting the
// environment's cluster cannot honor (also none, or a revoked one): the
// deployment that applies it would fail.
func (s *Service) canScope(ctx context.Context, p project.Project, e project.Environment) error {
	c, err := s.cluster.Secrets(ctx, target(p, e))
	if err != nil {
		return err
	}
	if !c.ScopedSettings {
		return release.ErrServiceSettingsUnavailable()
	}
	return nil
}
