package releasesvc

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// approvalWait is how long the authorize step parks for a decision: a day
// past a request's life (7 days), so the expiry sweep always answers first
// and this timeout is only the backstop.
const approvalWait = 8 * 24 * time.Hour

// judged says which IT action a deployment is judged as; the platform's own
// deployments (the automatic restore, a config deployment that applies
// settings already judged where they changed) are not judged at all.
func judged(r release.Reason) (string, bool) {
	switch r {
	case release.ReasonDeploy:
		return gate.ActionDeploy, true
	case release.ReasonRollback:
		return gate.ActionRollback, true
	}
	return "", false
}

// authorizeStep is the deploy workflow's first step: IT's rules judge the
// deployment as who asked for it, with the permissions they held then (the
// run's input carries that snapshot; a push carries its system actor).
//
//   - allowed: the run goes on (the output says so);
//   - denied: the deployment fails, POLICY_DENIED, before anything is built;
//   - approval: the request is stored (idempotently, by run and step), the
//     deployment becomes waiting_approval and the run parks until the
//     approval domain signals the decision (the output of this step).
//
// Phase 3c adds `authorize_scan` after build: the same judgment with the
// image's scan, through the same Hold/WaitFor shape.
func (s *Service) authorizeStep(ctx context.Context, st *workflow.State) (any, error) {
	var in release.DeployInput
	if err := st.Input(&in); err != nil {
		return nil, workflow.Fail(err)
	}
	j, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	d, org := j.deployment, j.target.Organization
	action, ok := judged(d.Reason)
	if !ok {
		return gate.Decision{Status: gate.DecisionAllowed}, nil
	}
	if in.Actor.IsZero() {
		// A run started before 3b carries no actor (the input was only the
		// deployment id), and the request was already judged (deny-only) when
		// it was asked. Judging it now as nobody would refuse or hold a
		// deployment that was accepted under the old rules, so it is let
		// through (docs/approvals.md "Upgrading"). A new run can never get
		// here: begin refuses to start a judged deployment with no actor.
		slog.Warn("deploy authorize: run without an actor (started before approvals): allowed", "deployment", d.ID, "run", st.Run.ID)
		return gate.Decision{Status: gate.DecisionAllowed}, nil
	}
	if s.policy == nil {
		return nil, errx.Internal("IT's rules are not wired")
	}
	p, e, err := s.place(ctx, org, d.Project, d.Environment)
	if err != nil {
		return nil, err
	}
	ctx = actor.With(ctx, in.Actor) // judged, and the request held, as who asked
	verdict, err := s.policy.Check(ctx, org, gate.Request{Action: action, Tier: string(p.Tier), ProjectSlug: p.Slug, Environment: e.Slug})
	if err != nil {
		return nil, err
	}
	switch verdict.Effect {
	case gate.Deny:
		if err := s.withdraw(ctx, org, st, release.StepAuthorize); err != nil {
			return nil, err
		}
		return nil, workflow.Fail(refused(verdict))
	case gate.Approval:
	default:
		if err := s.withdraw(ctx, org, st, release.StepAuthorize); err != nil {
			return nil, err
		}
		return gate.Decision{Status: gate.DecisionAllowed}, nil
	}
	if s.approvals == nil {
		return nil, errx.Internal("approvals are not wired")
	}
	summary := "Publicar " + where(p, e)
	if d.Reason == release.ReasonRollback {
		summary = "Volver a una versión anterior de " + where(p, e)
	}
	if err := s.holdDeployment(ctx, st, p, e, d, action, verdict, release.StepAuthorize, summary); err != nil {
		return nil, err
	}
	return nil, workflow.WaitFor(gate.SignalFor(release.StepAuthorize), approvalWait)
}

// holdDeployment opens the request of a deployment's gate step, atomic with
// the deployment's waiting_approval status (the step it was at stays: the
// console's progress; only the status says it waits). The one place a Hold
// runs inside a transaction (TestApprovalsAreNotHeldInsideATransaction): the
// verdict is already in.
func (s *Service) holdDeployment(ctx context.Context, st *workflow.State, p project.Project, e project.Environment,
	d release.Deployment, action string, verdict gate.Verdict, step, summary string) error {
	org := d.Organization
	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.repo.FindDeploymentForUpdate(ctx, org, d.ID)
		if err != nil {
			return err
		}
		if cur.Status.Finished() {
			return workflow.Fail(errSuperseded)
		}
		if _, err := s.approvals.Hold(ctx, org, gate.Hold{Action: action, Project: p.ID.String(), Environment: e.ID.String(),
			Summary: summary, Verdict: verdict, Run: st.Run.ID.String(), RunStep: step,
			Payload: release.DeploymentApproval{Op: release.OpDeployment, Deployment: d.ID, Reason: d.Reason}}); err != nil {
			return err
		}
		return s.repo.SetProgress(ctx, org, d.ID, release.StatusWaitingApproval, cur.Step)
	})
}

