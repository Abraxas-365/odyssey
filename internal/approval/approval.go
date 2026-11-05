// Package approval is what happens when IT's rules hold an action: a Request
// that someone with the right permission approves or rejects (docs/approvals.md).
//
// The governed domains (release, resource, ai) never import this package:
// each holds an action through its own Approvals port (an adapter in
// approval/adapters implements it, over Commands). What runs when a request
// is approved is wired in the composition root: a direct action re-runs as
// the requester through its domain's Commands with the approval's mark on
// the context (gate.WithApproval); a deployment's workflow is signalled.
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type tag struct{}

func (tag) IDName() string { return "approval_id" }

// ID identifies one request.
type ID = identity.ID[tag]

func NewID() ID                      { return identity.NewID[tag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[tag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[tag](raw) }

// Status of a request. Only pending can be decided.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusExpired   Status = "expired"   // nobody decided within TTL
	StatusCancelled Status = "cancelled" // the requester took it back, or a newer deploy replaced it
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusApproved, StatusRejected, StatusExpired, StatusCancelled:
		return true
	}
	return false
}

// Decided reports whether the request left pending.
func (s Status) Decided() bool { return s != StatusPending }

// TTL is how long a request waits for a decision.
const TTL = 7 * 24 * time.Hour

// Limits and codes.
const (
	MaxNote = 500

	CodeSelf      = "APPROVAL_SELF"       // 403: nobody decides their own request
	CodeDecided   = "APPROVAL_DECIDED"    // 409: the request is not pending any more
	CodeNotUsable = "APPROVAL_NOT_USABLE" // 409: the approval does not cover this action, or was used
)

