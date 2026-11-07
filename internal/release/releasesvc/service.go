// Package releasesvc implements release.Commands and release.Queries and the
// deploy workflow that moves a deployment through the cluster.
//
// Locks are always taken in the same order — workflow run, project (held
// for share by DeploySource), environment state, deployment — so Deploy,
// Rollback, the promote step and OnFailure (which runs holding its run)
// never deadlock each other.
package releasesvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
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
	// Assistants are the clients IAMKit registered for the template
	// assistants (IAMKIT_MCP_CLIENT_<KEY>_ID), to name them in a
	// deployment's requested_via.
	Assistants assistant.Registered
}

type Service struct {
	repo     release.Repository
	projects project.Queries
	cluster  release.Cluster
	builder  release.Builder
	// resources is the resource domain (nil until the module wires it: no
	// resource can then be connected).
	resources release.Resources
	// aiStatus is the ai domain (nil until wired: the AI reads as off).
	aiStatus release.AIStatus
	// policy is IT's rules (the policyrelease adapter); nil refuses every
	// governed action (see authorize).
	policy release.Policy
	// approvals holds what a rule wants approved (the approvalrelease
	// adapter); nil fails an approval verdict closed.
	approvals release.Approvals
	// people names who asked for a deployment (the releaseiam adapter); nil
	// keeps no name.
	people release.People
	engine workflow.Engine
	tx     txn.Runner
	events event.Publisher
	opts   Options
	now    func() time.Time // the clock (tests set it)
}

func New(repo release.Repository, projects project.Queries, cluster release.Cluster, builder release.Builder, engine workflow.Engine,
	tx txn.Runner, events event.Publisher, opts Options) *Service {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 5 * time.Minute
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 2 * time.Second
	}
	return &Service{repo: repo, projects: projects, cluster: cluster, builder: builder, engine: engine, tx: tx, events: events, opts: opts, now: time.Now}
}

var (
	_ release.Commands     = (*Service)(nil)
	_ release.Queries      = (*Service)(nil)
	_ release.AccessLookup = (*Service)(nil)
	_ release.Workflows    = (*Service)(nil)
	_ release.Usage        = (*Service)(nil)
)

// errSuperseded ends the workflow of a deployment that no longer matters (a
// newer one cancelled it); OnFailure finds it finished and does nothing.
var errSuperseded = errx.Conflict("deployment superseded")

// UseResources wires the resource domain's port (after construction: the
// resource domain depends on this service's Usage, so it is built later).
func (s *Service) UseResources(r release.Resources) { s.resources = r }

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
	topology, err := s.projects.Services(ctx, organization, id)
	if err != nil {
		return release.Deployment{}, err
	}
	asked, err := release.ImageServices(topology, input.Image, input.Config)
	if err != nil {
		return release.Deployment{}, err
	}
	if err := s.denyOnly(ctx, p, e, gate.ActionDeploy, brings{version: asked}); err != nil {
		return release.Deployment{}, err
	}
	r := release.Release{ID: release.NewID(), Organization: organization, Project: id, Image: input.Image,
		Config: input.Config, CreatedBy: input.RequestedBy}
	var d release.Deployment
	var superseded *release.Deployment
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if r.Services, err = s.imageServices(ctx, organization, id, r.Image, r.Config); err != nil {
			return err
		}
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
		d, superseded, err = s.begin(ctx, state, p, e, release.Deployment{ID: release.NewDeploymentID(), Release: &r.ID,
			Image: r.Image, Reason: release.ReasonDeploy, RequestedBy: input.RequestedBy, Message: input.Message})
		return err
	})
	if err != nil {
		return release.Deployment{}, err
	}
	s.stop(ctx, superseded)
	return d.WithProgress(), nil
}

