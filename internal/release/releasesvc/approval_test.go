package releasesvc

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// The fakes embed the port (nil) and answer only what the approval steps ask;
// anything else panics, which also proves what they do NOT touch.

type approvalRepo struct {
	release.Repository
	dep      release.Deployment
	state    release.EnvironmentState
	progress []release.Status
	settled  bool
}

func (r *approvalRepo) FindDeployment(context.Context, identity.OrganizationID, release.DeploymentID) (release.Deployment, error) {
	return r.dep, nil
}
func (r *approvalRepo) FindDeploymentForUpdate(context.Context, identity.OrganizationID, release.DeploymentID) (release.Deployment, error) {
	return r.dep, nil
}
func (r *approvalRepo) FindState(context.Context, identity.OrganizationID, project.EnvironmentID) (release.EnvironmentState, error) {
	return r.state, nil
}
func (r *approvalRepo) LockState(_ context.Context, s release.EnvironmentState) (release.EnvironmentState, error) {
	return r.state, nil
}
func (r *approvalRepo) SetProgress(_ context.Context, _ identity.OrganizationID, _ release.DeploymentID, status release.Status, step string) error {
	r.progress = append(r.progress, status)
	r.dep.Status, r.dep.Step = status, step
	return nil
}
func (r *approvalRepo) Finish(_ context.Context, _ identity.OrganizationID, _ release.DeploymentID, status release.Status, _ *release.Failure) error {
	r.dep.Status = status
	return nil
}
func (r *approvalRepo) Settle(context.Context, identity.OrganizationID, project.EnvironmentID, release.DeploymentID) error {
	r.settled = true
	return nil
}

// rules answers a fixed effect and remembers who was judged.
type rules struct {
	effect gate.Effect
	asked  int
	actors []actor.Actor
}

func (r *rules) Check(ctx context.Context, _ identity.OrganizationID, _ gate.Request) (gate.Verdict, error) {
	r.asked++
	r.actors = append(r.actors, actor.From(ctx))
	v := gate.Verdict{Effect: r.effect, Rules: []gate.Rule{{ID: "r1", Name: "Producción se aprueba"}}}
	if r.effect == gate.Approval {
		v.Approvers = []string{"odyssey:approvals:decide"}
	}
	return v, nil
}

// approvals records what the steps hold, consume and cancel.
type approvals struct {
	gate.RunApprover
	held      []gate.Hold
	consumed  []string
	cancelled []string
}

func (a *approvals) Hold(_ context.Context, _ identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	a.held = append(a.held, h)
	return gate.NewPending("req-1"), nil
}
func (a *approvals) Consume(_ context.Context, _ identity.OrganizationID, request string) error {
	a.consumed = append(a.consumed, request)
	return nil
}
func (a *approvals) CancelRun(_ context.Context, _ identity.OrganizationID, run string) error {
	a.cancelled = append(a.cancelled, run)
	return nil
}

type buildCancels struct {
	release.Builder
	cancelled []build.ID
}

func (b *buildCancels) Cancel(_ context.Context, _ identity.OrganizationID, id build.ID, _ build.Cancel) error {
	b.cancelled = append(b.cancelled, id)
	return nil
}

type approvalRig struct {
	svc      *Service
	repo     *approvalRepo
	rules    *rules
	approver *approvals
	events   *eventtest.Recorder
	builder  *buildCancels
	st       *workflow.State
	actor    actor.Actor
}

var requester = identity.MustParseUserID("0193b5a0-0000-7000-8000-000000000001")

func newApprovalRig(t *testing.T, effect gate.Effect, reason release.Reason, who actor.Actor) *approvalRig {
	t.Helper()
	org := identity.NewOrganizationID()
	e := project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}
	p := project.Project{ID: project.NewID(), Organization: org, Name: "Pagos", Slug: "pagos", Tier: project.TierProduction}
	dep := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: p.ID, Environment: e.ID,
		Status: release.StatusBuilding, Step: release.StepBuild, Reason: reason, RequestedBy: requester}
	rig := &approvalRig{repo: &approvalRepo{dep: dep}, rules: &rules{effect: effect}, approver: &approvals{},
		events: &eventtest.Recorder{}, builder: &buildCancels{}, actor: who}
	rig.svc = &Service{repo: rig.repo, projects: &usesProjects{p: p, e: e}, tx: directTx{}, policy: rig.rules, approvals: rig.approver,
		events: rig.events, builder: rig.builder, now: time.Now}
	input, err := json.Marshal(release.DeployInput{Deployment: dep.ID, Actor: who})
	if err != nil {
		t.Fatal(err)
	}
	rig.st = &workflow.State{Run: workflow.Run{ID: workflow.NewRunID(), Organization: org, Input: input, Outputs: map[string]json.RawMessage{}}}
	return rig
}

