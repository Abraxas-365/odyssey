package releasesvc

import (
	"context"
	"errors"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Workflows returns the deploy workflow:
//
//	authorize  IT's rules judge the deployment as who asked for it; a rule that
//	           wants approval holds it (waiting_approval) until someone decides
//	approval   act on that decision: carry on, or cancel the deployment
//	build      (from source) build the uploaded folder, then record its release
//	apply      hand the desired app (with the deployment's revision) to the cluster
//	wait_ready poll the cluster until that revision answers its health check
//	promote    make the release the environment's current one (the only place it changes)
//
// authorize and approval are first so nothing is built, uploaded or applied
// before IT agrees. Phase 3c adds `authorize_scan` (and `approval_scan`) right
// after build, with the image's scan, judged the same way (see
// authorizeScanStep).
//
// Workflow steps are positional (a run stores the index of its step), so a run
// already in flight when steps were added resumes at the same index, which now
// names an EARLIER step: with authorize and approval added (3b) it repeated
// work; with authorize_scan and approval_scan added after build (3c, two more)
// a run that was at apply resumes at authorize_scan (it judges the build's scan
// like a new deployment would; a run with no build or actor is let through, see
// authorizeScanStep), one at wait_ready at approval_scan (no output of
// authorize_scan: nothing was asked, carry on) and one at promote at
// apply. approval treats a run with no gate output as allowed, and build,
// apply and wait_ready are idempotent, so it simply repeats work and never
// skips a step.
//
// On failure (a coded error, retries exhausted or the readiness timeout) the
// deployment fails and, when another release was serving and the cluster
// was touched (not a failed build), a deployment of that release starts at
// once (auto_rollback): the cluster goes back to what the environment's
// current release says.
func (s *Service) Workflows() []workflow.Definition {
	return []workflow.Definition{{
		Name: release.WorkflowDeploy,
		Steps: []workflow.Step{
			{Name: release.StepAuthorize, Run: s.authorizeStep, MaxAttempts: 8},
			{Name: release.StepApproval, Run: s.approvalStep, MaxAttempts: 8},
			// Polls don't count as attempts; the build's own timeout ends it
			// first, the step's Timeout bounds a build that never reports.
			{Name: release.StepBuild, Run: s.build, Timeout: build.Timeout + 5*time.Minute, MaxAttempts: 30},
			// The scan is recorded by build.scan on its own. Polls don't count
			// as attempts; ScanWait, inside the step, ends the wait (no step
			// Timeout: the step may also park for an approval).
			{Name: release.StepAuthorizeScan, Run: s.authorizeScanStep, MaxAttempts: 8},
			{Name: release.StepApprovalScan, Run: s.approvalScanStep, MaxAttempts: 8},
			// ~4 min of retries (backoff 2^n s) ride out a cluster briefly unreachable.
			{Name: release.StepApply, Run: s.apply, MaxAttempts: 8},
			// Polls don't count as attempts; errors (cluster or DB unreachable)
			// do, and the step's Timeout bounds the whole wait anyway.
			{Name: release.StepWaitReady, Run: s.waitReady, Timeout: s.readyBound(), MaxAttempts: 30},
			{Name: release.StepPromote, Run: s.promote},
		},
		OnFailure: s.deployFailed,
	}}
}

// resourcesGrace is how long, beyond the time the agent waits for an app's
// resources (resourcekind.Wait), wait_ready lets the agent report it.
const resourcesGrace = 3 * time.Minute

// readyBound is wait_ready's outer bound. An app with resources may wait
// that long: the agent holds its revision back until they are ready
// (resourcekind.Wait) and then reports why it gave up. One without resources
// is bounded by ReadyTimeout inside waitReady (see stillWaiting).
func (s *Service) readyBound() time.Duration {
	return max(s.opts.ReadyTimeout, resourcekind.Wait+resourcesGrace)
}

// applyOutput is what the apply step saves for wait_ready.
type applyOutput struct {
	// Resources: the desired app uses resources (the wait may be long).
	Resources bool `json:"resources"`
	// AppliedAt is when the cluster took the app.
	AppliedAt time.Time `json:"applied_at"`
}

// job is what a step works on.
type job struct {
	deployment release.Deployment
	target     release.Target
	app        string
}

// load reads the run's deployment; a finished one (cancelled by a newer
// deployment) ends the run without effect.
func (s *Service) load(ctx context.Context, st *workflow.State) (job, error) {
	var in release.DeployInput
	if err := st.Input(&in); err != nil {
		return job{}, workflow.Fail(err)
	}
	org := st.Run.Organization
	d, err := s.repo.FindDeployment(ctx, org, in.Deployment)
	if err != nil {
		return job{}, err
	}
	if d.Status.Finished() {
		return job{}, workflow.Fail(errSuperseded)
	}
	state, err := s.repo.FindState(ctx, org, d.Environment)
	if err != nil {
		return job{}, err
	}
	return job{deployment: d, app: state.App,
		target: release.Target{Organization: org, Project: d.Project, Environment: d.Environment}}, nil
}

// progress records the step a deployment is at; a deployment finished in the
// meantime (cancelled by a newer one) ends the run.
func (s *Service) progress(ctx context.Context, j job, status release.Status, step string) error {
	if j.deployment.Status == status && j.deployment.Step == step {
		return nil
	}
	err := s.repo.SetProgress(ctx, j.target.Organization, j.deployment.ID, status, step)
	if errx.IsType(err, errx.TypeNotFound) {
		return workflow.Fail(errSuperseded)
	}
	return err
}

func (s *Service) apply(ctx context.Context, st *workflow.State) (any, error) {
	j, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	if j.deployment.Release == nil {
		return nil, workflow.Fail(errx.Internal("deployment " + j.deployment.ID.String() + " has no release to apply"))
	}
	if err := s.progress(ctx, j, release.StatusApplying, release.StepApply); err != nil {
		return nil, err
	}
	r, err := s.repo.FindRelease(ctx, j.target.Organization, *j.deployment.Release)
	if err != nil {
		return nil, err
	}
	settings, err := s.resolveSettings(ctx, j)
	if err != nil {
		return nil, err
	}
	desired := release.Desired(j.app, j.deployment, r).WithSettings(settings) // the one place access, variables and secrets reach the cluster
	if err := s.handOver(ctx, j, desired, settings.Revision); err != nil {
		if coded(err, release.CodeApplyFailed) {
			return nil, workflow.Fail(err)
		}
		return nil, err // the cluster is unreachable: retry
	}
	return applyOutput{Resources: len(desired.Resources) > 0, AppliedAt: s.now().UTC()}, nil
}

// handOver tells the cluster, in one transaction that holds the project (so
// it is not archived meanwhile: Archive takes it for update) and then the
// environment (the order of every lock here, and RetireArchived's). Either
// this commits before the archive, and the retirement that follows carries a
// higher revision, or it sees the project archived and cancels the
// deployment as RetireArchived would (reason archived), without reaching the
// cluster. Architecture test: TestTheClusterIsToldOnlyWhileTheProjectIsHeld.
func (s *Service) handOver(ctx context.Context, j job, desired release.DesiredApp, settings int64) error {
	org := j.target.Organization
	var stopped bool
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		p, err := s.projects.Hold(ctx, org, j.target.Project)
		if err != nil {
			return err
		}
		if p.Archived() {
			e, err := s.projects.FindEnvironment(ctx, org, p.ID, j.target.Environment)
			if err != nil {
				return err
			}
			if _, err := s.lockState(ctx, p, e); err != nil {
				return err
			}
			// An archived project refuses every change: its last editor is
			// whoever archived it.
			if _, err := s.cancelArchived(ctx, p, e, j.deployment.ID, p.UpdatedBy); err != nil {
				return err
			}
			stopped = true
			return s.repo.Settle(ctx, org, e.ID, j.deployment.ID)
		}
		if _, err := s.repo.LockState(ctx, release.EnvironmentState{Environment: j.target.Environment, Organization: org,
			Project: p.ID, App: j.app}); err != nil {
			return err
		}
		if err := s.cluster.Apply(ctx, j.target, desired); err != nil {
			return err
		}
		// The cluster has been told (a refusal above never gets here). From now
		// on the app is protected as these settings say, whatever happens to
		// this deployment: a config deployment that fails afterwards does NOT
		// reopen the app by itself, and the sign-in keeps asking for this mode.
		// The console compares the latest settings with this revision.
		return s.repo.SetAppliedSettings(ctx, org, j.target.Environment, j.deployment.Revision, settings)
	})
	if err == nil && stopped {
		return workflow.Fail(errSuperseded) // cancelled: the run ends quietly
	}
	return err
}

