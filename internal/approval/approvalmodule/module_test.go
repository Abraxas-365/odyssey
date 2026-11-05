package approvalmodule

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/approval/approvalsvc"
	"github.com/Abraxas-365/odyssey/internal/approval/approvaltest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// executor stands for a governed domain: it runs the action and, when
// redeems is set, uses the approval up like the real services do.
type executor struct {
	svc     *approvalsvc.Service
	redeems bool
	err     error
	ran     int
}

func (x *executor) Handles(approval.Request) bool { return true }

func (x *executor) Execute(ctx context.Context, r approval.Request) error {
	x.ran++
	if x.err != nil {
		return x.err
	}
	if x.redeems {
		return x.svc.Consume(ctx, r.Organization, r.ID)
	}
	return nil
}

type rig struct {
	m    Module
	repo *approvaltest.Repo
	rec  *eventtest.Recorder
	req  approval.Request
	ev   event.Event
}

var decider = approval.Viewer{User: identity.MustParseUserID(uuid.NewString()), Permissions: []string{"odyssey:approvals:decide"}}

// approved opens a direct request and has it approved; ev is the event the
// reactor would be handed.
func approved(t *testing.T, x *executor) *rig {
	t.Helper()
	repo, rec := approvaltest.NewRepo(), &eventtest.Recorder{}
	svc := approvalsvc.New(repo, txn.Passthrough{}, rec)
	x.svc = svc
	org := identity.NewOrganizationID()
	r, err := svc.Open(context.Background(), org, approval.Open{Action: "set_access", Project: project.NewID(), Environment: project.NewEnvironmentID(),
		Summary: "Hacer pública «app»", Payload: map[string]string{"mode": "public"}, Reason: "r",
		Requester: approval.Requester{User: identity.MustParseUserID(uuid.NewString())}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(context.Background(), org, r.ID, approval.Decide{By: decider}); err != nil {
		t.Fatal(err)
	}
	var ev event.Event
	for _, e := range rec.Events() {
		if e.Type == approval.EventApproved {
			ev = e
		}
	}
	return &rig{m: Module{Commands: svc, Queries: svc, executors: []approval.Executor{x}}, repo: repo, rec: rec, req: r, ev: ev}
}

func (r *rig) stored(t *testing.T) approval.Request {
	t.Helper()
	got, err := r.m.Queries.Get(context.Background(), r.req.Organization, r.req.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func count(types []string, want string) int {
	n := 0
	for _, t := range types {
		if t == want {
			n++
		}
	}
	return n
}

// An approved request the action never redeemed (a rule changed meanwhile and
// now allows it, or the action was a no-op) is used up at the end of the run.
func TestExecuteConsumesWhatTheActionDidNotRedeem(t *testing.T) {
	x := &executor{}
	r := approved(t, x)
	if err := r.m.execute(context.Background(), r.ev); err != nil {
		t.Fatal(err)
	}
	got := r.stored(t)
	if x.ran != 1 || got.ConsumedAt == nil || got.ExecutionError != "" {
		t.Fatalf("ran %d, consumed %v, error %q", x.ran, got.ConsumedAt, got.ExecutionError)
	}
	if n := count(r.rec.Types(), approval.EventConsumed); n != 1 {
		t.Fatalf("%d consumed events: %v", n, r.rec.Types())
	}
}

// When the action redeemed it already, consuming again does nothing (one
// event, not two).
func TestExecuteConsumeIsIdempotent(t *testing.T) {
	x := &executor{redeems: true}
	r := approved(t, x)
	if err := r.m.execute(context.Background(), r.ev); err != nil {
		t.Fatal(err)
	}
	if n := count(r.rec.Types(), approval.EventConsumed); n != 1 || r.stored(t).ConsumedAt == nil {
		t.Fatalf("%d consumed events: %v", n, r.rec.Types())
	}
	// delivered twice: the second delivery does not run it again
	if err := r.m.execute(context.Background(), r.ev); err != nil || x.ran != 1 {
		t.Fatalf("second delivery: %v, ran %d", err, x.ran)
	}
}

// A failed action is recorded, not consumed.
func TestExecuteFailureIsNotConsumed(t *testing.T) {
	x := &executor{err: errx.Business("La app ya no existe.")}
	r := approved(t, x)
	if err := r.m.execute(context.Background(), r.ev); err != nil {
		t.Fatal(err)
	}
	got := r.stored(t)
	if got.ConsumedAt != nil || got.ExecutionError != "La app ya no existe." {
		t.Fatalf("consumed %v, error %q", got.ConsumedAt, got.ExecutionError)
	}
	if !slices.Contains(r.rec.Types(), approval.EventExecutionFailed) || count(r.rec.Types(), approval.EventConsumed) != 0 {
		t.Fatalf("events %v", r.rec.Types())
	}
}