// DeploySource stores the source first (a stream: it may take minutes, so
// outside any transaction); the deployment then builds it in its workflow.
// The project is held while the deployment is written, so it cannot have
// been archived during the upload; a source whose deployment could not
// start is discarded (or, failing that, swept as an orphan).
func (s *Service) DeploySource(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.DeploySource, archive io.Reader) (release.Deployment, error) {
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
	// Judged before the folder is stored: a refused deploy uploads nothing.
	if err := s.denyOnlySource(ctx, p, e); err != nil {
		return release.Deployment{}, err
	}
	src, err := s.builder.Upload(ctx, organization, id, build.Upload{Name: input.Name, UploadedBy: input.RequestedBy}, archive)
	if err != nil {
		return release.Deployment{}, err
	}
	d, err := s.startSource(ctx, organization, id, e, src, input.Config, asked{by: input.RequestedBy, message: input.Message, rebuild: input.Rebuild})
	if err != nil {
		if derr := s.builder.Discard(context.WithoutCancel(ctx), organization, src.ID); derr != nil {
			slog.Warn("could not discard the source of a deployment that did not start; the sweep will", "source", src.ID, "error", derr)
		}
		return release.Deployment{}, err
	}
	return d, nil
}

// DeployUploaded starts a deployment that builds a source already uploaded
// to the project (a commit imported from git). Unlike DeploySource it never
// discards the source when the deployment is refused: whoever uploaded it
// decides.
func (s *Service) DeployUploaded(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.DeployUploaded) (release.Deployment, error) {
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
	if err := s.denyOnlySource(ctx, p, e); err != nil {
		return release.Deployment{}, err
	}
	src, err := s.builder.Source(ctx, organization, input.Source)
	if err != nil {
		return release.Deployment{}, err
	}
	if src.Project != id {
		return release.Deployment{}, errx.NotFound("source not found")
	}
	if src.Expired() {
		return release.Deployment{}, errx.Business(build.Message(build.CodeSourceExpired)).WithCode(build.CodeSourceExpired)
	}
	return s.startSource(ctx, organization, id, e, src, input.Config, asked{by: input.RequestedBy, via: input.Via, rebuild: input.Rebuild})
}

// asked is who asked for a deployment from source, through what ("" derives
// it from the request), in what words and whether to build everything again.
type asked struct {
	by      identity.UserID
	via     string
	message string
	rebuild bool
}

