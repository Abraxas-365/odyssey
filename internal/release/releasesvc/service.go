// Package releasesvc implements release.Commands and release.Queries and the
// deploy workflow that moves a deployment through the cluster.
//
// Locks are always taken in the same order — workflow run, environment state,
// deployment — so Deploy, Rollback, the promote step and OnFailure (which
// runs holding its run) never deadlock each other.
package releasesvc

import (
	"context"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Options tune the deploy workflow; zero values take the defaults.
type Options struct {
	// ReadyTimeout is how long an app may take to answer its health check
	// (default 5 min).
	ReadyTimeout time.Duration
	// PollEvery is how often wait_ready asks the cluster (default 2 s).
	PollEvery time.Duration
}

type Service struct {
	repo     release.Repository
	projects project.Queries
	cluster  release.Cluster
	engine   workflow.Engine
	tx       txn.Runner
	events   event.Publisher
	opts     Options
}

func New(repo release.Repository, projects project.Queries, cluster release.Cluster, engine workflow.Engine,
	tx txn.Runner, events event.Publisher, opts Options) *Service {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 5 * time.Minute
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 2 * time.Second
	}
	return &Service{repo: repo, projects: projects, cluster: cluster, engine: engine, tx: tx, events: events, opts: opts}
}

var (
	_ release.Commands  = (*Service)(nil)
	_ release.Queries   = (*Service)(nil)
	_ release.Workflows = (*Service)(nil)
)

// errSuperseded ends the workflow of a deployment that no longer matters (a
// newer one cancelled it); OnFailure finds it finished and does nothing.
var errSuperseded = errx.Conflict("deployment superseded")

// place resolves and checks the project and environment of a request.
func (s *Service) place(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Project, project.Environment, error) {
	p, err := s.projects.Find(ctx, organization, id)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	e, err := s.projects.FindEnvironment(ctx, organization, id, env)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, e, nil
}

func names(p project.Project, e project.Environment) release.Names {
	return release.Names{Project: p.Name, Environment: e.Slug}
}

func archived(p project.Project) error {
	if p.Archived() {
		return errx.Business("the app is archived: unarchive it to publish").WithCode(release.CodeArchived)
	}
	return nil
}

func (s *Service) Deploy(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.Deploy) (release.Deployment, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return release.Deployment{}, err
	}
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.Deployment{}, err
	}
	if err := archived(p); err != nil {
		return release.Deployment{}, err
	}
	r := release.Release{ID: release.NewID(), Organization: organization, Project: id, Image: input.Image,
		Config: input.Config, CreatedBy: input.RequestedBy}
	var d release.Deployment
	var superseded *release.Deployment
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateRelease(ctx, r); err != nil {
			return err
		}
		if err := s.events.Publish(ctx, release.NewCreated(organization, input.RequestedBy, r, names(p, e))); err != nil {
			return err
		}
		state, err := s.lockState(ctx, p, e)
		if err != nil {
			return err
		}
		d, superseded, err = s.begin(ctx, state, p, e, r, release.NewDeploymentID(), release.ReasonDeploy, nil, input.RequestedBy)
		return err
	})
	if err != nil {
		return release.Deployment{}, err
	}
	s.stop(ctx, superseded)
	return d.WithProgress(), nil
}

func (s *Service) Rollback(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, target release.DeploymentID, input release.Rollback) (release.Deployment, error) {
	if err := input.Validate(); err != nil {
		return release.Deployment{}, err
	}
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.Deployment{}, err
	}
	if err := archived(p); err != nil {
		return release.Deployment{}, err
	}
	old, err := s.deployment(ctx, organization, id, env, target)
	if err != nil {
		return release.Deployment{}, err
	}
	if old.Status != release.StatusReady {
		return release.Deployment{}, errx.Business("only a version that went live can be restored").WithCode(release.CodeNotRollbackable)
	}
	r, err := s.repo.FindRelease(ctx, organization, old.Release)
	if err != nil {
		return release.Deployment{}, err
	}
	var d release.Deployment
	var superseded *release.Deployment
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, err := s.lockState(ctx, p, e)
		if err != nil {
			return err
		}
		if state.Current != nil && *state.Current == r.ID && state.Active == nil {
			return errx.Business("this version is already the one online").WithCode(release.CodeAlreadyCurrent)
		}
		d, superseded, err = s.begin(ctx, state, p, e, r, release.NewDeploymentID(), release.ReasonRollback, state.CurrentDeployment, input.RequestedBy)
		return err
	})
	if err != nil {
		return release.Deployment{}, err
	}
	s.stop(ctx, superseded)
	return d.WithProgress(), nil
}

func (s *Service) lockState(ctx context.Context, p project.Project, e project.Environment) (release.EnvironmentState, error) {
	return s.repo.LockState(ctx, release.EnvironmentState{Environment: e.ID, Organization: p.Organization,
		Project: p.ID, App: release.AppName(p.Slug, e)})
}

