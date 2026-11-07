package approvalpg_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalpg"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var (
	alice = identity.MustParseUserID("00000000-0000-4000-8000-0000000000a1")
	bob   = identity.MustParseUserID("00000000-0000-4000-8000-0000000000b2")
)

func setup(t *testing.T) (*approvalpg.Repository, txn.Runner, context.Context) {
	db := pgtest.DB(t, migrate.Set{Owner: approvalpg.Schema, Files: approvalpg.Migrations()})
	return approvalpg.New(db), txn.Postgres{DB: db}, context.Background()
}

func request(org identity.OrganizationID, key string) approval.Request {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return approval.Request{ID: approval.NewID(), Organization: org, Action: "set_access", Project: project.NewID(),
		Environment: project.NewEnvironmentID(), Summary: "Poner la app pública", Payload: json.RawMessage(`{"mode":"public"}`),
		RequestedBy: alice, RequesterPermissions: []string{"odyssey:projects:write"}, Reason: "r",
		Rules: []approval.Rule{{ID: "1", Name: "regla"}}, Approvers: []string{"odyssey:approvals:decide"},
		Status: approval.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now, Key: key}
}

func insert(t *testing.T, repo *approvalpg.Repository, ctx context.Context, r approval.Request) {
	t.Helper()
	if ok, err := repo.Insert(ctx, r); err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
}

func TestInsertFindAndTenantIsolation(t *testing.T) {
	repo, _, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := request(org, "k1")
	insert(t, repo, ctx, r)
	got, err := repo.Find(ctx, org, r.ID)
	if err != nil || got.Summary != r.Summary || got.RequestedBy != alice || len(got.Rules) != 1 ||
		got.Approvers[0] != "odyssey:approvals:decide" || got.RequesterPermissions[0] != "odyssey:projects:write" ||
		!approval.SamePayload(got.Payload, r.Payload) || got.Status != approval.StatusPending {
		t.Fatalf("find: %+v %v", got, err)
	}
	if _, err := repo.Find(ctx, other, r.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other org reads it: %v", err)
	}
	if _, err := repo.FindOpen(ctx, other, "k1"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other org finds the key: %v", err)
	}
	// the same key in another organization is another request
	insert(t, repo, ctx, request(other, "k1"))
}

// A request about the whole app (set_services, MS-3c) has no environment:
// stored as NULL, read back as the zero ID, only by its organization.
func TestAppWideRequestHasNoEnvironment(t *testing.T) {
	repo, _, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := request(org, "services")
	r.Action, r.Environment = "set_services", project.EnvironmentID{}
	insert(t, repo, ctx, r)
	got, err := repo.FindOpen(ctx, org, "services")
	if err != nil || got.ID != r.ID || !got.Environment.IsZero() || got.Action != "set_services" {
		t.Fatalf("app-wide request %+v %v", got, err)
	}
	page, err := repo.List(ctx, org, approval.Viewer{User: alice}, approval.Filter{}, query.Pagination{Limit: 10})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Environment.IsZero() {
		t.Fatalf("listed %+v %v", page.Items, err)
	}
	if _, err := repo.Find(ctx, other, r.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other org reads it: %v", err)
	}
}

