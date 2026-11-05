package releasesvc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// UseAIStatus wires the ai domain's port (the ai module is built after this
// one, since it needs release.AI).
func (s *Service) UseAIStatus(a release.AIStatus) { s.aiStatus = a }

var errNoResources = errors.New("the resource domain is not wired")

// Uses reads what the app depends on from the settings in force, the
// secrets judged against the cluster, the resource domain, the ai domain and
// the cluster's egress policy. The project and environment are resolved once
// and passed down; only the resources the app is connected to are asked for.
//
// Only the settings and the state (release's own data) can fail the call.
// Each of the other sources degrades on its own: its error is logged and the
// section comes back unavailable, so one cluster or gateway that is down does
// not take the whole card with it. A port nobody wired says nothing (no
// resources, AI off).
func (s *Service) Uses(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.Uses, error) {
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.Uses{}, err
	}
	// Settings before state: a settings change and its config deployment are
	// written together, so new settings with an old state cannot be read;
	// state before settings could (a transient, false config_failed).
	settings, err := s.repo.FindSettings(ctx, organization, env)
	if err != nil {
		return release.Uses{}, err
	}
	state, err := s.repo.FindState(ctx, organization, env)
	if err != nil {
		return release.Uses{}, err
	}
	in := release.UsesInput{Settings: settings, Serving: state.Current != nil, Active: state.Active != nil, Applied: state.AppliedSettings}
	down := func(section release.UsesSection, err error) {
		slog.Warn("uses: a source is unavailable", "section", section, "environment", env, "error", err)
		in.Unavailable = append(in.Unavailable, section)
	}
	if in.Secrets, err = s.viewSecrets(ctx, p, e, settings); err != nil {
		down(release.SectionSecrets, err)
	}
	if len(settings.Resources) > 0 {
		names := make([]string, len(settings.Resources))
		for i, r := range settings.Resources {
			names[i] = r.Name
		}
		if s.resources == nil {
			// connected resources and no domain to ask: unknown, not "gone"
			down(release.SectionResources, errNoResources)
		} else if in.Resources, err = s.resources.Statuses(ctx, organization, env, names); err != nil {
			down(release.SectionResources, err)
		}
	}
	if s.aiStatus != nil {
		if in.AI, err = s.aiStatus.AIState(ctx, organization, id, env); err != nil {
			down(release.SectionAI, err)
		}
	}
	if in.Egress, err = s.cluster.Egress(ctx, target(p, e)); err != nil {
		down(release.SectionEgress, err)
	}
	return release.NewUses(in), nil
}
