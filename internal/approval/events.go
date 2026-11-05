package approval

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the approvals' public contract. Payloads carry facts and IDs
// (never the requester's permissions); add fields freely, rename or remove
// one only in a new version.
const (
	EventRequested       = "approval.requested"
	EventApproved        = "approval.approved"
	EventRejected        = "approval.rejected"
	EventExpired         = "approval.expired"
	EventCancelled       = "approval.cancelled"
	EventConsumed        = "approval.consumed"
	EventExecutionFailed = "approval.execution_failed"
)

// SubjectKind prefixes the events' subject ("approval/<id>").
const SubjectKind = "approval"

// Requested v1.
type Requested struct {
	Approval    string    `json:"approval_id"`
	Action      string    `json:"action"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Summary     string    `json:"summary"`
	RequestedBy string    `json:"requested_by"`
	Reason      string    `json:"reason"`
	Rules       []Rule    `json:"rules"`
	Approvers   []string  `json:"approvers"`
	ExpiresAt   time.Time `json:"expires_at"`
	Run         string    `json:"run_id"`
}

// Decided is the payload of approved, rejected, expired and cancelled (v1):
// what became of the request. DecidedBy is empty when nobody decided it
// (expired, or replaced by a newer deployment).
type Decided struct {
	Approval    string `json:"approval_id"`
	Action      string `json:"action"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	RequestedBy string `json:"requested_by"`
	DecidedBy   string `json:"decided_by"`
	Note        string `json:"note"`
	Run         string `json:"run_id"`
	// RunStep is the workflow step that waits for the decision ("" for a
	// direct action, and for events published before steps were named: the
	// run's first step then).
	RunStep string `json:"run_step"`
}

// Consumed v1: the approval was used (the action ran, or the deployment it
// held was released).
type Consumed struct {
	Approval    string `json:"approval_id"`
	Action      string `json:"action"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Run         string `json:"run_id"`
}

// ExecutionFailed v1: the approved action could not run; Error says why in
// words (the requester sees it on the request).
type ExecutionFailed struct {
	Approval    string `json:"approval_id"`
	Action      string `json:"action"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Error       string `json:"error"`
}

func userText(id identity.UserID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}

func requester(r Request) string {
	if !r.RequestedBy.IsZero() {
		return r.RequestedBy.String()
	}
	return r.RequestedSystem
}

func NewRequested(r Request) event.Event {
	rules := r.Rules
	if rules == nil {
		rules = []Rule{}
	}
	return event.New(EventRequested, 1, r.Organization, r.RequestedBy, event.Subject(SubjectKind, r.ID),
		"Pidió aprobación de TI: "+r.Summary,
		Requested{Approval: r.ID.String(), Action: r.Action, Project: r.Project.String(), Environment: r.Environment.String(),
			Summary: r.Summary, RequestedBy: requester(r), Reason: r.Reason, Rules: rules, Approvers: r.Approvers,
			ExpiresAt: r.ExpiresAt, Run: r.Run})
}

func decided(r Request, by identity.UserID, note string) Decided {
	return Decided{Approval: r.ID.String(), Action: r.Action, Project: r.Project.String(), Environment: r.Environment.String(),
		RequestedBy: requester(r), DecidedBy: userText(by), Note: note, Run: r.Run, RunStep: r.RunStep}
}

func NewApproved(r Request, by identity.UserID, note string) event.Event {
	return event.New(EventApproved, 1, r.Organization, by, event.Subject(SubjectKind, r.ID),
		"Aprobó: "+r.Summary, decided(r, by, note))
}

func NewRejected(r Request, by identity.UserID, note string) event.Event {
	return event.New(EventRejected, 1, r.Organization, by, event.Subject(SubjectKind, r.ID),
		"Rechazó: "+r.Summary, decided(r, by, note))
}

// NewExpired: nobody decided in time (the actor is the system).
func NewExpired(r Request) event.Event {
	return event.New(EventExpired, 1, r.Organization, identity.UserID{}, event.Subject(SubjectKind, r.ID),
		"Venció sin decisión: "+r.Summary, decided(r, identity.UserID{}, ""))
}

// NewCancelled: the requester took it back (by is them), or a newer
// deployment replaced it (by is zero).
func NewCancelled(r Request, by identity.UserID) event.Event {
	summary := "Canceló la solicitud: " + r.Summary
	if by.IsZero() {
		summary = "Se canceló la solicitud (la reemplazó otra): " + r.Summary
	}
	return event.New(EventCancelled, 1, r.Organization, by, event.Subject(SubjectKind, r.ID), summary, decided(r, by, ""))
}

func NewConsumed(r Request) event.Event {
	return event.New(EventConsumed, 1, r.Organization, identity.UserID{}, event.Subject(SubjectKind, r.ID),
		"Se usó la aprobación: "+r.Summary,
		Consumed{Approval: r.ID.String(), Action: r.Action, Project: r.Project.String(), Environment: r.Environment.String(), Run: r.Run})
}

func NewExecutionFailed(r Request, message string) event.Event {
	return event.New(EventExecutionFailed, 1, r.Organization, identity.UserID{}, event.Subject(SubjectKind, r.ID),
		"No se pudo hacer lo aprobado: "+r.Summary,
		ExecutionFailed{Approval: r.ID.String(), Action: r.Action, Project: r.Project.String(), Environment: r.Environment.String(), Error: message})
}
