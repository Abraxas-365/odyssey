package gate

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// What a governed domain does when the rulebook holds an action for approval
// (docs/approvals.md): it stores an approval request through its OWN
// Approvals port and answers Pending (HTTP 202, MCP pending_approval); when
// IT approves, the action runs again as the requester with the approval's
// mark on the context (WithApproval) and the domain redeems it.

// StatusPendingApproval is the `status` of an answer that says "IT must
// decide first" (HTTP 202 and MCP).
const StatusPendingApproval = "pending_approval"

// PendingMessage is what the person asking reads (plain Spanish).
const PendingMessage = "Le pedimos a TI que lo apruebe. Te avisamos cuando lo decidan."

// Pending is the outcome of an action held for approval: no error, nothing
// changed yet.
type Pending struct {
	// Status is always StatusPendingApproval: it is what tells this answer
	// from the action's own result.
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

// NewPending is the answer for the request that holds the action.
func NewPending(request string) Pending {
	return Pending{Status: StatusPendingApproval, RequestID: request, Message: PendingMessage}
}

// Outcome is embedded in the result of a direct action a rule may hold: when
// Pending is set the action did NOT happen (the rest of the result is empty)
// and the transport answers 202 with the Pending (HTTP) or a
// pending_approval answer (MCP) instead of the action's own result. It is
// never on the wire as a field of the result.
type Outcome struct {
	Pending *Pending `json:"-"`
}

// Held reports whether the action was held for approval.
func (o Outcome) Held() bool { return o.Pending != nil }

// Holding is the outcome of an action held as p.
func Holding(p Pending) Outcome { return Outcome{Pending: &p} }

// Held turns Enforce's answer into an Outcome (the zero one when p is nil:
// the action may go on).
func Held(p *Pending) Outcome {
	if p == nil {
		return Outcome{}
	}
	return Holding(*p)
}

// SignalApproval is the workflow signal that tells a run waiting for IT's
// decision (a deployment's authorize step) what became of its request; its
// payload is a Decision. The approval domain sends it, the governed domain's
// workflow waits for it: they share only this vocabulary.
const SignalApproval = "approval"

// Decision values: what became of a request (approval.Status, as text).
const (
	DecisionAllowed   = "allowed" // IT's rules let it go on: nothing was asked
	DecisionApproved  = "approved"
	DecisionRejected  = "rejected"
	DecisionExpired   = "expired"
	DecisionCancelled = "cancelled"
)

// Decision is the payload of SignalApproval.
type Decision struct {
	Request string `json:"request_id"`
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

// Detail is how a caller that asks on behalf of a bigger operation (resource
// creating a resource that connects) describes what will re-run when the
// approval comes: one plain Spanish sentence and the operation's own
// deterministic payload (offered back to redeem the approval).
type Detail struct {
	Summary string
	Payload any
}

// Hold is what a domain gives its Approvals port when the verdict is
// approval: the action, where, what exactly (Payload, as the domain will
// re-run it) and the rules that asked for it.
type Hold struct {
	Action      string
	Project     string
	Environment string
	// Summary is one plain Spanish sentence: "Hacer pública «app» (producción)".
	Summary string
	// Payload is the domain's own JSON-able description of the action. The
	// same value is offered back (Grant) to redeem the approval, so it must
	// be deterministic.
	Payload any
	Verdict Verdict
	// Run and RunStep tie the request to a workflow step (a deployment's
	// authorize step): asking twice for the same pair returns the same
	// request. Empty for direct actions.
	Run     string
	RunStep string
}

// Grant is what a domain presents to redeem an approval: it must match the
// approved request (organization, action, where and payload) and be unused.
type Grant struct {
	Action      string
	Project     string
	Environment string
	Payload     any
}

type approvalKey struct{}

// WithApproval marks ctx as running an approved request: the governed
// service redeems it instead of holding the action again.
func WithApproval(ctx context.Context, request string) context.Context {
	return context.WithValue(ctx, approvalKey{}, request)
}

// ApprovalFrom is the approved request WithApproval marked ctx with.
func ApprovalFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(approvalKey{}).(string)
	return id, ok && id != ""
}

// Checker is the Policy port every governed domain declares.
type Checker interface {
	Check(ctx context.Context, organization identity.OrganizationID, request Request) (Verdict, error)
}

// Approver is the Approvals port every governed domain declares (each in its
// own package: none imports internal/approval).
type Approver interface {
	Hold(ctx context.Context, organization identity.OrganizationID, hold Hold) (Pending, error)
	Redeem(ctx context.Context, organization identity.OrganizationID, request string, grant Grant) error
}

// RunApprover is the part of the Approvals port a domain with a workflow that
// waits for a decision (release's deploy) also needs: use up the approval the
// run was released with, and withdraw the request of a run that is replaced.
type RunApprover interface {
	Approver
	// Consume marks an approved request used (idempotent).
	Consume(ctx context.Context, organization identity.OrganizationID, request string) error
	// CancelRun cancels the pending request of a workflow run (nothing
	// pending is a no-op).
	CancelRun(ctx context.Context, organization identity.OrganizationID, run string) error
}

// Enforce is the single enforcement function every governed service's
// `authorize` calls, so the three domains answer alike: nil, nil lets the
// action go on; a Pending means it was held and nothing may change; an error
// is a refusal (POLICY_DENIED 403) or a failure.
//
// An approval verdict is satisfied only by a redeemed approval (ctx marked
// by WithApproval, matching the Grant): never by the mere existence of one.
func Enforce(ctx context.Context, organization identity.OrganizationID, policy Checker, approvals Approver, request Request, hold Hold) (*Pending, error) {
	if policy == nil {
		return nil, errx.Internal("IT's rules are not wired")
	}
	verdict, err := policy.Check(ctx, organization, request)
	if err != nil {
		return nil, err
	}
	switch verdict.Effect {
	case Deny:
		return nil, verdict.Err()
	case Approval:
		if approvals == nil { // fail closed: nothing is allowed by accident
			return nil, errx.Internal("approvals are not wired")
		}
		hold.Action, hold.Verdict = request.Action, verdict
		if id, ok := ApprovalFrom(ctx); ok {
			return nil, approvals.Redeem(ctx, organization, id,
				Grant{Action: hold.Action, Project: hold.Project, Environment: hold.Environment, Payload: hold.Payload})
		}
		pending, err := approvals.Hold(ctx, organization, hold)
		if err != nil {
			return nil, err
		}
		return &pending, nil
	}
	return nil, nil
}

// DenyOnly is Enforce's cheap cousin for the moment a request arrives: it
// refuses a deny at once (so a refused deploy uploads nothing and answers
// 403 immediately) but lets an approval verdict pass: the deployment's
// workflow judges it again, as its own `authorize` step.
func DenyOnly(ctx context.Context, organization identity.OrganizationID, policy Checker, request Request) error {
	if policy == nil {
		return errx.Internal("IT's rules are not wired")
	}
	verdict, err := policy.Check(ctx, organization, request)
	if err != nil {
		return err
	}
	if verdict.Effect == Deny {
		return verdict.Err()
	}
	return nil
}
