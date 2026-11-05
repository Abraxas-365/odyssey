// Package airelease is the ai.Release adapter on the release domain: the
// app's use of the AI lives in the settings release deploys, and is changed
// through release's AI port only (never releasesvc). It is the only way ai
// reaches release.
package airelease

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Release struct{ release release.AI }

func New(r release.AI) *Release { return &Release{release: r} }

var _ ai.Release = (*Release)(nil)

func (r *Release) Target(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (ai.Target, error) {
	t, err := r.release.AITarget(ctx, org, id, env)
	if err != nil {
		return ai.Target{}, err
	}
	out := ai.Target{Capable: t.Capable && !t.Revoked, NextVersion: t.NextVersion, Revision: t.Revision, Conflict: t.Conflict}
	if t.Key != nil && !t.Revoked {
		out.Key = &ai.ClusterKey{Cluster: t.Key.Cluster, KeyID: t.Key.KeyID, Public: t.Key.Public, App: t.Key.App}
	}
	return out, nil
}

func (r *Release) Enable(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, d ai.Delivery) (ai.Applied, error) {
	c, err := r.release.EnableAI(ctx, org, id, env, release.EnableAI{Version: d.Version, KeyID: d.KeyID, BaseURL: d.BaseURL, Sealed: d.Sealed, RequestedBy: d.By})
	if err != nil {
		return ai.Applied{}, err
	}
	return ai.Applied{Revision: c.Settings.Revision}, nil
}

func (r *Release) Disable(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, by identity.UserID) (ai.Applied, error) {
	c, err := r.release.DisableAI(ctx, org, id, env, release.DisableAI{RequestedBy: by})
	if err != nil {
		return ai.Applied{}, err
	}
	return ai.Applied{Revision: c.Settings.Revision}, nil
}

func (r *Release) Settled(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, revision int64) (bool, error) {
	return r.release.AISettled(ctx, org, id, env, revision)
}
