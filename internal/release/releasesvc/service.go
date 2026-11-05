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
	"io"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
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
	// AntigravityClientID is IAMKIT_MCP_CLIENT_ANTIGRAVITY_ID: the OAuth
	// client of Antigravity, to name it in a deployment's requested_via.
	AntigravityClientID string
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
	engine   workflow.Engine
	tx       txn.Runner
	events   event.Publisher
	opts     Options
	now      func() time.Time // the clock (tests set it)
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
		d, superseded, err = s.begin(ctx, state, p, e, release.Deployment{ID: release.NewDeploymentID(), Release: &r.ID,
			Image: r.Image, Reason: release.ReasonDeploy, RequestedBy: input.RequestedBy})
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
	src, err := s.builder.Upload(ctx, organization, id, build.Upload{Name: input.Name, UploadedBy: input.RequestedBy}, archive)
	if err != nil {
		return release.Deployment{}, err
	}
	d, err := s.startSource(ctx, organization, id, e, src, input.Config, input.RequestedBy, "")
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
	return s.startSource(ctx, organization, id, e, src, input.Config, input.RequestedBy, input.Via)
}

// startSource writes the deployment that builds src with cfg, holding the
// project so it cannot have been archived meanwhile, and stops the one it
// superseded.
func (s *Service) startSource(ctx context.Context, organization identity.OrganizationID, id project.ID, e project.Environment, src build.Source, cfg release.Config, by identity.UserID, via string) (release.Deployment, error) {
	b := build.NewID()
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
		state, err := s.lockState(ctx, held, e)
		if err != nil {
			return err
		}
		d, superseded, err = s.begin(ctx, state, held, e, release.Deployment{ID: release.NewDeploymentID(), Build: &b,
			Source: &release.DeploymentSource{ID: src.ID, Name: src.Name, Size: src.Size, Git: src.Git}, Config: &cfg,
			Reason: release.ReasonDeploy, RequestedBy: by, RequestedVia: via})
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
	if old.Status != release.StatusReady || old.Release == nil {
		return release.Deployment{}, errx.Business("only a version that went live can be restored").WithCode(release.CodeNotRollbackable)
	}
	r, err := s.repo.FindRelease(ctx, organization, *old.Release)
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
		d.RequestedVia = release.ViaFor(d.Reason, event.MetaFrom(ctx).Client, s.opts.AntigravityClientID)
	}
	actor := d.RequestedBy
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
			if old.Build != nil && old.Release == nil { // nobody needs its image now
				if err := s.builder.Cancel(ctx, p.Organization, *old.Build, build.Cancel{CancelledBy: actor}); err != nil {
					return release.Deployment{}, nil, err
				}
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
	return out, nil
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
	var out release.AccessChange
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, prev, err := s.lockSettings(ctx, p, e)
		if err != nil {
			return err
		}
		var next *release.Settings
		if prev.Access.Mode != input.Mode {
			n := prev.WithAccess(input.Mode, input.RequestedBy)
			next = &n
		}
		var started *release.DeploymentID
		if out, started, err = s.changeSettings(ctx, state, p, e, prev, next, input.RequestedBy); err != nil || next == nil {
			return err
		}
		return s.events.Publish(ctx, release.NewAccessChanged(organization, input.RequestedBy, id.String(), prev, out.Settings, started, names(p, e)))
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
		TokenHash: hash, Name: input.Name, Config: input.Config, Client: event.MetaFrom(ctx).Client,
		RequestedBy: input.RequestedBy, CreatedAt: now, ExpiresAt: now.Add(release.UploadTicketTTL)}
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
	// in the same chain as this request.
	ctx = event.WithMeta(ctx, event.Meta{CorrelationID: event.MetaFrom(ctx).CorrelationID, Client: t.Client})
	d, err := s.DeploySource(ctx, t.Organization, t.Project, t.Environment,
		release.DeploySource{Name: t.Name, Config: t.Config, RequestedBy: t.RequestedBy}, archive)
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
