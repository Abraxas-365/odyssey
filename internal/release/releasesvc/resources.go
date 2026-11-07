package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
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
	// The services are checked (and IT's rules judge them) before the
	// transaction, which re-reads the connection under the lock.
	info, err := s.resources.Find(ctx, organization, e.ID, input.Name)
	if err != nil {
		return release.SettingsChange{}, err
	}
	_, reached, err := s.connectScope(ctx, p, e, info.Kind, input.Name, input.Services)
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
		pending, err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionConnectResource, ResourceKind: string(info.Kind), Services: reached}, gate.Hold{
			Summary: "Conectar " + string(info.Kind) + " «" + info.Name + "» a " + where(p, e) + scopeLabel(input.Services),
			Payload: release.ConnectResourceApproval{Op: release.OpConnectResource, Name: input.Name, Prefix: input.Prefix, Services: input.Services}})
		if err != nil {
			return release.SettingsChange{}, err
		}
		if pending != nil { // held for IT: nothing changed
			return release.SettingsChange{Outcome: gate.Holding(*pending)}, nil
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
		old, _ := prev.Resource(info.Name)
		use := release.ResourceUse{Name: info.Name, Kind: info.Kind, Prefix: input.Prefix, Services: release.Kept(input.Services, old.Services)}
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

// connectScope is the services a connection of the resource named name
// reaches (in: what the input asks; nil keeps the connection's, the whole
// app for a new one), checked: the app's services and a capable cluster
// when it names some, and a files resource reaching exactly one service of
// an app with several. reached is what IT's rules see.
func (s *Service) connectScope(ctx context.Context, p project.Project, e project.Environment, kind resourcekind.Kind, name string,
	in *release.Scope) (scope release.Scope, reached []gate.Service, err error) {
	settings, err := s.repo.FindSettings(ctx, p.Organization, e.ID)
	if err != nil {
		return nil, nil, err
	}
	old, connected := settings.Resource(name)
	scope = release.Kept(in, old.Services)
	var topology []project.Service
	if in == nil {
		topology, err = s.projects.Services(ctx, p.Organization, p.ID)
	} else {
		topology, err = s.checkScope(ctx, p, e, scope)
	}
	if err != nil {
		return nil, nil, err
	}
	// what is in force stays as it is (its service may be gone)
	if in != nil || !connected {
		if err := release.CheckResourceScope(kind, scope, topology); err != nil {
			return nil, nil, err
		}
	}
	return scope, scope.Gate(topology), nil
}

// scopeLabel words the services asked for in a summary.
func scopeLabel(in *release.Scope) string {
	if in == nil {
		return ""
	}
	return in.Normalize().Label()
}

// AuthorizeConnect is IT's rules' answer to connecting a resource (nil, nil:
// allowed; a Pending: held, the detail describes what will re-run). It
// checks the services the connection reaches and writes nothing but the
// approval request; see release.Usage.
func (s *Service) AuthorizeConnect(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, connect release.AuthorizeConnect) (*gate.Pending, error) {
	if connect.Services != nil {
		if err := connect.Services.CheckNames("services"); err != nil {
			return nil, err
		}
	}
	p, e, err := s.placeWritable(ctx, organization, id, env)
	if err != nil {
		return nil, err
	}
	_, reached, err := s.connectScope(ctx, p, e, connect.Kind, connect.Name, connect.Services)
	if err != nil {
		return nil, err
	}
	return s.authorize(ctx, p, e, gate.Request{Action: gate.ActionConnectResource, ResourceKind: string(connect.Kind), Services: reached}, gate.Hold{
		Summary: connect.Detail.Summary, Payload: connect.Detail.Payload})
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
