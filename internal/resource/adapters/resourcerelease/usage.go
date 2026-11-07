// Package resourcerelease is the resource.Usage adapter on the release
// domain: connections live in the settings release deploys, and are changed
// through release's Commands, Queries and Usage ports only (never
// releasesvc). It is the only way resource reaches release.
package resourcerelease

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/resource"
)

type Usage struct {
	commands release.Commands
	queries  release.Queries
	usage    release.Usage
}

func New(commands release.Commands, queries release.Queries, usage release.Usage) *Usage {
	return &Usage{commands: commands, queries: queries, usage: usage}
}

var _ resource.Usage = (*Usage)(nil)

func connections(uses []release.ResourceUse) []resource.Connection {
	out := make([]resource.Connection, 0, len(uses))
	for _, u := range uses {
		out = append(out, resource.Connection{Name: u.Name, Prefix: u.Prefix, Services: append([]string{}, u.Services.Normalize()...)})
	}
	return out
}

// scope is release's form of the services resource carries (nil: keep).
func scope(services *[]string) *release.Scope {
	if services == nil {
		return nil
	}
	s := release.Scope(append([]string{}, *services...))
	return &s
}

func applied(c release.SettingsChange) resource.Applied {
	out := resource.Applied{Revision: c.Settings.Revision}
	if c.Deployment != nil {
		id := c.Deployment.ID.String()
		out.Deployment = &id
	}
	return out
}

func (u *Usage) Connections(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) ([]resource.Connection, error) {
	s, err := u.queries.Settings(ctx, org, id, env)
	if err != nil {
		return nil, err
	}
	return connections(s.Resources), nil
}

func (u *Usage) Connect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, l resource.Link, by identity.UserID) (resource.Applied, error) {
	out, err := u.commands.ConnectResource(ctx, org, id, env, release.ConnectResource{Name: l.Name, Prefix: l.Prefix, Services: scope(l.Services),
		RequestedBy: by, Judged: l.Judged})
	if err != nil {
		return resource.Applied{}, err
	}
	return applied(out), nil
}

func (u *Usage) AuthorizeConnect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, ask resource.Ask) (*gate.Pending, error) {
	return u.usage.AuthorizeConnect(ctx, org, id, env, release.AuthorizeConnect{Name: ask.Name, Kind: ask.Kind, Services: scope(ask.Services), Detail: ask.Detail})
}

func (u *Usage) Disconnect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, by identity.UserID) (resource.Applied, error) {
	out, err := u.commands.DisconnectResource(ctx, org, id, env, release.DisconnectResource{Name: name, RequestedBy: by})
	if err != nil {
		return resource.Applied{}, err
	}
	return applied(out), nil
}

func (u *Usage) WithConnectionsLocked(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID,
	fn func(ctx context.Context, connected []resource.Connection) error) error {
	return u.usage.WithResourceUseLocked(ctx, org, id, env, func(ctx context.Context, uses []release.ResourceUse) error {
		return fn(ctx, connections(uses))
	})
}