func (r *approvalRig) decided(t *testing.T, status string) {
	t.Helper()
	raw, err := json.Marshal(gate.Decision{Request: "req-1", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	r.st.Run.Outputs[release.StepAuthorize] = raw
}

var dev = actor.Actor{User: requester, Permissions: []string{"odyssey:projects:write"}}

func TestAuthorizeAllowedGoesOnAndWithdrawsAStaleRequest(t *testing.T) {
	r := newApprovalRig(t, gate.Allow, release.ReasonDeploy, dev)
	out, err := r.svc.authorizeStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed {
		t.Fatalf("out %v, err %v", out, err)
	}
	// The rules flipped to allow after a request for this run was opened (a
	// retry of the step): that request is withdrawn, so IT is not asked.
	if !slices.Equal(r.approver.cancelled, []string{r.st.Run.ID.String()}) {
		t.Fatalf("the orphan request of the run was not cancelled: %v", r.approver.cancelled)
	}
	if len(r.approver.held) != 0 {
		t.Fatalf("an allowed deployment held a request: %v", r.approver.held)
	}
	if got := r.rules.actors[0]; got.User != requester || !got.Can("odyssey:projects:write") {
		t.Fatalf("judged as %+v, want the requester's snapshot", got)
	}
}

func TestAuthorizeDeniedFailsBeforeBuildingAndWithdrawsTheRequest(t *testing.T) {
	r := newApprovalRig(t, gate.Deny, release.ReasonRollback, dev)
	_, err := r.svc.authorizeStep(context.Background(), r.st)
	var coded *errx.Error
	if c, ok := workflow.AsControl(err); !ok || c.Kind != workflow.ControlFail || !errors.As(err, &coded) || coded.Code != release.CodePolicyDenied {
		t.Fatalf("want a permanent POLICY_DENIED failure, got %v", err)
	}
	if len(r.approver.cancelled) != 1 {
		t.Fatalf("a denied deployment must not leave its request pending: %v", r.approver.cancelled)
	}
}

func TestAuthorizeHoldsAndParks(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonRollback, dev)
	_, err := r.svc.authorizeStep(context.Background(), r.st)
	if c, ok := workflow.AsControl(err); !ok || c.Kind != workflow.ControlWait || c.Signal != gate.SignalApproval || c.Delay != approvalWait {
		t.Fatalf("want WaitFor(approval), got %v", err)
	}
	if len(r.approver.held) != 1 {
		t.Fatalf("held %d requests", len(r.approver.held))
	}
	h := r.approver.held[0]
	if h.Action != gate.ActionRollback || h.Run != r.st.Run.ID.String() || h.RunStep != release.StepAuthorize || h.Summary == "" {
		t.Fatalf("the request %+v", h)
	}
	if h.Payload.(release.DeploymentApproval).Deployment != r.repo.dep.ID {
		t.Fatalf("payload %+v", h.Payload)
	}
	if !slices.Equal(r.repo.progress, []release.Status{release.StatusWaitingApproval}) || r.repo.dep.Step != release.StepBuild {
		t.Fatalf("progress %v at step %q: the deployment must wait where it was", r.repo.progress, r.repo.dep.Step)
	}
	if len(r.approver.cancelled) != 0 {
		t.Fatalf("a held deployment cancelled its own request: %v", r.approver.cancelled)
	}
}

func TestAuthorizeHoldsAPushAsItsSystemActor(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonDeploy, actor.Actor{System: "github:ana"})
	if _, err := r.svc.authorizeStep(context.Background(), r.st); kind(err) != workflow.ControlWait {
		t.Fatalf("a push is judged as the system actor and held: %v", err)
	}
	if got := r.rules.actors[0]; got.System != "github:ana" || !got.User.IsZero() {
		t.Fatalf("judged as %+v", got)
	}
}

// A run that started before 3b has no actor in its input (it was judged
// deny-only when asked): judging it now as nobody would hold or refuse what
// was accepted, so it is let through without asking the rules.
func TestAuthorizeLetsAPre3bRunThrough(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonDeploy, actor.Actor{})
	out, err := r.svc.authorizeStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed {
		t.Fatalf("out %v, err %v", out, err)
	}
	if r.rules.asked != 0 || len(r.approver.held) != 0 || len(r.repo.progress) != 0 {
		t.Fatalf("a run with no actor was judged (%d) or held (%d)", r.rules.asked, len(r.approver.held))
	}
}

// Only a run from before 3b may have no actor: a new deploy or rollback with
// nobody on the context is refused before it starts, so it can never use
// that pass to skip IT's rules.
func TestBeginRefusesAJudgedDeploymentWithNoActor(t *testing.T) {
	r := newApprovalRig(t, gate.Allow, release.ReasonDeploy, actor.Actor{})
	p, e := r.svc.projects.(*usesProjects).p, r.svc.projects.(*usesProjects).e
	for _, reason := range []release.Reason{release.ReasonDeploy, release.ReasonRollback} {
		_, _, err := r.svc.begin(context.Background(), release.EnvironmentState{}, p, e, release.Deployment{ID: release.NewDeploymentID(), Reason: reason})
		if !errx.IsType(err, errx.TypeInternal) {
			t.Fatalf("%s with no actor: want an internal refusal, got %v", reason, err)
		}
	}
}