// resolveSettings is the environment's settings this deployment applies. They
// are resolved here, not when the deployment starts, so one that has not
// applied yet takes the latest; the revision is recorded before the cluster is
// told and then kept, so an attempt retried after the cluster took the app
// applies the very same settings (the cluster ignores a second app with the
// same deployment revision).
func (s *Service) resolveSettings(ctx context.Context, j job) (release.Settings, error) {
	org, env := j.target.Organization, j.target.Environment
	if j.deployment.SettingsRevision != nil {
		return s.repo.FindSettingsRevision(ctx, org, env, *j.deployment.SettingsRevision)
	}
	latest, err := s.repo.FindSettings(ctx, org, env)
	if err != nil {
		return release.Settings{}, err
	}
	if err := s.repo.SetSettingsRevision(ctx, org, j.deployment.ID, latest.Revision); err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return release.Settings{}, workflow.Fail(errSuperseded)
		}
		return release.Settings{}, err
	}
	return latest, nil
}

// build starts the deployment's build and follows it until its image is
// ready; then, in one transaction, it creates the release (the image plus
// the deployment's configuration) and moves the deployment on to apply. A
// deployment of an image has nothing to build.
func (s *Service) build(ctx context.Context, st *workflow.State) (any, error) {
	j, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	d, org := j.deployment, j.target.Organization
	if d.Build == nil || d.Release != nil { // an image, or built by an earlier attempt
		return nil, nil
	}
	if d.Source == nil || d.Config == nil {
		return nil, workflow.Fail(errx.Internal("deployment " + d.ID.String() + " has a build but no source"))
	}
	b, err := s.builder.Status(ctx, org, *d.Build)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil, s.startBuild(ctx, j)
	}
	if err != nil {
		return nil, err
	}
	switch b.Status {
	case build.StatusSucceeded:
		return nil, s.built(ctx, j, b)
	case build.StatusFailed:
		f := build.Failure{Code: build.CodeInternal}
		if b.Error != nil {
			f = *b.Error
		}
		return nil, workflow.Fail(release.BuildFailed(f))
	case build.StatusCancelled: // only a newer deployment cancels it, and it cancels this one too
		return nil, workflow.Fail(errSuperseded)
	}
	return nil, workflow.Poll(s.opts.PollEvery)
}

