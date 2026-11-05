package approvalsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/approval/approvalsvc"
	"github.com/Abraxas-365/odyssey/internal/approval/approvaltest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var (
	org    = identity.NewOrganizationID()
	alice  = newUser() // asks
	it     = newUser() // decides
	pid    = project.NewID()
	eid    = project.NewEnvironmentID()
	decide = []string{"odyssey:approvals:decide"}
)

func setup() (*approvalsvc.Service, *approvaltest.Repo, *eventtest.Recorder) {
	repo, rec := approvaltest.NewRepo(), &eventtest.Recorder{}
	return approvalsvc.New(repo, txn.Passthrough{}, rec), repo, rec
}

func open(t *testing.T, s *approvalsvc.Service, payload string) approval.Request {
	t.Helper()
	r, err := s.Open(context.Background(), org, approval.Open{Action: "set_access", Project: pid, Environment: eid,
		Summary: "Hacer pública «app»", Payload: map[string]string{"mode": payload}, Reason: "r",
		Requester: approval.Requester{User: alice, Permissions: []string{"odyssey:projects:write"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func itViewer() approval.Viewer    { return approval.Viewer{User: it, Permissions: decide} }
func aliceViewer() approval.Viewer { return approval.Viewer{User: alice, Permissions: decide} }

func code(err error) string {
	if e, ok := err.(*errx.Error); ok {
		return e.Code
	}
	return ""
}

func TestOpenIsIdempotentWhilePendingAndPublishesOnce(t *testing.T) {
	s, repo, rec := setup()
	a, b := open(t, s, "public"), open(t, s, "public")
	if a.ID != b.ID || len(repo.Rows) != 1 || len(rec.Types()) != 1 || rec.Types()[0] != approval.EventRequested {
		t.Fatalf("%v %v %v", a.ID == b.ID, len(repo.Rows), rec.Types())
	}
	if a.Approvers[0] != "odyssey:approvals:decide" || !a.ExpiresAt.After(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("defaults: %+v", a)
	}
	if c := open(t, s, "team"); c.ID == a.ID {
		t.Fatal("another payload is another request")
	}
	// a workflow step: idempotent whatever became of it
	in := approval.Open{Action: "deploy", Project: pid, Environment: eid, Summary: "Publicar", Run: "r1", RunStep: "authorize",
		Requester: approval.Requester{User: alice}}
	first, _ := s.Open(context.Background(), org, in)
	if _, err := s.Reject(context.Background(), org, first.ID, approval.Decide{By: itViewer()}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Open(context.Background(), org, in)
	if err != nil || again.ID != first.ID || again.Status != approval.StatusRejected {
		t.Fatalf("step reopened: %+v %v", again, err)
	}
	if _, err := s.Open(context.Background(), org, approval.Open{Action: "x"}); err == nil {
		t.Fatal("an open without a place")
	}
}

func TestApproveRejectRules(t *testing.T) {
	ctx := context.Background()
	s, _, rec := setup()
	r := open(t, s, "public")
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: aliceViewer()}); code(err) != approval.CodeSelf || !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("self approve: %v", err)
	}
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: approval.Viewer{User: it, Permissions: []string{"odyssey:projects:write"}}}); err == nil {
		t.Fatal("without the permission")
	}
	stranger := approval.Viewer{User: newUser()}
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: stranger}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("a stranger must not even see it: %v", err)
	}
	if _, err := s.Approve(ctx, identity.NewOrganizationID(), r.ID, approval.Decide{By: itViewer()}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization: %v", err)
	}
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer(), Note: string(make([]rune, 501))}); err == nil {
		t.Fatal("a note over the limit")
	}
	got, err := s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer(), Note: "ok"})
	if err != nil || got.Status != approval.StatusApproved || got.DecidedBy == nil || *got.DecidedBy != it {
		t.Fatalf("approve: %+v %v", got, err)
	}
	if _, err := s.Reject(ctx, org, r.ID, approval.Decide{By: itViewer()}); code(err) != approval.CodeDecided || !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("decided twice: %v", err)
	}
	if types := rec.Types(); len(types) != 2 || types[1] != approval.EventApproved {
		t.Fatalf("events %v", types)
	}
	r2 := open(t, s, "team")
	if got, err := s.Reject(ctx, org, r2.ID, approval.Decide{By: itViewer(), Note: "no"}); err != nil || got.Status != approval.StatusRejected {
		t.Fatalf("reject: %+v %v", got, err)
	}
}