func TestAuthorizeDoesNotJudgeThePlatformsOwnDeployments(t *testing.T) {
	for _, reason := range []release.Reason{release.ReasonConfig, release.ReasonAutoRollback} {
		r := newApprovalRig(t, gate.Deny, reason, actor.Actor{})
		out, err := r.svc.authorizeStep(context.Background(), r.st)
		if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed || r.rules.asked != 0 {
			t.Fatalf("%s: out %v, err %v, asked %d", reason, out, err, r.rules.asked)
		}
	}
}

func TestApprovalStepContinuesWhenNothingWasAsked(t *testing.T) {
	r := newApprovalRig(t, gate.Allow, release.ReasonDeploy, dev)
	if _, err := r.svc.approvalStep(context.Background(), r.st); err != nil { // no authorize output: a run from before 3b
		t.Fatalf("a run without the authorize output: %v", err)
	}
	r.decided(t, gate.DecisionAllowed)
	if _, err := r.svc.approvalStep(context.Background(), r.st); err != nil || len(r.approver.consumed) != 0 {
		t.Fatalf("allowed: %v, consumed %v", err, r.approver.consumed)
	}
}

func TestApprovalStepResumesAnApprovedDeployment(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonDeploy, dev)
	buildID := build.NewID()
	r.repo.dep.Status, r.repo.dep.Build = release.StatusWaitingApproval, &buildID
	r.decided(t, gate.DecisionApproved)
	if _, err := r.svc.approvalStep(context.Background(), r.st); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.approver.consumed, []string{"req-1"}) {
		t.Fatalf("the approval was not used up: %v", r.approver.consumed)
	}
	if r.repo.dep.Status != release.StatusBuilding || r.repo.dep.Step != release.StepBuild {
		t.Fatalf("back to %s/%s, want building at build", r.repo.dep.Status, r.repo.dep.Step)
	}
}

func TestApprovalStepResumesAnImageDeploymentAsPending(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonRollback, dev)
	rel := release.NewID()
	r.repo.dep.Status, r.repo.dep.Step, r.repo.dep.Release = release.StatusWaitingApproval, release.StepApply, &rel
	r.decided(t, gate.DecisionApproved)
	if _, err := r.svc.approvalStep(context.Background(), r.st); err != nil {
		t.Fatal(err)
	}
	if r.repo.dep.Status != release.StatusPending {
		t.Fatalf("status %s", r.repo.dep.Status)
	}
}

func TestApprovalStepCancelsWhatIsNotApproved(t *testing.T) {
	for status, want := range map[string]string{
		gate.DecisionRejected:  release.CancelApprovalRejected,
		gate.DecisionExpired:   release.CancelApprovalExpired,
		gate.DecisionCancelled: release.CancelApprovalCanceled,
	} {
		r := newApprovalRig(t, gate.Approval, release.ReasonDeploy, dev)
		buildID := build.NewID()
		r.repo.dep.Status, r.repo.dep.Build = release.StatusWaitingApproval, &buildID
		r.decided(t, status)
		_, err := r.svc.approvalStep(context.Background(), r.st)
		if !errors.Is(err, errSuperseded) { // the run ends quietly
			t.Fatalf("%s: %v", status, err)
		}
		if r.repo.dep.Status != release.StatusCancelled || !r.repo.settled {
			t.Fatalf("%s: deployment %s, settled %v", status, r.repo.dep.Status, r.repo.settled)
		}
		if len(r.builder.cancelled) != 1 || r.builder.cancelled[0] != buildID {
			t.Fatalf("%s: the build was not dropped: %v", status, r.builder.cancelled)
		}
		if len(r.approver.consumed) != 0 {
			t.Fatalf("%s: an approval nobody gave was consumed", status)
		}
		events := r.events.Events()
		if len(events) != 1 || events[0].Type != release.EventDeploymentCancelled {
			t.Fatalf("%s: events %v", status, r.events.Types())
		}
		p, err := event.Decode[release.DeploymentCancelled](events[0])
		if err != nil || p.Reason != want {
			t.Fatalf("%s: reason %q (%v), want %q", status, p.Reason, err, want)
		}
	}
}

func TestCancelHeldOnAFinishedDeploymentEndsTheRunWithoutEffect(t *testing.T) {
	r := newApprovalRig(t, gate.Approval, release.ReasonDeploy, dev)
	r.repo.dep.Status = release.StatusCancelled // a newer deployment already replaced it
	err := r.svc.cancelHeld(context.Background(), r.st, gate.Decision{Request: "req-1", Status: gate.DecisionRejected})
	if !errors.Is(err, errSuperseded) || len(r.events.Events()) != 0 {
		t.Fatalf("err %v, events %v", err, r.events.Types())
	}
}