// startBuild starts the build holding the deployment's row: begin, which
// cancels a superseded deployment's build, takes the same lock, so a build
// never starts for a deployment already cancelled.
func (s *Service) startBuild(ctx context.Context, j job) error {
	org := j.target.Organization
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		d, err := s.repo.FindDeploymentForUpdate(ctx, org, j.deployment.ID)
		if err != nil {
			return err
		}
		if d.Status.Finished() {
			return workflow.Fail(errSuperseded)
		}
		return s.builder.Start(ctx, org, build.Start{ID: *d.Build, Project: d.Project, Environment: d.Environment,
			Source: d.Source.ID, RequestedBy: d.RequestedBy})
	})
	var e *errx.Error
	if errors.As(err, &e) && e.Type == errx.TypeBusiness && e.Code != "" { // the build cannot start (SOURCE_EXPIRED)
		return workflow.Fail(release.BuildFailed(build.Failure{Code: e.Code, Message: build.Message(e.Code)}))
	}
	if err != nil {
		return err
	}
	return workflow.Poll(s.opts.PollEvery)
}

// built records the release of a succeeded build, once.
func (s *Service) built(ctx context.Context, j job, b build.Build) error {
	org := j.target.Organization
	p, e, err := s.place(ctx, org, j.deployment.Project, j.deployment.Environment)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := s.lockState(ctx, p, e); err != nil {
			return err
		}
		d, err := s.repo.FindDeploymentForUpdate(ctx, org, j.deployment.ID)
		if err != nil {
			return err
		}
		if d.Status.Finished() {
			return workflow.Fail(errSuperseded)
		}
		if d.Release != nil {
			return nil // recorded by an earlier attempt
		}
		r := release.Release{ID: release.NewID(), Organization: org, Project: d.Project, Image: b.Image,
			Config: *d.Config, CreatedBy: d.RequestedBy}
		if !release.ValidImage(r.Image) {
			return workflow.Fail(release.BuildFailed(build.Failure{Code: build.CodeInternal,
				Detail: "the builder reported an image that is not pinned by digest: " + r.Image}))
		}
		if err := s.repo.CreateRelease(ctx, r); err != nil {
			return err
		}
		if err := s.repo.SetBuilt(ctx, org, d.ID, r); err != nil {
			return err
		}
		return s.events.Publish(ctx, release.NewCreated(org, d.RequestedBy, r, names(p, e)))
	})
}