func TestCancelIsTheRequestersOnly(t *testing.T) {
	ctx := context.Background()
	s, _, rec := setup()
	r := open(t, s, "public")
	if _, err := s.Cancel(ctx, org, r.ID, approval.Cancel{By: itViewer()}); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("an approver cancelled someone else's: %v", err)
	}
	got, err := s.Cancel(ctx, org, r.ID, approval.Cancel{By: aliceViewer()})
	if err != nil || got.Status != approval.StatusCancelled {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	if _, err := s.Cancel(ctx, org, r.ID, approval.Cancel{By: aliceViewer()}); code(err) != approval.CodeDecided {
		t.Fatalf("cancel twice: %v", err)
	}
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer()}); code(err) != approval.CodeDecided {
		t.Fatalf("approve a cancelled one: %v", err)
	}
	if types := rec.Types(); types[len(types)-1] != approval.EventCancelled {
		t.Fatalf("events %v", types)
	}
}

func TestCancelRun(t *testing.T) {
	ctx := context.Background()
	s, _, rec := setup()
	if err := s.CancelRun(ctx, org, "none"); err != nil {
		t.Fatalf("nothing to cancel: %v", err)
	}
	r, _ := s.Open(ctx, org, approval.Open{Action: "deploy", Project: pid, Environment: eid, Summary: "Publicar", Run: "r1", RunStep: "authorize"})
	if err := s.CancelRun(ctx, org, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, org, r.ID); got.Status != approval.StatusCancelled {
		t.Fatalf("status %s", got.Status)
	}
	n := len(rec.Types())
	if err := s.CancelRun(ctx, org, "r1"); err != nil || len(rec.Types()) != n {
		t.Fatalf("second cancel must be a no-op: %v %v", err, rec.Types())
	}
}

// An approve that wins the race against the run's withdraw leaves an approval
// the run will not use: CancelRun uses it up instead of leaving it approved.
func TestCancelRunUsesUpAnApprovalThatWonTheRace(t *testing.T) {
	ctx := context.Background()
	s, _, rec := setup()
	r, _ := s.Open(ctx, org, approval.Open{Action: "deploy", Project: pid, Environment: eid, Summary: "Publicar", Run: "r2", RunStep: "authorize"})
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(ctx, org, "r2"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, org, r.ID)
	if got.Status != approval.StatusApproved || got.ConsumedAt == nil {
		t.Fatalf("want approved and used up, got %s consumed=%v", got.Status, got.ConsumedAt)
	}
	n := len(rec.Types())
	if err := s.CancelRun(ctx, org, "r2"); err != nil || len(rec.Types()) != n {
		t.Fatalf("again must be a no-op: %v %v", err, rec.Types())
	}
}