func TestInsertIsIdempotentWhileOpen(t *testing.T) {
	repo, _, ctx := setup(t)
	org := identity.NewOrganizationID()
	first := request(org, "same")
	insert(t, repo, ctx, first)
	if ok, err := repo.Insert(ctx, request(org, "same")); err != nil || ok {
		t.Fatalf("second pending with the key: %v %v", ok, err)
	}
	held, err := repo.FindOpen(ctx, org, "same")
	if err != nil || held.ID != first.ID {
		t.Fatalf("open: %+v %v", held, err)
	}
	// decided: the key is free for a direct action ...
	if ok, err := repo.Decide(ctx, org, first.ID, approval.StatusRejected, &bob, "no", time.Now()); err != nil || !ok {
		t.Fatalf("decide: %v %v", ok, err)
	}
	if _, err := repo.FindOpen(ctx, org, "same"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("decided request still open: %v", err)
	}
	insert(t, repo, ctx, request(org, "same"))
	// ... but a workflow step keeps its request whatever became of it
	step := request(org, "run:r1/authorize")
	step.Run, step.RunStep = "r1", "authorize"
	insert(t, repo, ctx, step)
	if _, err := repo.Decide(ctx, org, step.ID, approval.StatusApproved, &bob, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	again := request(org, "run:r1/authorize")
	again.Run, again.RunStep = "r1", "authorize"
	if ok, err := repo.Insert(ctx, again); err != nil || ok {
		t.Fatalf("run step inserted twice: %v %v", ok, err)
	}
	if got, err := repo.FindByRun(ctx, org, "r1", "authorize"); err != nil || got.ID != step.ID || got.Status != approval.StatusApproved {
		t.Fatalf("by run: %+v %v", got, err)
	}
	// a run holds one request per step: another step's request is not this one
	if _, err := repo.FindByRun(ctx, org, "r1", "authorize_scan"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("the authorize request answers for authorize_scan: %v", err)
	}
	scan := request(org, "run:r1/authorize_scan")
	scan.Run, scan.RunStep = "r1", "authorize_scan"
	insert(t, repo, ctx, scan)
	if got, err := repo.FindByRun(ctx, org, "r1", "authorize_scan"); err != nil || got.ID != scan.ID || got.Status != approval.StatusPending {
		t.Fatalf("by run and step: %+v %v", got, err)
	}
	if got, err := repo.FindByRun(ctx, org, "r1", "authorize"); err != nil || got.ID != step.ID {
		t.Fatalf("the scan request replaced the authorize one: %+v %v", got, err)
	}
	if _, err := repo.FindByRun(ctx, identity.NewOrganizationID(), "r1", "authorize"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other org reads the run's request: %v", err)
	}
}

func TestDecideConsumeAndExecutionError(t *testing.T) {
	repo, db, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := request(org, "d")
	insert(t, repo, ctx, r)
	if ok, err := repo.Consume(ctx, org, r.ID, time.Now()); err != nil || ok {
		t.Fatalf("consumed a pending request: %v %v", ok, err)
	}
	if ok, _ := repo.Decide(ctx, other, r.ID, approval.StatusApproved, &bob, "", time.Now()); ok {
		t.Fatal("another organization decided it")
	}
	if ok, err := repo.Decide(ctx, org, r.ID, approval.StatusApproved, &bob, "adelante", time.Now()); err != nil || !ok {
		t.Fatalf("decide: %v %v", ok, err)
	}
	if ok, _ := repo.Decide(ctx, org, r.ID, approval.StatusRejected, &bob, "", time.Now()); ok {
		t.Fatal("decided twice")
	}
	got, _ := repo.Find(ctx, org, r.ID)
	if got.Status != approval.StatusApproved || got.DecidedBy == nil || *got.DecidedBy != bob || got.DecisionNote != "adelante" || got.DecidedAt == nil {
		t.Fatalf("decided: %+v", got)
	}
	if ok, _ := repo.Consume(ctx, other, r.ID, time.Now()); ok {
		t.Fatal("another organization consumed it")
	}
	if ok, err := repo.Consume(ctx, org, r.ID, time.Now()); err != nil || !ok {
		t.Fatalf("consume: %v %v", ok, err)
	}
	if ok, _ := repo.Consume(ctx, org, r.ID, time.Now()); ok {
		t.Fatal("consumed twice")
	}
	if err := repo.SetExecutionError(ctx, other, r.ID, "x"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization set the error: %v", err)
	}
	if err := repo.SetExecutionError(ctx, org, r.ID, "no se pudo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Find(ctx, org, r.ID); got.ExecutionError != "no se pudo" || got.ConsumedAt == nil {
		t.Fatalf("after: %+v", got)
	}
	// FindForUpdate needs a transaction and is tenant scoped
	if _, err := repo.FindForUpdate(ctx, org, r.ID); err == nil {
		t.Fatal("FindForUpdate outside a transaction")
	}
	err := db.Do(ctx, func(ctx context.Context) error {
		if _, err := repo.FindForUpdate(ctx, other, r.ID); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("other org locks it: %v", err)
		}
		got, err := repo.FindForUpdate(ctx, org, r.ID)
		if err != nil || got.ID != r.ID {
			t.Fatalf("lock: %+v %v", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListScopesWhatTheViewerSeesAndTenantIsolation(t *testing.T) {
	repo, _, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	mine := request(org, "mine") // alice's, decidable by approvers
	theirs := request(org, "theirs")
	theirs.RequestedBy = bob
	theirs.Approvers = []string{"odyssey:approvals:decide"}
	private := request(org, "private") // bob's, for a permission alice lacks
	private.RequestedBy = bob
	private.Approvers = []string{"odyssey:other"}
	for _, r := range []approval.Request{mine, theirs, private, request(other, "elsewhere")} {
		insert(t, repo, ctx, r)
	}
	page := query.Pagination{Limit: 50}
	ids := func(v approval.Viewer, f approval.Filter) map[approval.ID]bool {
		got, err := repo.List(ctx, org, v, f, page)
		if err != nil {
			t.Fatal(err)
		}
		out := map[approval.ID]bool{}
		for _, r := range got.Items {
			out[r.ID] = true
		}
		return out
	}
	approver := approval.Viewer{User: alice, Permissions: []string{"odyssey:approvals:decide"}}
	if got := ids(approver, approval.Filter{}); len(got) != 2 || !got[mine.ID] || !got[theirs.ID] {
		t.Fatalf("approver sees %v", got)
	}
	if got := ids(approver, approval.Filter{Mine: true}); len(got) != 1 || !got[mine.ID] {
		t.Fatalf("mine %v", got)
	}
	if got := ids(approver, approval.Filter{Decidable: true}); len(got) != 1 || !got[theirs.ID] {
		t.Fatalf("decidable (not their own) %v", got)
	}
	plain := approval.Viewer{User: alice}
	if got := ids(plain, approval.Filter{}); len(got) != 1 || !got[mine.ID] {
		t.Fatalf("requester without permissions sees %v", got)
	}
	if got := ids(plain, approval.Filter{Decidable: true}); len(got) != 0 {
		t.Fatalf("plain user can decide %v", got)
	}
	if got := ids(approval.Viewer{User: bob, Permissions: []string{"odyssey:other"}}, approval.Filter{}); len(got) != 2 || !got[theirs.ID] || !got[private.ID] {
		t.Fatalf("bob sees %v", got)
	}
	// status filter and the total
	if _, err := repo.Decide(ctx, org, mine.ID, approval.StatusApproved, &bob, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := ids(approver, approval.Filter{Status: []approval.Status{approval.StatusPending}}); len(got) != 1 || !got[theirs.ID] {
		t.Fatalf("pending %v", got)
	}
	paged, err := repo.List(ctx, org, approver, approval.Filter{}, query.Pagination{Limit: 1})
	if err != nil || len(paged.Items) != 1 || paged.Page.Total != 2 {
		t.Fatalf("page: %+v %v", paged, err)
	}
	// other organization's viewer sees only its own
	if got, _ := repo.List(ctx, other, approver, approval.Filter{}, page); len(got.Items) != 1 {
		t.Fatalf("other org sees %d", len(got.Items))
	}
}

func TestListExpiredTakesOnlyPendingPastTheirTime(t *testing.T) {
	repo, db, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	late, fresh, decided, foreign := request(org, "late"), request(org, "fresh"), request(org, "decided"), request(other, "foreign")
	now := time.Now().UTC().Truncate(time.Microsecond)
	late.ExpiresAt, decided.ExpiresAt, foreign.ExpiresAt = now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Second)
	for _, r := range []approval.Request{late, fresh, decided, foreign} {
		insert(t, repo, ctx, r)
	}
	if _, err := repo.Decide(ctx, org, decided.ID, approval.StatusRejected, &bob, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListExpired(ctx, now, 10); err == nil {
		t.Fatal("ListExpired outside a transaction")
	}
	err := db.Do(ctx, func(ctx context.Context) error {
		got, err := repo.ListExpired(ctx, now, 10)
		if err != nil || len(got) != 2 || got[0].ID != late.ID || got[1].ID != foreign.ID {
			t.Fatalf("expired: %+v %v", got, err)
		}
		if got, _ := repo.ListExpired(ctx, now, 1); len(got) != 1 {
			t.Fatalf("limit: %d", len(got))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