func (s *Service) waitReady(ctx context.Context, st *workflow.State) (any, error) {
	j, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	if err := s.progress(ctx, j, release.StatusVerifying, release.StepWaitReady); err != nil {
		return nil, err
	}
	status, err := s.cluster.Status(ctx, j.target, j.app)
	if err != nil {
		return nil, err
	}
	switch {
	case status.ObservedRevision > j.deployment.Revision:
		return nil, workflow.Fail(errSuperseded) // a newer deployment took over
	case status.ObservedRevision < j.deployment.Revision:
		return nil, s.stillWaiting(st) // the cluster has not seen it yet
	case status.Phase == release.PhaseReady:
		return status.URL, nil
	case status.Phase == release.PhaseFailed:
		return nil, workflow.Fail(release.Unready(status))
	}
	return nil, s.stillWaiting(st)
}

// stillWaiting polls again, unless the app has no resources and has been
// waiting longer than ReadyTimeout: then it fails as the step's timeout
// always did (NOT_READY), so an app without resources keeps its bound while
// the step's own Timeout leaves room for the resources of one that has them.
// A run that started before the apply step saved its output (none, or null)
// is bounded by the step's Timeout alone.
func (s *Service) stillWaiting(st *workflow.State) error {
	var applied applyOutput
	if err := st.Output(release.StepApply, &applied); err == nil && !applied.Resources && !applied.AppliedAt.IsZero() &&
		s.now().Sub(applied.AppliedAt) > s.opts.ReadyTimeout {
		return workflow.Fail(release.Failed(release.CodeNotReady, "La app no quedó lista a tiempo."))
	}
	return workflow.Poll(s.opts.PollEvery)
}

// promote is the only code that changes what an environment serves
// (architecture test: TestOnlyThePromoteStepMovesTheCurrentRelease).
func (s *Service) promote(ctx context.Context, st *workflow.State) (any, error) {
	var url string
	if err := st.Output(release.StepWaitReady, &url); err != nil {
		return nil, workflow.Fail(err)
	}
	var in release.DeployInput
	if err := st.Input(&in); err != nil {
		return nil, workflow.Fail(err)
	}
	org := st.Run.Organization
	peek, err := s.repo.FindDeployment(ctx, org, in.Deployment)
	if err != nil {
		return nil, err
	}
	if peek.Status.Finished() {
		if peek.Status == release.StatusReady {
			return url, nil // promoted by an earlier attempt
		}
		return nil, workflow.Fail(errSuperseded)
	}
	if err := s.progress(ctx, job{deployment: peek, target: release.Target{Organization: org}}, release.StatusVerifying, release.StepPromote); err != nil {
		return nil, err
	}
	p, e, err := s.place(ctx, org, peek.Project, peek.Environment)
	if err != nil {
		return nil, err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, err := s.lockState(ctx, p, e)
		if err != nil {
			return err
		}
		d, err := s.repo.FindDeploymentForUpdate(ctx, org, in.Deployment)
		if err != nil {
			return err
		}
		switch d.Status {
		case release.StatusReady:
			return nil // promoted by an earlier attempt
		case release.StatusCancelled, release.StatusFailed:
			return workflow.Fail(errSuperseded)
		}
		if err := s.repo.Promote(ctx, org, d.Environment, d, url); err != nil {
			return err
		}
		if err := s.repo.Finish(ctx, org, d.ID, release.StatusReady, nil); err != nil {
			return err
		}
		if err := s.events.Publish(ctx, release.NewDeploymentReady(org, d.RequestedBy, d, url, names(p, e))); err != nil {
			return err
		}
		// Settings changed while it was applying: the cluster has older ones.
		state.Active, state.Current, state.CurrentDeployment = nil, d.Release, &d.ID
		return s.applyNewerSettings(ctx, state, p, e)
	})
	return url, err
}