// begin starts a deployment of r in the locked environment, cancelling the
// one in progress (returned, so its run can be stopped after the commit).
func (s *Service) begin(ctx context.Context, state release.EnvironmentState, p project.Project, e project.Environment,
	r release.Release, id release.DeploymentID, reason release.Reason, rollbackOf *release.DeploymentID, actor identity.UserID) (release.Deployment, *release.Deployment, error) {
	d := release.Deployment{
		ID: id, Organization: p.Organization, Project: p.ID, Environment: e.ID,
		Release: r.ID, Image: r.Image, Status: release.StatusPending, Reason: reason, Step: release.StepApply,
		Revision: state.Revision + 1, Previous: state.Current, RollbackOf: rollbackOf, RequestedBy: actor,
	}
	var superseded *release.Deployment
	if state.Active != nil {
		old, err := s.repo.FindDeploymentForUpdate(ctx, p.Organization, *state.Active)
		if err != nil {
			return release.Deployment{}, nil, err
		}
		if !old.Status.Finished() {
			if err := s.repo.Finish(ctx, p.Organization, old.ID, release.StatusCancelled, nil); err != nil {
				return release.Deployment{}, nil, err
			}
			if err := s.events.Publish(ctx, release.NewDeploymentCancelled(p.Organization, actor, old, d.ID, names(p, e))); err != nil {
				return release.Deployment{}, nil, err
			}
			superseded = &old
		}
	}
	run, err := s.engine.Start(ctx, workflow.Start{Workflow: release.WorkflowDeploy, Organization: p.Organization,
		Actor: actor, Subject: event.Subject(release.DeploymentSubjectKind, d.ID),
		Input: release.DeployInput{Deployment: d.ID}, IdempotencyKey: d.ID.String()})
	if err != nil {
		return release.Deployment{}, nil, err
	}
	d.Run = run
	if err := s.repo.CreateDeployment(ctx, d); err != nil {
		return release.Deployment{}, nil, err
	}
	if err := s.repo.Begin(ctx, p.Organization, e.ID, d.ID, d.Revision); err != nil {
		return release.Deployment{}, nil, err
	}
	if err := s.events.Publish(ctx, release.NewDeploymentStarted(p.Organization, actor, d, names(p, e))); err != nil {
		return release.Deployment{}, nil, err
	}
	created, err := s.repo.FindDeployment(ctx, p.Organization, d.ID)
	return created, superseded, err
}

// stop cancels a superseded deployment's run. It is an optimisation: the
// deployment is already cancelled and its steps end themselves on seeing it;
// the cluster ignores its older revision.
func (s *Service) stop(ctx context.Context, d *release.Deployment) {
	if d != nil {
		_ = s.engine.Cancel(ctx, d.Organization, d.Run, "superseded by a newer deployment")
	}
}

// ---- queries ----

func (s *Service) deployment(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, deployment release.DeploymentID) (release.Deployment, error) {
	d, err := s.repo.FindDeployment(ctx, organization, deployment)
	if err != nil {
		return release.Deployment{}, err
	}
	if d.Project != id || d.Environment != env {
		return release.Deployment{}, errx.NotFound("deployment not found")
	}
	return d, nil
}

func (s *Service) FindDeployment(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, deployment release.DeploymentID) (release.Deployment, error) {
	d, err := s.deployment(ctx, organization, id, env, deployment)
	return d.WithProgress(), err
}

func (s *Service) ListDeployments(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, filter release.DeploymentFilter, page query.Pagination) (query.Paginated[release.Deployment], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[release.Deployment]{}, err
	}
	if _, _, err := s.place(ctx, organization, id, env); err != nil {
		return query.Paginated[release.Deployment]{}, err
	}
	out, err := s.repo.ListDeployments(ctx, organization, env, filter, page.Normalize())
	for i := range out.Items {
		out.Items[i] = out.Items[i].WithProgress()
	}
	return out, err
}

func (s *Service) FindRelease(ctx context.Context, organization identity.OrganizationID, id project.ID, rel release.ID) (release.Release, error) {
	r, err := s.repo.FindRelease(ctx, organization, rel)
	if err != nil {
		return release.Release{}, err
	}
	if r.Project != id {
		return release.Release{}, errx.NotFound("release not found")
	}
	return r, nil
}

func (s *Service) Current(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.Current, error) {
	if _, _, err := s.place(ctx, organization, id, env); err != nil {
		return release.Current{}, err
	}
	state, err := s.repo.FindState(ctx, organization, env)
	if err != nil {
		return release.Current{}, err
	}
	out := release.Current{Environment: env, URL: state.URL}
	if state.Current != nil {
		r, err := s.repo.FindRelease(ctx, organization, *state.Current)
		if err != nil {
			return release.Current{}, err
		}
		out.Release = &r
	}
	for _, x := range []struct {
		id   *release.DeploymentID
		into **release.Deployment
	}{{state.CurrentDeployment, &out.Deployment}, {state.Active, &out.Active}} {
		if x.id == nil {
			continue
		}
		d, err := s.repo.FindDeployment(ctx, organization, *x.id)
		if err != nil {
			return release.Current{}, err
		}
		d = d.WithProgress()
		*x.into = &d
	}
	return out, nil
}