// Rule names a rule that held the action.
type Rule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Request is one action held for approval.
type Request struct {
	ID           ID                      `json:"id"`
	Organization identity.OrganizationID `json:"-"`
	// Action is the gate action held (deploy, set_access, ...).
	Action      string                `json:"action"`
	Project     project.ID            `json:"project_id"`
	Environment project.EnvironmentID `json:"environment_id"`
	// Summary is the action as one plain Spanish sentence.
	Summary string `json:"summary"`
	// Payload is the domain's own description of the action, as it re-runs.
	Payload json.RawMessage `json:"payload"`
	// RequestedBy is the person who asked (zero: a system actor, see
	// RequestedSystem). RequesterPermissions is the snapshot of what they
	// could do when they asked: the approved action re-runs as them with
	// exactly that.
	RequestedBy          identity.UserID `json:"requested_by"`
	RequestedSystem      string          `json:"requested_system,omitempty"`
	RequesterPermissions []string        `json:"-"`
	// Reason is why it was held, in words (the rules' names).
	Reason string `json:"reason"`
	Rules  []Rule `json:"rules"`
	// Approvers are the permissions that can decide: any ONE suffices.
	Approvers    []string         `json:"approvers"`
	Status       Status           `json:"status"`
	DecidedBy    *identity.UserID `json:"decided_by"`
	DecidedAt    *time.Time       `json:"decided_at"`
	DecisionNote string           `json:"decision_note"`
	ExpiresAt    time.Time        `json:"expires_at"`
	// ConsumedAt is when the approval was used (the action ran, or the
	// deployment continued): an approval is good for one use.
	ConsumedAt     *time.Time `json:"consumed_at"`
	ExecutionError string     `json:"execution_error,omitempty"`
	// Run and RunStep tie the request to the workflow step that waits for it
	// (a deployment's authorize step); empty for direct actions.
	Run       string    `json:"run_id,omitempty"`
	RunStep   string    `json:"run_step,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Key makes asking twice for the same thing one request (set by Open).
	Key string `json:"-"`
}

// CanBeDecidedBy reports whether permissions include any of the approvers.
func (r Request) CanBeDecidedBy(permissions []string) bool {
	for _, a := range r.Approvers {
		if slices.Contains(permissions, a) {
			return true
		}
	}
	return false
}

// ExpiredAt reports whether the request ran out of time by now.
func (r Request) ExpiredAt(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Usable reports whether the approval can still be redeemed.
func (r Request) Usable() bool { return r.Status == StatusApproved && r.ConsumedAt == nil }

// Viewer is who reads or decides: the user and what their token allows.
type Viewer struct {
	User        identity.UserID
	Permissions []string
}

// Can reports whether the viewer may decide r (never their own).
func (v Viewer) Can(r Request) bool {
	return !v.User.IsZero() && v.User != r.RequestedBy && r.CanBeDecidedBy(v.Permissions)
}

// Sees reports whether r is visible to the viewer: their own, or one they
// could decide.
func (v Viewer) Sees(r Request) bool {
	return (!v.User.IsZero() && v.User == r.RequestedBy) || r.CanBeDecidedBy(v.Permissions)
}

// Open is the request to hold an action (Commands.Open).
type Open struct {
	Action      string
	Project     project.ID
	Environment project.EnvironmentID
	Summary     string
	Payload     any
	Reason      string
	Rules       []Rule
	Approvers   []string
	Run         string
	RunStep     string
	// Requester is who asked: the actor on the context.
	Requester Requester
}

// Requester is who asked for the held action.
type Requester struct {
	User        identity.UserID
	System      string
	Permissions []string
}

func (in Open) Validate() error {
	switch {
	case in.Action == "":
		return errx.Internal("approval: an action is needed")
	case in.Project.IsZero() || in.Environment.IsZero():
		return errx.Internal("approval: a project and an environment are needed")
	case strings.TrimSpace(in.Summary) == "":
		return errx.Internal("approval: a summary is needed")
	case in.Run != "" && in.RunStep == "":
		return errx.Internal("approval: a run needs its step")
	}
	return nil
}

// Normalized fills the approvers' default.
func (in Open) Normalized() Open {
	if len(in.Approvers) == 0 {
		in.Approvers = []string{permission.ApprovalsDecide}
	}
	in.Approvers = slices.Clone(in.Approvers)
	slices.Sort(in.Approvers)
	in.Approvers = slices.Compact(in.Approvers)
	return in
}

// Decide is a decision on a request.
type Decide struct {
	By   Viewer
	Note string
}

// View is a request as a viewer sees it.
type View struct {
	Request
	// Mine: the viewer asked for it. CanDecide: the viewer may approve or
	// reject it now (pending, not theirs, holding one of its approvers).
	Mine      bool `json:"mine"`
	CanDecide bool `json:"can_decide"`
}

func (d Decide) Validate() error {
	if len([]rune(d.Note)) > MaxNote {
		return errx.InvalidField("note", "note must be at most 500 characters")
	}
	return nil
}

// Cancel is the requester taking a request back.
type Cancel struct{ By Viewer }

// Grant is what a governed domain presents to redeem an approval.
type Grant struct {
	Action      string
	Project     project.ID
	Environment project.EnvironmentID
	Payload     any
}

// Filter narrows List. Mine: only the viewer's own; Decidable: only those the
// viewer could decide (not their own); neither: both kinds.
type Filter struct {
	Status    []Status
	Mine      bool
	Decidable bool
}

func (f Filter) Validate() error {
	for _, s := range f.Status {
		if !s.Valid() {
			return errx.InvalidField("status", "status must be pending, approved, rejected, expired or cancelled")
		}
	}
	if f.Mine && f.Decidable {
		return errx.Validation("mine and decidable cannot be asked together")
	}
	return nil
}

// Canonical re-encodes a JSON payload with sorted keys so two descriptions
// of the same action compare equal.
func Canonical(payload any) (json.RawMessage, error) {
	var raw []byte
	switch p := payload.(type) {
	case nil:
		raw = []byte("{}")
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	default:
		var err error
		if raw, err = json.Marshal(p); err != nil {
			return nil, errx.Wrap(err, "approval: encode payload", errx.TypeInternal)
		}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errx.Wrap(err, "approval: decode payload", errx.TypeInternal)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, errx.Wrap(err, "approval: encode payload", errx.TypeInternal)
	}
	return out, nil
}

// SamePayload reports whether two canonical payloads describe the same action.
func SamePayload(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// KeyOf is the identity of a held action while it is pending: asking again
// for the same thing (same person, action, place and payload) is the same
// request. A request tied to a workflow step is identified by it.
func KeyOf(o Open, payload json.RawMessage) string {
	if o.Run != "" {
		return "run:" + o.Run + "/" + o.RunStep
	}
	h := sha256.New()
	for _, part := range []string{o.Requester.User.String(), o.Requester.System, o.Action, o.Environment.String(), string(payload)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "act:" + hex.EncodeToString(h.Sum(nil))
}

// ReasonOf words why the rules held an action.
func ReasonOf(rules []Rule) string {
	names := make([]string, len(rules))
	for i, r := range rules {
		names[i] = r.Name
	}
	switch len(names) {
	case 0:
		return "Las reglas de TI piden aprobación."
	case 1:
		return "La regla de TI «" + names[0] + "» pide aprobación."
	}
	return "Las reglas de TI «" + strings.Join(names, "», «") + "» piden aprobación."
}

// ErrSelf is the 403 for deciding one's own request.
func ErrSelf() error {
	return errx.Forbidden("No puedes decidir una solicitud que hiciste tú: tiene que verla otra persona.").WithCode(CodeSelf)
}

// ErrDecided is the 409 for a request that is not pending any more.
func ErrDecided(s Status) error {
	msg := "Esta solicitud ya se decidió."
	if s == StatusExpired {
		msg = "Esta solicitud venció sin que nadie la decidiera."
	}
	return errx.Conflict(msg).WithCode(CodeDecided).WithDetail("status", string(s))
}

// ErrNotUsable is the 409 for an approval that does not cover the action.
func ErrNotUsable() error {
	return errx.Conflict("La aprobación no sirve para esta acción (ya se usó, o es de otra).").WithCode(CodeNotUsable)
}