// startSource writes the deployment that builds src with cfg, holding the
// project so it cannot have been archived meanwhile, and stops the one it
// superseded.
func (s *Service) startSource(ctx context.Context, organization identity.OrganizationID, id project.ID, e project.Environment, src build.Source, cfg release.Config, a asked) (release.Deployment, error) {
	var d release.Deployment
	var superseded *release.Deployment
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		held, err := s.projects.Hold(ctx, organization, id)
		if err != nil {
			return err
		}
		if err := archived(held); err != nil { // archived while the source was uploading
			return err
		}
		// The services as they are now: the version is made of these, and a
		// change from here on waits for the next deployment.
		topology, err := s.projects.Services(ctx, organization, id)
		if err != nil {
			return err
		}
		builds := release.BuildsFor(topology)
		state, err := s.lockState(ctx, held, e)
		if err != nil {
			return err
		}
		d, superseded, err = s.begin(ctx, state, held, e, release.Deployment{ID: release.NewDeploymentID(), Build: &builds[0].Build,
			Builds: builds, Source: &release.DeploymentSource{ID: src.ID, Name: src.Name, Size: src.Size, Git: src.Git}, Config: &cfg,
			Reason: release.ReasonDeploy, RequestedBy: a.by, RequestedVia: a.via, Message: a.message, Rebuild: a.rebuild})
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
	// A rollback puts an older version online: IT's rules judge it like a
	// deploy, with the services of that version.
	old, err := s.deployment(ctx, organization, id, env, target)
	if err != nil {
		return release.Deployment{}, err
	}
	if old.Status != release.StatusReady || old.Release == nil {
		return release.Deployment{}, errx.Business("only a version that went live can be restored").WithCode(release.CodeNotRollbackable)
	}
	r, err := s.repo.FindRelease(ctx, organization, *old.Release)
	if err != nil {
		return release.Deployment{}, err
	}
	if err := s.denyOnly(ctx, p, e, gate.ActionRollback, brings{version: r.Services}); err != nil {
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
		d, superseded, err = s.begin(ctx, state, p, e, release.Deployment{ID: release.NewDeploymentID(), Release: &r.ID, Image: r.Image,
			Reason: release.ReasonRollback, RollbackOf: state.CurrentDeployment, RequestedBy: input.RequestedBy})
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

// imageServices snapshots the app's services for a new version of one
// published image (an app of one service only). It holds the project (in
// the caller's transaction) first, so the services read are the ones
// committed: a concurrent change of the list (which locks the project)
// lands before or after this version, never half.
func (s *Service) imageServices(ctx context.Context, organization identity.OrganizationID, id project.ID, image string, c release.Config) ([]release.Service, error) {
	if _, err := s.projects.Hold(ctx, organization, id); err != nil {
		return nil, err
	}
	topology, err := s.projects.Services(ctx, organization, id)
	if err != nil {
		return nil, err
	}
	return release.ImageServices(topology, image, c)
}

// begin starts the deployment d (its ID, what it runs, why and for whom) in
// the locked environment, cancelling the one in progress, and its build
// (returned, so its run can be stopped after the commit). A deployment with
// a build starts building; one of a release, at apply.
func (s *Service) begin(ctx context.Context, state release.EnvironmentState, p project.Project, e project.Environment,
	d release.Deployment) (release.Deployment, *release.Deployment, error) {
	d.Organization, d.Project, d.Environment = p.Organization, p.ID, e.ID
	d.Status, d.Step = release.StatusPending, release.StepApply
	if d.Build != nil {
		d.Status, d.Step = release.StatusBuilding, release.StepBuild
	}
	d.Revision, d.Previous = state.Revision+1, state.Current
	if d.RequestedVia == "" {
		d.RequestedVia = release.ViaFor(d.Reason, event.MetaFrom(ctx).Client, s.opts.Assistants)
	}
	by := d.RequestedBy
	// who asked, as they were: the authorize step judges the deployment as
	// them (a push carries its system actor; the platform's own deployments,
	// auto_rollback and config, are not judged at all).
	input := actor.From(ctx)
	if _, governed := judged(d.Reason); governed && input.IsZero() {
		// The authorize step lets an actor-less run through (it can only be
		// one started before 3b): a new one must never be, or it would skip
		// IT's rules. Fail closed.
		return release.Deployment{}, nil, errx.Internal("a deployment that IT's rules judge needs who asked for it")
	}
	var superseded *release.Deployment
	var cancelRuns []string // requests of the waiting deployment this one replaces
	if state.Active != nil {
		old, err := s.repo.FindDeploymentForUpdate(ctx, p.Organization, *state.Active)
		if err != nil {
			return release.Deployment{}, nil, err
		}
		if !old.Status.Finished() {
			if err := s.repo.Finish(ctx, p.Organization, old.ID, release.StatusCancelled, nil); err != nil {
				return release.Deployment{}, nil, err
			}
			if old.Build != nil && old.Release == nil { // nobody needs its images now
				if err := s.cancelBuilds(ctx, old, by); err != nil {
					return release.Deployment{}, nil, err
				}
			}
			if old.Status == release.StatusWaitingApproval && s.approvals != nil {
				// A newer deployment replaces the one waiting for IT: its
				// request has no reason to wait any more.
				cancelRuns = append(cancelRuns, old.Run.String())
			}
			if err := s.events.Publish(ctx, release.NewDeploymentCancelled(p.Organization, by, old, d.ID, names(p, e))); err != nil {
				return release.Deployment{}, nil, err
			}
			superseded = &old
		}
	}
	for _, run := range cancelRuns {
		// the run holds at most one request at a time: whichever gate it waits at
		for _, step := range []string{release.StepAuthorize, release.StepAuthorizeScan} {
			if err := s.approvals.CancelRun(ctx, p.Organization, run, step); err != nil {
				return release.Deployment{}, nil, err
			}
		}
	}
	run, err := s.engine.Start(ctx, workflow.Start{Workflow: release.WorkflowDeploy, Organization: p.Organization,
		Actor: by, Subject: event.Subject(release.DeploymentSubjectKind, d.ID),
		Input: release.DeployInput{Deployment: d.ID, Actor: input}, IdempotencyKey: d.ID.String()})
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
	if err := s.events.Publish(ctx, release.NewDeploymentStarted(p.Organization, by, d, names(p, e))); err != nil {
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

// cancelBuilds stops every build of d (one per service) in the caller's
// transaction; one that never started has nothing to stop.
func (s *Service) cancelBuilds(ctx context.Context, d release.Deployment, by identity.UserID) error {
	for _, id := range d.BuildIDs() {
		if err := s.builder.Cancel(ctx, d.Organization, id, build.Cancel{CancelledBy: by}); err != nil {
			return err
		}
	}
	return nil
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
	out := release.Current{Environment: env, URL: state.URL, AppliedSettingsRevision: state.AppliedSettings}
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
	if out.Access, err = s.accessInForce(ctx, organization, env, state.AppliedSettings, out.Release != nil); err != nil {
		return release.Current{}, err
	}
	return out, nil
}

// accessInForce is Current's Access: the applied revision's mode against the
// latest stored one (what ListServing reads in one query for many
// environments). A revision that is gone tells nothing, as there.
func (s *Service) accessInForce(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, applied *int64, online bool) (*release.ServingAccess, error) {
	if !online || applied == nil {
		return nil, nil
	}
	var appliedMode *release.AccessMode
	if *applied > 0 {
		at, err := s.repo.FindSettingsRevision(ctx, organization, env, *applied)
		if errx.IsType(err, errx.TypeNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		appliedMode = &at.Access.Mode
	}
	latest, err := s.repo.FindSettings(ctx, organization, env)
	if err != nil {
		return nil, err
	}
	return release.AccessInForce(true, applied, appliedMode, &latest.Revision, &latest.Access.Mode), nil
}

// ---- settings ----

// FindAccessTarget is the one tenant-less lookup of an environment (the
// public access sign-in): its organization and the address the app was
// promoted at. NotFound until the first deployment is live.
func (s *Service) FindAccessTarget(ctx context.Context, env project.EnvironmentID) (release.AccessTarget, error) {
	st, err := s.repo.FindStateByEnvironment(ctx, env)
	if err != nil {
		return release.AccessTarget{}, err
	}
	if st.URL == "" {
		return release.AccessTarget{}, errx.NotFound("environment not found")
	}
	// The mode is what the cluster was last told, not what is stored: a
	// change waiting for its config deployment (or one that failed after
	// apply) is not what the app is protected with.
	var settings release.Settings
	if st.AppliedSettings != nil {
		settings, err = s.repo.FindSettingsRevision(ctx, st.Organization, st.Environment, *st.AppliedSettings)
	} else {
		settings, err = s.repo.FindSettings(ctx, st.Organization, st.Environment)
	}
	if err != nil {
		return release.AccessTarget{}, err
	}
	return release.AccessTarget{Environment: st.Environment, Organization: st.Organization, URL: st.URL, Mode: settings.Access.Mode}, nil
}

func (s *Service) Settings(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.Settings, error) {
	if _, _, err := s.place(ctx, organization, id, env); err != nil {
		return release.Settings{}, err
	}
	return s.repo.FindSettings(ctx, organization, env)
}

func (s *Service) AccessProtectionAvailable(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID) (bool, error) {
	if _, _, err := s.place(ctx, organization, id, env); err != nil {
		return false, err
	}
	return s.cluster.CanProtect(ctx, release.Target{Organization: organization, Project: id, Environment: env})
}

// SetAccess stores the mode as the environment's next settings revision and,
// when the release serving is not running with it and nothing is in
// progress, starts the config deployment that applies it. With a deployment
// in progress the revision only waits: that deployment applies the latest
// settings at its apply step, or, if it already applied, starts the config
// deployment when it ends (promote). The mode already in force adds no
// revision, but still starts the config deployment if the serving release
// never got it (a config deployment refused at apply): asking again is the
// retry. One that failed after apply did reach the cluster (the applied
// revision is recorded): the app is not reopened, and nothing is retried.
func (s *Service) SetAccess(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.SetAccess) (release.AccessChange, error) {
	if err := input.Validate(); err != nil {
		return release.AccessChange{}, err
	}
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.AccessChange{}, err
	}
	if err := archived(p); err != nil {
		return release.AccessChange{}, err
	}
	// IT's rules judge only a CHANGE, and before the transaction (a refusal,
	// or in 3b the approval request it stores, must not live in a transaction
	// that rolls back on that same refusal). The mode already in force is not
	// judged: asking again is the retry of a config deployment, and a rule
	// about "public" must not block someone who changes nothing. The mode is
	// read here and re-read under the lock. If it moved away from the target
	// in between (read as a no-op, so nothing was judged, and now it would
	// change), the transaction gives up and the change is judged before
	// trying again: no path applies a mode nobody judged.
	judged := false
	for attempt := 0; ; attempt++ {
		current, err := s.repo.FindSettings(ctx, organization, e.ID)
		if err != nil {
			return release.AccessChange{}, err
		}
		if !judged && current.Access.Mode != input.Mode {
			pending, err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionSetAccess, AccessMode: string(input.Mode)}, gate.Hold{
				Summary: accessSummary(p, e, input.Mode), Payload: accessPayload(input.Mode)})
			if err != nil {
				return release.AccessChange{}, err
			}
			if pending != nil { // held for IT: nothing changed
				return release.AccessChange{Outcome: gate.Holding(*pending)}, nil
			}
			judged = true
		}
		out, err := s.applyAccess(ctx, p, e, input, judged)
		if errors.Is(err, errUnjudged) && attempt < 2 {
			continue
		}
		if errors.Is(err, errUnjudged) {
			return release.AccessChange{}, errx.Conflict("El acceso de esta app cambió mientras lo guardabas. Inténtalo de nuevo.")
		}
		return out, err
	}
}

// errUnjudged: under the lock the mode would change, but the change was not
// judged by IT's rules (it read as a no-op before the transaction).
var errUnjudged = errors.New("access change not judged")

func (s *Service) applyAccess(ctx context.Context, p project.Project, e project.Environment, input release.SetAccess, judged bool) (release.AccessChange, error) {
	var out release.AccessChange
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		state, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		var next *release.Settings
		if prev.Access.Mode != input.Mode {
			if !judged {
				return errUnjudged
			}
			n := prev.WithAccess(input.Mode, input.RequestedBy)
			next = &n
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil || next == nil {
			return err
		}
		return s.events.Publish(ctx, release.NewAccessChanged(p.Organization, input.RequestedBy, p.ID.String(), prev, out.Settings, started, names(p, e)))
	})
	return out, err
}

// lockSettings locks the environment (serialising its settings revisions
// and deployments) and reads its settings in force.
func (s *Service) lockSettings(ctx context.Context, p project.Project, e project.Environment) (release.EnvironmentState, release.Settings, error) {
	state, err := s.lockState(ctx, p, e)
	if err != nil {
		return release.EnvironmentState{}, release.Settings{}, err
	}
	prev, err := s.repo.FindSettings(ctx, p.Organization, e.ID)
	return state, prev, err
}

// changeSettings stores next as the environment's next revision (nil: nothing
// changed) and, when the release serving is not running with the latest
// revision and nothing is in progress, starts the config deployment that
// applies it. With a deployment in progress the revision only waits: that
// deployment applies the latest settings at its apply step, or, if it already
// applied, starts the config deployment when it ends (promote). Nothing
// changed still starts the config deployment if the serving release never got
// the latest revision (a config deployment refused at apply): asking again is
// the retry. One that failed after apply did reach the cluster (the applied
// revision is recorded), and nothing is retried. The caller holds the lock
// (lockSettings) and publishes the change.
func (s *Service) changeSettings(ctx context.Context, state release.EnvironmentState, p project.Project, e project.Environment,
	prev release.Settings, next *release.Settings, by identity.UserID) (release.SettingsChange, *release.DeploymentID, error) {
	out := release.SettingsChange{Settings: prev}
	if next != nil {
		next.Environment, next.Organization, next.CreatedBy = e.ID, p.Organization, by
		stored, err := s.repo.AppendSettings(ctx, *next)
		if err != nil {
			return release.SettingsChange{}, nil, err
		}
		out.Settings = stored
	}
	if state.Current == nil || state.Active != nil || out.Settings.Revision <= state.AppliedRevision() {
		return out, nil, nil
	}
	d, err := s.startConfig(ctx, state, p, e, by)
	if err != nil {
		return release.SettingsChange{}, nil, err
	}
	d = d.WithProgress()
	out.Deployment = &d
	return out, &d.ID, nil
}

// ---- upload tickets ----

// askedWith is the permissions the ticket's requester holds on ctx right now
// (none when the context carries someone else, or no one).
func askedWith(ctx context.Context, by identity.UserID) []string {
	if a := actor.From(ctx); a.User == by {
		return slices.Clone(a.Permissions)
	}
	return nil
}

// ticketRetention is how long an expired ticket is kept (so the person who
// used it late still hears "expired", not "unknown"); the next ticket its
// organization issues sweeps older ones.
const ticketRetention = 24 * time.Hour

// IssueUpload issues a ticket to publish a folder to the environment. The
// token is returned once, only here: the database keeps its hash.
func (s *Service) IssueUpload(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.IssueUpload) (release.UploadTicket, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return release.UploadTicket{}, err
	}
	p, e, err := s.place(ctx, organization, id, env)
	if err != nil {
		return release.UploadTicket{}, err
	}
	if err := archived(p); err != nil {
		return release.UploadTicket{}, err
	}
	token, hash := release.NewUploadToken()
	now := time.Now().UTC()
	t := release.UploadTicket{ID: release.NewUploadTicketID(), Organization: organization, Project: id, Environment: e.ID,
		TokenHash: hash, Name: input.Name, Message: input.Message, Config: input.Config, Client: event.MetaFrom(ctx).Client,
		RequestedBy: input.RequestedBy, Permissions: askedWith(ctx, input.RequestedBy), CreatedAt: now, ExpiresAt: now.Add(release.UploadTicketTTL)}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.DeleteUploadTickets(ctx, organization, now.Add(-ticketRetention)); err != nil {
			return err
		}
		if err := s.repo.CreateUploadTicket(ctx, t); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewUploadTicketIssued(t, names(p, e)))
	})
	if err != nil {
		return release.UploadTicket{}, err
	}
	t.Token = token
	return t, nil
}

