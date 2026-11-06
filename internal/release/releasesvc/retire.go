package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// RetireArchived removes an archived project's apps from their clusters: the
// handler of project.archived (the release module subscribes it). For every
// environment that ever deployed it stops the deployment in progress
// (cancelled, reason archived, with its build and its pending approval) and
// hands the cluster a retirement above every deployment. Its databases,
// caches and files stay (they are not the app's), and so does its history.
// A redelivered event changes nothing; a project that is not archived (or no
// longer exists) is left alone.
func (s *Service) RetireArchived(ctx context.Context, organization identity.OrganizationID, id project.ID, by identity.UserID) error {
	p, err := s.projects.Find(ctx, organization, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil || !p.Archived() {
		return err
	}
	envs, err := s.projects.ListEnvironments(ctx, organization, id, project.EnvironmentFilter{})
	if err != nil {
		return err
	}
	for _, e := range envs {
		if err := s.retire(ctx, p, e, by); err != nil {
			return err
		}
	}
	return nil
}

// retire retires one environment's app, holding the project and then the
// environment (the apply step's order: a deployment either reached the
// cluster before, and this revision is above it, or it sees the archive).
func (s *Service) retire(ctx context.Context, p project.Project, e project.Environment, by identity.UserID) error {
	org := p.Organization
	var stopped *release.Deployment
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		held, err := s.projects.Hold(ctx, org, p.ID)
		if err != nil || !held.Archived() {
			return err
		}
		if seen, err := s.repo.FindState(ctx, org, e.ID); err != nil || seen.App == "" {
			return err // never deployed: the cluster has nothing of it
		}
		state, err := s.lockState(ctx, held, e)
		if err != nil {
			return err
		}
		if state.RetiredAt != nil && state.Active == nil {
			return nil // retired already (a redelivery)
		}
		if state.Active != nil {
			if stopped, err = s.cancelArchived(ctx, held, e, *state.Active, by); err != nil {
				return err
			}
			if err := s.repo.Settle(ctx, org, e.ID, *state.Active); err != nil {
				return err
			}
		}
		revision := state.Revision + 1
		if err := s.repo.Retire(ctx, org, e.ID, revision); err != nil {
			return err
		}
		return s.cluster.Retire(ctx, release.Target{Organization: org, Project: p.ID, Environment: e.ID}, state.App, revision)
	})
	if err != nil {
		return err
	}
	if stopped != nil {
		_ = s.engine.Cancel(ctx, org, stopped.Run, "its project was archived")
	}
	return nil
}

// cancelArchived ends the environment's deployment in progress (nil when it
// already finished): cancelled with reason archived, its build stopped and
// the approval it waits for withdrawn.
func (s *Service) cancelArchived(ctx context.Context, p project.Project, e project.Environment, id release.DeploymentID,
	by identity.UserID) (*release.Deployment, error) {
	org := p.Organization
	d, err := s.repo.FindDeploymentForUpdate(ctx, org, id)
	if err != nil || d.Status.Finished() {
		return nil, err
	}
	if err := s.repo.Finish(ctx, org, d.ID, release.StatusCancelled, nil); err != nil {
		return nil, err
	}
	if d.Build != nil && d.Release == nil {
		if err := s.cancelBuilds(ctx, d, by); err != nil {
			return nil, err
		}
	}
	if d.Status == release.StatusWaitingApproval && s.approvals != nil {
		for _, step := range []string{release.StepAuthorize, release.StepAuthorizeScan} {
			if err := s.approvals.CancelRun(ctx, org, d.Run.String(), step); err != nil {
				return nil, err
			}
		}
	}
	if err := s.events.Publish(ctx, release.NewDeploymentArchived(org, by, d, names(p, e))); err != nil {
		return nil, err
	}
	return &d, nil
}