func TestExpiredRequestsCannotBeDecidedAndExpireInBatches(t *testing.T) {
	ctx := context.Background()
	s, repo, rec := setup()
	r := open(t, s, "public")
	for i := range repo.Rows {
		repo.Rows[i].ExpiresAt = time.Now().Add(-time.Minute)
	}
	if _, err := s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer()}); code(err) != approval.CodeDecided {
		t.Fatalf("approve a stale one: %v", err)
	}
	v, _ := s.Find(ctx, org, r.ID, itViewer())
	if v.CanDecide {
		t.Fatal("a stale request is not decidable")
	}
	for i := 0; i < 120; i++ {
		open(t, s, string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	for i := range repo.Rows {
		repo.Rows[i].ExpiresAt = time.Now().Add(-time.Minute)
	}
	n, err := s.Expire(ctx)
	if err != nil || n != 121 {
		t.Fatalf("expired %d %v", n, err)
	}
	for _, x := range repo.Rows {
		if x.Status != approval.StatusExpired {
			t.Fatalf("left %s", x.Status)
		}
	}
	if types := rec.Types(); types[len(types)-1] != approval.EventExpired {
		t.Fatalf("events %v", types[len(types)-1])
	}
	if n, err := s.Expire(ctx); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}

func TestRedeemNeedsTheSameActorActionPlaceAndPayloadAndWorksOnce(t *testing.T) {
	s, _, rec := setup()
	r := open(t, s, "public")
	grant := approval.Grant{Action: "set_access", Project: pid, Environment: eid, Payload: map[string]string{"mode": "public"}}
	asAlice := actor.With(context.Background(), actor.Actor{User: alice})
	if err := s.Redeem(asAlice, org, r.ID, grant); code(err) != approval.CodeNotUsable {
		t.Fatalf("redeem a pending request: %v", err)
	}
	if _, err := s.Approve(context.Background(), org, r.ID, approval.Decide{By: itViewer()}); err != nil {
		t.Fatal(err)
	}
	bad := map[string]approval.Grant{
		"action":  {Action: "enable_ai", Project: pid, Environment: eid, Payload: grant.Payload},
		"project": {Action: "set_access", Project: project.NewID(), Environment: eid, Payload: grant.Payload},
		"env":     {Action: "set_access", Project: pid, Environment: project.NewEnvironmentID(), Payload: grant.Payload},
		"payload": {Action: "set_access", Project: pid, Environment: eid, Payload: map[string]string{"mode": "team"}},
	}
	for name, g := range bad {
		if err := s.Redeem(asAlice, org, r.ID, g); code(err) != approval.CodeNotUsable {
			t.Fatalf("%s: %v", name, err)
		}
	}
	asIT := actor.With(context.Background(), actor.Actor{User: it})
	if err := s.Redeem(asIT, org, r.ID, grant); code(err) != approval.CodeNotUsable {
		t.Fatalf("another actor redeemed it: %v", err)
	}
	if err := s.Redeem(asAlice, identity.NewOrganizationID(), r.ID, grant); code(err) != approval.CodeNotUsable {
		t.Fatalf("another organization redeemed it: %v", err)
	}
	if err := s.Redeem(asAlice, org, r.ID, grant); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if err := s.Redeem(asAlice, org, r.ID, grant); code(err) != approval.CodeNotUsable {
		t.Fatalf("redeemed twice: %v", err)
	}
	if types := rec.Types(); types[len(types)-1] != approval.EventConsumed {
		t.Fatalf("events %v", types)
	}
}

func TestConsumeIsIdempotentAndNeedsApproval(t *testing.T) {
	ctx := context.Background()
	s, _, _ := setup()
	r := open(t, s, "public")
	if err := s.Consume(ctx, org, r.ID); code(err) != approval.CodeNotUsable {
		t.Fatalf("consume pending: %v", err)
	}
	_, _ = s.Approve(ctx, org, r.ID, approval.Decide{By: itViewer()})
	if err := s.Consume(ctx, org, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Consume(ctx, org, r.ID); err != nil {
		t.Fatalf("second consume: %v", err)
	}
}

func TestFailRecordsAndPublishes(t *testing.T) {
	ctx := context.Background()
	s, repo, rec := setup()
	r := open(t, s, "public")
	if err := s.Fail(ctx, org, r.ID, "no se pudo"); err != nil || repo.Rows[0].ExecutionError != "no se pudo" {
		t.Fatalf("%v %q", err, repo.Rows[0].ExecutionError)
	}
	if types := rec.Types(); types[len(types)-1] != approval.EventExecutionFailed {
		t.Fatalf("events %v", types)
	}
	if err := s.Fail(ctx, identity.NewOrganizationID(), r.ID, "x"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization: %v", err)
	}
}

func TestQueriesAreScopedToWhatTheViewerSees(t *testing.T) {
	ctx := context.Background()
	s, _, _ := setup()
	r := open(t, s, "public")
	if _, err := s.Find(ctx, org, r.ID, approval.Viewer{User: newUser()}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := s.Find(ctx, identity.NewOrganizationID(), r.ID, itViewer()); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other org: %v", err)
	}
	v, err := s.Find(ctx, org, r.ID, itViewer())
	if err != nil || !v.CanDecide || v.Mine {
		t.Fatalf("approver view: %+v %v", v, err)
	}
	v, _ = s.Find(ctx, org, r.ID, aliceViewer())
	if v.CanDecide || !v.Mine {
		t.Fatalf("requester view: %+v", v)
	}
	if _, err := s.List(ctx, org, itViewer(), approval.Filter{Mine: true, Decidable: true}, query.Pagination{}); err == nil {
		t.Fatal("mine and decidable together")
	}
	if _, err := s.List(ctx, org, itViewer(), approval.Filter{Status: []approval.Status{"nope"}}, query.Pagination{}); err == nil {
		t.Fatal("unknown status")
	}
	got, err := s.List(ctx, org, itViewer(), approval.Filter{}, query.Pagination{})
	if err != nil || len(got.Items) != 1 || !got.Items[0].CanDecide {
		t.Fatalf("list: %+v %v", got, err)
	}
}

func newUser() identity.UserID { return identity.MustParseUserID(uuid.NewString()) }
