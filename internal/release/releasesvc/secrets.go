package releasesvc

import (
	"context"
	"maps"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Variables and secrets (2a). Every change is a settings revision applied
// like an access change (changeSettings); a secret's value never passes
// through here in the clear: a managed one arrives sealed to the cluster's
// key and goes on to the fleet in the same transaction as its reference.

// SetEnv changes plain variables.
func (s *Service) SetEnv(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.SetEnv) (release.SettingsChange, error) {
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
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		n, set, unset, err := prev.WithEnv(input, open)
		if err != nil {
			return err
		}
		var next *release.Settings
		if len(set)+len(unset) > 0 {
			next = &n
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil || next == nil {
			return err
		}
		return s.events.Publish(ctx, release.NewEnvChanged(organization, input.RequestedBy, id.String(), out.Settings, set, unset, started, names(p, e)))
	})
	return out, err
}

// RequestSecret records that a value is needed. Nothing reaches the cluster:
// the app keeps running as it is until a person loads the value.
func (s *Service) RequestSecret(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.RequestSecret) (release.SecretView, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return release.SecretView{}, err
	}
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return release.SecretView{}, err
	}
	var out release.SecretView
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		_, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		if _, plain := prev.Env[input.Name]; plain {
			return errx.InvalidField("name", input.Name+" is a plain variable: load its value as a secret in the console to make it one")
		}
		if ref, ok := prev.Secret(input.Name); ok {
			c, err := s.clusterSecrets(ctx, p, e)
			if err != nil {
				return err
			}
			versions, err := s.repo.ListSecretVersions(ctx, organization, e.ID)
			if err != nil {
				return err
			}
			out = release.ViewSecrets(prev, nil, c, versions).Items[indexOf(prev, ref.Name)]
			return nil
		}
		versions, err := s.repo.ListSecretVersions(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		if err := prev.CheckRequest(open, input.Name); err != nil {
			return err
		}
		q := release.SecretRequest{Environment: e.ID, Organization: organization, Name: input.Name, Note: input.Note,
			RequestedBy: input.RequestedBy, RequestedVia: release.ViaFor(release.ReasonDeploy, event.MetaFrom(ctx).Client, s.opts.AntigravityClientID),
			CreatedAt: time.Now().UTC()}
		if err := s.repo.PutSecretRequest(ctx, q); err != nil {
			return err
		}
		out = release.SecretView{SecretRef: release.SecretRef{Name: q.Name, Source: release.SecretManaged}, State: release.SecretStateMissing,
			NextVersion: release.NextSecretVersion(prev, versions, q.Name), Request: &q}
		return s.events.Publish(ctx, release.NewSecretChanged(organization, input.RequestedBy, id.String(), prev, out.SecretRef,
			release.SecretChangeRequested, nil, names(p, e)))
	})
	return out, err
}

func indexOf(s release.Settings, name string) int {
	for i, r := range s.Normalize().Secrets {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// LoadSecret stores a managed value: the sealed bytes go to the fleet and the
// reference into a new revision in one transaction, so a deployment never
// waits for a version that was not sent. Version must be the next one: the
// value was sealed for it (pkg/secretbox binds the version), so a stale page
// cannot overwrite someone else's newer value.
func (s *Service) LoadSecret(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.LoadSecret) (release.SettingsChange, error) {
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
		// The next version is above anything this name ever had here (an
		// external secret, version 0, becomes managed; a removed one keeps
		// counting: the cluster may still hold its value and takes only newer
		// versions).
		versions, err := s.repo.ListSecretVersions(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		if old, ok := prev.Secret(input.Name); ok && old.Source == release.SecretExternal && !input.MayChangeExternal {
			return release.ErrExternalSecretForbidden(input.Name)
		}
		if want := release.NextSecretVersion(prev, versions, input.Name); input.Version != want {
			return errx.Conflict("someone loaded this secret meanwhile: reload the page and type the value again").
				WithCode(release.CodeSecretVersionConflict).WithDetail("version", want-1)
		}
		if err := s.cluster.DeliverSecret(ctx, target(p, e), release.SecretDelivery{App: state.App, Name: input.Name,
			Version: input.Version, KeyID: input.KeyID, Sealed: input.Sealed, By: input.RequestedBy}); err != nil {
			return err
		}
		now := time.Now().UTC()
		ref := release.SecretRef{Name: input.Name, Source: release.SecretManaged, Version: input.Version, KeyID: input.KeyID,
			LoadedBy: input.RequestedBy, LoadedAt: &now}
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		next, err := prev.WithSecret(ref, input.RequestedBy, open)
		if err != nil {
			return err
		}
		if _, err := s.repo.DeleteSecretRequest(ctx, organization, e.ID, input.Name); err != nil {
			return err
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, &next, input.RequestedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewSecretChanged(organization, input.RequestedBy, id.String(), out.Settings, ref,
			release.SecretChangeLoaded, started, names(p, e)))
	})
	return out, err
}

// SetExternalSecret points a secret at a ClusterSecretStore the cluster has
// (refused at once when it has no operator or no such store).
func (s *Service) SetExternalSecret(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.SetExternalSecret) (release.SettingsChange, error) {
	input = input.Normalize()
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
		c, err := s.cluster.Secrets(ctx, target(p, e))
		if err != nil {
			return err
		}
		if !c.External {
			return errx.Business("Este clúster no tiene un gestor de secretos externos (External Secrets Operator). Pide a TI que lo instale.").
				WithCode(release.CodeExternalSecretsUnavailable)
		}
		if !contains(c.Stores, input.Store) {
			return errx.InvalidField("store", "the cluster has no ClusterSecretStore named "+input.Store)
		}
		ref := release.SecretRef{Name: input.Name, Source: release.SecretExternal, Store: input.Store, Key: input.Key, Property: input.Property}
		if old, ok := prev.Secret(input.Name); ok && old.Source == release.SecretExternal &&
			old.Store == ref.Store && old.Key == ref.Key && old.Property == ref.Property {
			out, _, err = s.changeSettings(ctx, state, p, e, prev, nil, input.RequestedBy)
			return err
		}
		open, err := s.repo.ListSecretRequests(ctx, organization, e.ID)
		if err != nil {
			return err
		}
		next, err := prev.WithSecret(ref, input.RequestedBy, open)
		if err != nil {
			return err
		}
		if _, err := s.repo.DeleteSecretRequest(ctx, organization, e.ID, input.Name); err != nil {
			return err
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, &next, input.RequestedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewSecretChanged(organization, input.RequestedBy, id.String(), out.Settings, ref,
			release.SecretChangeExternal, started, names(p, e)))
	})
	return out, err
}