// withdraw cancels the request this run may have opened earlier at step: the
// step ran before (a crash or retry between opening the request and parking),
// and the rules have since changed to allow or deny the deployment, so nobody
// should be asked to decide it. A request already decided, and a run that
// never asked, are left alone (CancelRun is a no-op for them).
func (s *Service) withdraw(ctx context.Context, org identity.OrganizationID, st *workflow.State, step string) error {
	if s.approvals == nil {
		return nil
	}
	return s.approvals.CancelRun(ctx, org, st.Run.ID.String(), step)
}

// refused is the failure of a deployment IT's rules deny: plain words (no
// ": ", which separates the detail), the rule's name included.
func refused(v gate.Verdict) error {
	msg := "Las reglas de TI no permiten esta publicación."
	var e *errx.Error
	if errors.As(v.Err(), &e) {
		msg = e.Message
	}
	return release.Failed(release.CodePolicyDenied, strings.ReplaceAll(msg, ": ", " — "))
}

// approvalStep acts on what became of the deployment's request: it carries on
// (allowed, or approved: the approval is used up and the deployment goes back
// to the status it had), or cancels the deployment (rejected, expired or
// taken back).
func (s *Service) approvalStep(ctx context.Context, st *workflow.State) (any, error) {
	return nil, s.act(ctx, st, release.StepAuthorize)
}

// approvalScanStep is approvalStep for the decision authorize_scan asked.
func (s *Service) approvalScanStep(ctx context.Context, st *workflow.State) (any, error) {
	return nil, s.act(ctx, st, release.StepAuthorizeScan)
}

// act reads what the gate step saved (its own decision, or the signal's) and
// carries on, resumes or cancels accordingly.
func (s *Service) act(ctx context.Context, st *workflow.State, gateStep string) error {
	var dec gate.Decision
	if err := st.Output(gateStep, &dec); err != nil {
		return nil // a run from before this step existed: nothing was asked
	}
	switch dec.Status {
	case "", gate.DecisionAllowed:
		return nil
	case gate.DecisionApproved:
		return s.resume(ctx, st, dec, gateStep)
	}
	return s.cancelHeld(ctx, st, dec)
}

// resume uses up the approval and puts the deployment back where it was. What
// "where it was" is depends on the gate: authorize is before the build
// (pending, or building when a build exists but its release is not recorded);
// authorize_scan is the end of the build for the console (the release exists,
// apply has not started), so it goes back to building, as authorizeScanStep
// left it, and not to pending, which would say it is queued for nothing.
func (s *Service) resume(ctx context.Context, st *workflow.State, dec gate.Decision, gateStep string) error {
	j, err := s.load(ctx, st)
	if err != nil {
		return err
	}
	d, org := j.deployment, j.target.Organization
	if s.approvals == nil {
		return errx.Internal("approvals are not wired")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.repo.FindDeploymentForUpdate(ctx, org, d.ID)
		if err != nil {
			return err
		}
		if cur.Status.Finished() {
			return workflow.Fail(errSuperseded)
		}
		if err := s.approvals.Consume(ctx, org, dec.Request); err != nil {
			return err
		}
		status := release.StatusPending
		if cur.Build != nil && (cur.Release == nil || gateStep == release.StepAuthorizeScan) {
			status = release.StatusBuilding
		}
		if cur.Status != release.StatusWaitingApproval {
			return nil
		}
		return s.repo.SetProgress(ctx, org, d.ID, status, cur.Step)
	})
}

// cancelHeld ends a deployment IT did not approve (it never ran): cancelled,
// with the reason, its build (if any) dropped, and the environment's settings
// that changed meanwhile applied like when a deployment fails before the
// cluster. The run then ends quietly (errSuperseded).
func (s *Service) cancelHeld(ctx context.Context, st *workflow.State, dec gate.Decision) error {
	reason := release.CancelApprovalRejected
	switch dec.Status {
	case gate.DecisionExpired:
		reason = release.CancelApprovalExpired
	case gate.DecisionCancelled:
		reason = release.CancelApprovalCanceled
	}
	j, err := s.load(ctx, st)
	if err != nil {
		return err
	}
	d, org := j.deployment, j.target.Organization
	p, e, err := s.place(ctx, org, d.Project, d.Environment)
	if err != nil {
		return err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		state, err := s.lockState(ctx, p, e)
		if err != nil {
			return err
		}
		cur, err := s.repo.FindDeploymentForUpdate(ctx, org, d.ID)
		if err != nil {
			return err
		}
		if cur.Status.Finished() {
			return nil
		}
		if err := s.repo.Finish(ctx, org, d.ID, release.StatusCancelled, nil); err != nil {
			return err
		}
		if err := s.repo.Settle(ctx, org, d.Environment, d.ID); err != nil {
			return err
		}
		if cur.Build != nil && cur.Release == nil {
			if err := s.cancelBuilds(ctx, cur, cur.RequestedBy); err != nil {
				return err
			}
		}
		if err := s.events.Publish(ctx, release.NewDeploymentHeld(org, cur.RequestedBy, cur, reason, names(p, e))); err != nil {
			return err
		}
		if state.Current != nil && state.CurrentDeployment != nil && cur.Reason != release.ReasonConfig {
			state.Active = nil
			return s.applyNewerSettings(ctx, state, p, e)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return workflow.Fail(errSuperseded)
}
