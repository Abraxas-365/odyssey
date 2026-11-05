package approval

import (
	"context"
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Commands are the write operations on requests, implemented by
// approvalsvc.Service. The organization comes from the token; who decides
// (Viewer) comes from the actor on the context, never from the body.
type Commands interface {
	// Open holds an action. Asking again for the same thing (same run and
	// step, or the same person asking the same action again while it is
	// pending) returns the request that already exists.
	Open(ctx context.Context, organization identity.OrganizationID, input Open) (Request, error)
	// Approve and Reject decide a pending request: the viewer needs ANY of
	// its approvers' permissions and must not be the requester (403
	// APPROVAL_SELF); a request already decided is 409 APPROVAL_DECIDED.
	Approve(ctx context.Context, organization identity.OrganizationID, id ID, input Decide) (Request, error)
	Reject(ctx context.Context, organization identity.OrganizationID, id ID, input Decide) (Request, error)
	// Cancel is the requester taking back their own pending request.
	Cancel(ctx context.Context, organization identity.OrganizationID, id ID, input Cancel) (Request, error)
	// CancelRun cancels the pending request a workflow run holds at step (a
	// newer deployment replaced the one waiting); nothing pending is a no-op.
	CancelRun(ctx context.Context, organization identity.OrganizationID, run, step string) error
	// Redeem is how a governed domain uses an approval: it must be approved,
	// unused, for the actor on ctx (the requester) and for this very action
	// (same action, place and payload). It is consumed atomically (409
	// APPROVAL_NOT_USABLE otherwise).
	Redeem(ctx context.Context, organization identity.OrganizationID, id ID, grant Grant) error
	// Consume marks an approved request used without a grant: the deployment
	// workflow it released is the use.
	Consume(ctx context.Context, organization identity.OrganizationID, id ID) error
	// Fail records why an approved action could not run (and publishes it).
	Fail(ctx context.Context, organization identity.OrganizationID, id ID, message string) error
	// Expire marks the pending requests past their time as expired, a batch
	// at a time, and returns how many it expired (every organization: it is
	// the schedule's work).
	Expire(ctx context.Context) (int, error)
}

// Executor re-runs one kind of approved action (an adapter per governed
// domain: approvalrelease, approvalresource, approvalai). The reactor calls
// it with the requester as the actor on ctx and the approval's mark
// (gate.WithApproval), so the governed service redeems the approval instead
// of holding the action again.
type Executor interface {
	// Handles reports whether the executor re-runs r (by its action and the
	// "op" its payload names).
	Handles(r Request) bool
	// Execute runs the action as the requester. An error is recorded on the
	// request (execution_error) and published; nothing else retries it.
	Execute(ctx context.Context, r Request) error
}

// Queries are the read operations.
type Queries interface {
	// Find reads one request the viewer may see (theirs, or one they could
	// decide); any other is 404.
	Find(ctx context.Context, organization identity.OrganizationID, id ID, viewer Viewer) (View, error)
	// List reads what the viewer may see, newest first.
	List(ctx context.Context, organization identity.OrganizationID, viewer Viewer, filter Filter, page query.Pagination) (query.Paginated[View], error)
	// Get reads any request of the organization (for the system's own
	// reactions: re-running an approved action, signalling a run).
	Get(ctx context.Context, organization identity.OrganizationID, id ID) (Request, error)
}

// Repository stores requests, scoped by organization.
type Repository interface {
	// Insert stores the request; false when its Key is already held by an
	// open request (the caller reads that one).
	Insert(ctx context.Context, r Request) (bool, error)
	// FindOpen reads the request holding key: the pending one, or the one of
	// a workflow step (whatever its status).
	FindOpen(ctx context.Context, organization identity.OrganizationID, key string) (Request, error)
	Find(ctx context.Context, organization identity.OrganizationID, id ID) (Request, error)
	FindForUpdate(ctx context.Context, organization identity.OrganizationID, id ID) (Request, error)
	// FindByRun is the request a run holds at step (the latest, whatever its status).
	FindByRun(ctx context.Context, organization identity.OrganizationID, run, step string) (Request, error)
	List(ctx context.Context, organization identity.OrganizationID, viewer Viewer, filter Filter, page query.Pagination) (query.Paginated[Request], error)
	// ListExpired reads (and locks, skipping rows others hold) up to limit
	// pending requests of any organization whose time ran out by now.
	ListExpired(ctx context.Context, now time.Time, limit int) ([]Request, error)
	// Decide moves a pending request to status (false when it was not pending).
	Decide(ctx context.Context, organization identity.OrganizationID, id ID, status Status, by *identity.UserID, note string, at time.Time) (bool, error)
	// Consume sets consumed_at on an approved, unused request (false when it
	// was not).
	Consume(ctx context.Context, organization identity.OrganizationID, id ID, at time.Time) (bool, error)
	SetExecutionError(ctx context.Context, organization identity.OrganizationID, id ID, message string) error
}
