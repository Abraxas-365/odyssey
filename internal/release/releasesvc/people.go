package releasesvc

import (
	"context"
	"log/slog"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// UsePeople wires the organization's directory (the releaseiam adapter).
// Without it deployments keep no name and the console says "tu equipo".
func (s *Service) UsePeople(p release.People) { s.people = p }

// NameRequester records, once, the directory's name of whoever asked for the
// deployment: the handler of deployment.started (the release module
// subscribes it), so the request never waits on the directory and a
// directory that is down never fails a deployment. A deployment the platform
// started, one already named, or a person the directory does not know is
// left as it is. A directory error is returned, so the delivery is retried
// (a later success still fills the name); a deployment that no longer exists
// is ignored.
func (s *Service) NameRequester(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID) error {
	if s.people == nil {
		return nil
	}
	d, err := s.repo.FindDeployment(ctx, organization, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil || d.RequestedBy.IsZero() || d.RequestedByName != "" {
		return err
	}
	name, err := s.people.Name(ctx, organization, d.RequestedBy)
	if err != nil {
		slog.Warn("release: the directory could not name who asked for a deployment", "deployment", id, "err", err)
		return err
	}
	if name == "" {
		return nil
	}
	return s.repo.ObserveRequesterName(ctx, organization, id, name)
}
