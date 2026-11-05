package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// ConnectResource connects a resource to the environment's app: a new
// settings revision and, when something is online, the config deployment that
// wires the variables. The lock order (state, then settings) is the one
// SetEnv uses; the resource domain's Delete takes it too (WithResourceUseLocked).
func (s *Service) ConnectResource(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.ConnectResource) (release.SettingsChange, error) {
	if err := input.Validate(); err != nil {
		return release.SettingsChange{}, err
	}
	if s.resources == nil {
		return release.SettingsChange{}, errx.Internal("resources are not wired")
	}
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return release.SettingsChange{}, err
	}
	// IT's rules judge BEFORE the transaction: a refusal (or, in 3b, the
	// approval request it stores) must never live in a transaction that rolls
	// back on that same refusal. The resource's kind is read here; the
	// transaction reads the resource again under the lock. A caller that
	// already judged (Judged: resourcesvc.Create asks AuthorizeConnect before
	// it provisions anything) is not judged twice; a tier change in the
	// milliseconds between its check and this connection is ignored on purpose.
	if !input.Judged {
		info, err := s.resources.Find(ctx, organization, e.ID, input.Name)
		if err != nil {
			return release.SettingsChange{}, err
		}
		if err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionConnectResource, ResourceKind: string(info.Kind)}); err != nil {
			return release.SettingsChange{}, err
		}
	}
	var out release.SettingsChange
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		info, err := s.resources.Find(ctx, organization, e.ID, input.Name)
		if err != nil {
			return err
		}
		if info.Deleting {
			return errx.Conflict("El recurso " + input.Name + " se está eliminando: no se puede conectar.").WithCode(release.CodeResourceDeleting)
		}
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		use := release.ResourceUse{Name: info.Name, Kind: info.Kind, Prefix: input.Prefix}
		n, changed, err := prev.WithResource(use, input.RequestedBy, open)
		if err != nil {
			return err
		}
		var next *release.Settings
		if changed {
			next = &n
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil || next == nil {
			return err
		}
		return s.events.Publish(ctx, release.NewResourcesChanged(organization, input.RequestedBy, id.String(), out.Settings,
			release.ResourceConnected, use, started, names(p, e)))
	})
	return out, err
}

// AuthorizeConnect is IT's rules' answer to connecting a resource of the kind
// (nil: allowed). It writes nothing; see release.Usage.
func (s *Service) AuthorizeConnect(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, kind string) error {
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return err
	}
	return s.authorize(ctx, p, e, gate.Request{Action: gate.ActionConnectResource, ResourceKind: kind})
}

// DisconnectResource removes the connection; the resource keeps existing.
func (s *Service) DisconnectResource(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.DisconnectResource) (release.SettingsChange, error) {
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
		use, ok := prev.Resource(input.Name)
		if !ok {
			return errx.NotFound("the environment's app is not connected to " + input.Name)
		}
		n, _ := prev.WithoutResource(input.Name, input.RequestedBy)
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, &n, input.RequestedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewResourcesChanged(organization, input.RequestedBy, id.String(), out.Settings,
			release.ResourceDisconnected, use, started, names(p, e)))
	})
	return out, err
}

// WithResourceUseLocked runs fn holding the environment's settings lock (the
// one ConnectResource takes), with the resources connected at that moment.
// It does not require the project to be writable: removing a resource of an
// archived project is allowed.
func (s *Service) WithResourceUseLocked(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID,
	fn func(ctx context.Context, connected []release.ResourceUse) error) error {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		_, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		return fn(ctx, prev.Normalize().Resources)
	})
}