// RemoveSecret drops the secret (a new revision without it; the cluster
// prunes its value) and any request of its name.
func (s *Service) RemoveSecret(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.RemoveSecret) (release.SettingsChange, error) {
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
		requested, err := s.repo.DeleteSecretRequest(ctx, organization, e.ID, input.Name)
		if err != nil {
			return err
		}
		ref, ok := prev.Secret(input.Name)
		if !ok && !requested {
			return errx.NotFound("the environment has no secret named " + input.Name)
		}
		if ok && ref.Source == release.SecretExternal && !input.MayChangeExternal {
			return release.ErrExternalSecretForbidden(input.Name)
		}
		var next *release.Settings
		if ok {
			n := prev.WithoutSecret(input.Name, input.RequestedBy)
			next = &n
		} else {
			ref = release.SecretRef{Name: input.Name, Source: release.SecretManaged}
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewSecretChanged(organization, input.RequestedBy, id.String(), out.Settings, ref,
			release.SecretChangeRemoved, started, names(p, e)))
	})
	return out, err
}

// Secrets lists the environment's secrets and requests, judged against its
// cluster.
func (s *Service) Secrets(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.Secrets, error) {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.Secrets{}, err
	}
	settings, err := s.repo.FindSettings(ctx, organization, env)
	if err != nil {
		return release.Secrets{}, err
	}
	return s.viewSecrets(ctx, p, e, settings)
}

// viewSecrets judges the secrets of settings (already read) against the
// environment's cluster.
func (s *Service) viewSecrets(ctx context.Context, p project.Project, e project.Environment, settings release.Settings) (release.Secrets, error) {
	requests, err := s.repo.ListSecretRequests(ctx, p.Organization, e.ID)
	if err != nil {
		return release.Secrets{}, err
	}
	versions, err := s.repo.ListSecretVersions(ctx, p.Organization, e.ID)
	if err != nil {
		return release.Secrets{}, err
	}
	c, err := s.clusterSecrets(ctx, p, e)
	if err != nil {
		return release.Secrets{}, err
	}
	return release.ViewSecrets(settings, requests, c, versions), nil
}

// clusterSecrets is what the environment's cluster offers, for this
// environment's app: the key carries the app a value is sealed for, and only
// that app's failures count.
func (s *Service) clusterSecrets(ctx context.Context, p project.Project, e project.Environment) (release.ClusterSecrets, error) {
	c, err := s.cluster.Secrets(ctx, target(p, e))
	if err != nil {
		return release.ClusterSecrets{}, err
	}
	app := release.AppName(p.Slug, e)
	if c.Key != nil {
		k := *c.Key
		k.App = app
		c.Key = &k
	}
	maps.DeleteFunc(c.Failures, func(_ string, f release.SecretFailure) bool { return f.App != app })
	return c, nil
}

func (s *Service) placeWritable(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Project, project.Environment, error) {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, e, archived(p)
}

func target(p project.Project, e project.Environment) release.Target {
	return release.Target{Organization: p.Organization, Project: p.ID, Environment: e.ID}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
