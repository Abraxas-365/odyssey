package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// AI (2d-ai): the ai domain makes the key and asks release, through the
// release.AI port, to hand it to the cluster and record the app's use of it
// as a settings revision. Both happen in one transaction under the settings
// lock, so a deployment never waits for a key version that was not delivered.

var _ release.AI = (*Service)(nil)

func (s *Service) AITarget(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.AITarget, error) {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.AITarget{}, err
	}
	c, err := s.clusterSecrets(ctx, p, e)
	if err != nil {
		return release.AITarget{}, err
	}
	settings, err := s.repo.FindSettings(ctx, organization, env)
	if err != nil {
		return release.AITarget{}, err
	}
	versions, err := s.repo.ListSecretVersions(ctx, organization, env)
	if err != nil {
		return release.AITarget{}, err
	}
	out := release.AITarget{Capable: c.AI, Key: c.Key, Revoked: c.Revoked,
		NextVersion: release.AINextVersion(settings, versions), Revision: settings.Revision, Current: settings.AI}
	if !settings.AI.Enabled {
		// the variables the AI would set, against what the app already defines
		open, err := s.repo.ListSecretRequests(ctx, organization, env)
		if err != nil {
			return release.AITarget{}, err
		}
		_, _, out.Conflict = settings.WithAI(release.AISettings{Enabled: true, KeyVersion: out.NextVersion}, identity.UserID{}, open)
	}
	return out, nil
}

func (s *Service) EnableAI(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.EnableAI) (release.SettingsChange, error) {
	if err := input.Validate(); err != nil {
		return release.SettingsChange{}, err
	}
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return release.SettingsChange{}, err
	}
	var out release.SettingsChange
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		want := input.Settings()
		if prev.AI == want {
			// Already in force (a retry after the commit): the key was
			// delivered with it. Asking again still retries a config
			// deployment that was refused at apply.
			out, _, err = s.changeSettings(ctx, state, p, e, prev, nil, input.RequestedBy)
			return err
		}
		versions, err := s.repo.ListSecretVersions(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		if next := release.AINextVersion(prev, versions); input.Version != next {
			return errx.Conflict("La llave de IA cambió mientras se preparaba: vuelve a intentarlo.").
				WithCode(release.CodeSecretVersionConflict).WithDetail("version", next-1)
		}
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		// A name the app defines is refused before anything is delivered.
		n, _, err := prev.WithAI(want, input.RequestedBy, open)
		if err != nil {
			return err
		}
		if err := s.cluster.DeliverSecret(ctx, target(p, e), release.SecretDelivery{App: state.App, Name: release.AIKeySecret,
			Version: input.Version, KeyID: input.KeyID, Sealed: input.Sealed, By: input.RequestedBy}); err != nil {
			return err
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, &n, input.RequestedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewAIChanged(organization, input.RequestedBy, id.String(), out.Settings, started, names(p, e)))
	})
	return out, err
}

func (s *Service) DisableAI(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.DisableAI) (release.SettingsChange, error) {
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return release.SettingsChange{}, err
	}
	var out release.SettingsChange
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		n, changed := prev.WithoutAI(input.RequestedBy)
		var next *release.Settings
		if changed {
			next = &n
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil || next == nil {
			return err
		}
		return s.events.Publish(ctx, release.NewAIChanged(organization, input.RequestedBy, id.String(), out.Settings, started, names(p, e)))
	})
	return out, err
}

func (s *Service) AISettled(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, revision int64) (bool, error) {
	if _, _, err := s.place(ctx, organization, id, env); err != nil {
		return false, err
	}
	state, err := s.repo.FindState(ctx, organization, env)
	if err != nil {
		return false, err
	}
	switch {
	case state.Active != nil:
		return false, nil // a deployment is rolling out (or about to start the config one)
	case state.Current == nil:
		return true, nil // nothing online: nothing runs with any key
	}
	return state.AppliedRevision() >= revision, nil
}