// RedeemUpload spends the ticket token and publishes the archive it carries
// as the ticket says. The ticket is spent even when the archive is refused:
// asking for another one is the retry.
func (s *Service) RedeemUpload(ctx context.Context, token string, archive io.Reader) (release.Deployment, error) {
	if !release.ValidUploadToken(token) {
		return release.Deployment{}, errx.NotFound("upload ticket not found")
	}
	hash := release.HashUploadToken(token)
	var t release.UploadTicket
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		now := time.Now().UTC()
		var err error
		t, err = s.repo.ConsumeUploadTicket(ctx, hash, now)
		if err == nil { // the event names the client the ticket was issued to
			by := event.WithMeta(ctx, event.Meta{CorrelationID: event.MetaFrom(ctx).CorrelationID, Client: t.Client})
			return s.events.Publish(by, release.NewUploadTicketUsed(t, s.ticketNames(ctx, t)))
		}
		if !errx.IsType(err, errx.TypeNotFound) {
			return err
		}
		found, ferr := s.repo.FindUploadTicketByHash(ctx, hash) // unknown, used or expired?
		if ferr != nil {
			return ferr
		}
		switch {
		case found.UsedAt != nil:
			e := errx.Conflict("this upload ticket was used already: ask deploy_folder for a new one").WithCode(release.CodeUploadTicketUsed)
			if found.Deployment != nil {
				e = e.WithDetail("deployment_id", found.Deployment.String())
			}
			return e
		case found.Expired(now):
			return errx.Business("this upload ticket expired: ask deploy_folder for a new one").WithCode(release.CodeUploadTicketExpired)
		}
		return err // consumed by a concurrent request between the two statements
	})
	if err != nil {
		return release.Deployment{}, err
	}
	// The deployment is attributed to the client the ticket was issued to,
	// in the same chain as this request, and judged by IT's rules as the
	// person who asked for the ticket (this request carries no token: the
	// ticket is the credential), with the permissions they held then.
	ctx = event.WithMeta(ctx, event.Meta{CorrelationID: event.MetaFrom(ctx).CorrelationID, Client: t.Client})
	ctx = actor.With(ctx, actor.Actor{User: t.RequestedBy, Permissions: t.Permissions})
	d, err := s.DeploySource(ctx, t.Organization, t.Project, t.Environment,
		release.DeploySource{Name: t.Name, Message: t.Message, Config: t.Config, RequestedBy: t.RequestedBy}, archive)
	if err != nil {
		return release.Deployment{}, err
	}
	if err := s.repo.SetUploadDeployment(context.WithoutCancel(ctx), t.Organization, t.ID, d.ID); err != nil {
		slog.Warn("could not record the deployment an upload ticket started", "ticket", t.ID, "deployment", d.ID, "error", err)
	}
	return d, nil
}

// ticketNames words the ticket's event; a project that cannot be read
// leaves the names empty (the event still carries the IDs).
func (s *Service) ticketNames(ctx context.Context, t release.UploadTicket) release.Names {
	p, e, err := s.place(ctx, t.Organization, t.Project, t.Environment)
	if err != nil {
		return release.Names{}
	}
	return names(p, e)
}