// deployFailed is the workflow's OnFailure, in the transaction that marks
// the run failed.
func (s *Service) deployFailed(ctx context.Context, st *workflow.State, cause error) error {
	if errors.Is(cause, errSuperseded) {
		return nil
	}
	var in release.DeployInput
	if err := st.Input(&in); err != nil {
		return nil // nothing to record: the input is not a deployment
	}
	org := st.Run.Organization
	peek, err := s.repo.FindDeployment(ctx, org, in.Deployment)
	if err != nil {
		return err
	}
	p, e, err := s.place(ctx, org, peek.Project, peek.Environment)
	if err != nil {
		return err
	}
	state, err := s.lockState(ctx, p, e)
	if err != nil {
		return err
	}
	d, err := s.repo.FindDeploymentForUpdate(ctx, org, in.Deployment)
	if err != nil || d.Status.Finished() {
		return err
	}
	failure := release.FailureOf(d.Step, cause)
	if err := s.repo.Finish(ctx, org, d.ID, release.StatusFailed, &failure); err != nil {
		return err
	}
	if err := s.repo.Settle(ctx, org, d.Environment, d.ID); err != nil {
		return err
	}
	d.Status, d.Error = release.StatusFailed, &failure

	// A build that is still running is no longer needed.
	if d.Build != nil && d.Release == nil {
		if err := s.builder.Cancel(ctx, org, *d.Build, build.Cancel{}); err != nil {
			return err
		}
	}

	// Put the cluster back to the serving release, unless this deployment
	// never reached it (it failed building), was already doing that (no
	// loops) or nothing was serving.
	var restore *release.DeploymentID
	// A deployment that failed at apply never reached the cluster either: the
	// cluster refused it (for instance ACCESS_UNAVAILABLE, a protected mode on
	// a cluster without the gate) or could not be reached, so it still runs
	// what it ran and there is nothing to restore. A restore would only apply
	// the serving release under the same settings, to be refused the same way.
	neverReached := d.Step == release.StepBuild || d.Step == release.StepApply
	// An archived project gets nothing back: its app is being retired. The
	// project is read unlocked: a restore created as the archive commits is
	// stopped at handOver, the real guard; this only saves the work.
	if state.Current != nil && !neverReached && d.Reason != release.ReasonAutoRollback && !p.Archived() &&
		(d.Release == nil || *state.Current != *d.Release) {
		id := release.NewDeploymentID()
		restore = &id
	}
	if err := s.events.Publish(ctx, release.NewDeploymentFailed(org, d.RequestedBy, d, restore, names(p, e))); err != nil {
		return err
	}
	if restore == nil {
		// A deployment that never touched the cluster leaves the settings that
		// changed meanwhile to nobody, except a config deployment (no loops:
		// asking again is the retry).
		if state.Current != nil && neverReached && d.Reason != release.ReasonConfig && state.CurrentDeployment != nil {
			state.Active = nil
			return s.applyNewerSettings(ctx, state, p, e)
		}
		return nil
	}
	r, err := s.repo.FindRelease(ctx, org, *state.Current)
	if err != nil {
		return err
	}
	state.Active = nil
	_, _, err = s.begin(ctx, state, p, e, release.Deployment{ID: *restore, Release: &r.ID, Image: r.Image,
		Reason: release.ReasonAutoRollback, RollbackOf: &d.ID})
	return err
}

func coded(err error, code string) bool {
	var e *errx.Error
	return errors.As(err, &e) && e.Code == code
}

// applyNewerSettings starts a config deployment of the current release when
// the environment's latest settings are newer than the ones the cluster was
// last told (state.AppliedSettings). The caller holds the environment's state
// lock with Active cleared. Only the end of a deployment that applied older
// settings, or of a build that never reached the cluster, calls it: a config
// deployment that fails does not start another (no loops; a new PUT retries).
func (s *Service) applyNewerSettings(ctx context.Context, state release.EnvironmentState, p project.Project, e project.Environment) error {
	// An archived project's app is being retired (read unlocked: handOver is
	// the real guard, this only saves the work).
	if state.Current == nil || p.Archived() {
		return nil
	}
	latest, err := s.repo.FindSettings(ctx, p.Organization, e.ID)
	if err != nil || latest.Revision <= state.AppliedRevision() {
		return err
	}
	_, err = s.startConfig(ctx, state, p, e, latest.CreatedBy)
	return err
}

// startConfig begins the deployment that applies the latest settings to the
// release the environment serves (state is locked, nothing is in progress).
func (s *Service) startConfig(ctx context.Context, state release.EnvironmentState, p project.Project, e project.Environment,
	by identity.UserID) (release.Deployment, error) {
	r, err := s.repo.FindRelease(ctx, p.Organization, *state.Current)
	if err != nil {
		return release.Deployment{}, err
	}
	d, _, err := s.begin(ctx, state, p, e, release.Deployment{ID: release.NewDeploymentID(), Release: &r.ID, Image: r.Image,
		Reason: release.ReasonConfig, RequestedBy: by})
	return d, err
}
