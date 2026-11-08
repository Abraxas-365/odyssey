// Package gate is the vocabulary shared by IT's rulebook (internal/policy) and
// the domains it governs (release, resource, ai): what is being asked
// (Request), what the rules said (Verdict) and how a refusal reads (Err).
//
// Each governed domain owns a Policy port that speaks these types, and an
// adapter in internal/policy/adapters implements it; no domain imports policy.
// This package imports no domain, so all of them may use it.
package gate

import (
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Actions: what a rule can govern (the values of `action` in CEL).
const (
	ActionDeploy          = "deploy"
	ActionSetAccess       = "set_access"
	ActionConnectResource = "connect_resource"
	ActionDeleteResource  = "delete_resource"
	ActionSetSecret       = "set_secret"
	ActionEnableAI        = "enable_ai"
	ActionRollback        = "rollback"
	// ActionSetServices changes the app's services (the whole app: it has no
	// environment).
	ActionSetServices = "set_services"
	// ActionCreateResource creates a resource (connected or not); the
	// catalog has already resolved its recipe (Request.Resource).
	ActionCreateResource = "create_resource"
	// ActionChangeResource changes an existing resource's parameters.
	ActionChangeResource = "change_resource"
)

// Actions lists every action a rule can name.
var Actions = []string{ActionDeploy, ActionSetAccess, ActionConnectResource, ActionDeleteResource,
	ActionSetSecret, ActionEnableAI, ActionRollback, ActionSetServices, ActionCreateResource, ActionChangeResource}

// Effect is what the rules decided.
type Effect string

const (
	Allow    Effect = "allow"
	Deny     Effect = "deny"
	Approval Effect = "approval"
)

// Valid reports whether e is one of the three effects.
func (e Effect) Valid() bool { return e == Allow || e == Deny || e == Approval }

// Stronger reports whether e beats other (deny > approval > allow).
func (e Effect) Stronger(other Effect) bool { return e.rank() > other.rank() }

func (e Effect) rank() int {
	switch e {
	case Deny:
		return 2
	case Approval:
		return 1
	}
	return 0
}

// Error codes of a refusal.
const (
	CodeDenied           = "POLICY_DENIED"     // 403
	CodeApprovalRequired = "APPROVAL_REQUIRED" // 409
)

// Request is what a domain tells the rulebook about the action it is about to
// take. Who acts is not here: the adapter reads it from the context (actor).
// Fields the action does not have stay empty.
type Request struct {
	Action       string
	Tier         string // the app's tier: sandbox, internal, production
	ProjectSlug  string
	Environment  string // the environment's slug
	AccessMode   string // set_access: the mode being set
	ResourceKind string // connect_resource, delete_resource, create_resource, change_resource
	// Recipe is what the catalog resolved for create_resource and
	// change_resource (nil for the other actions).
	Recipe *Recipe
	// Scan is the vulnerability scan of what is being published; nil when
	// there is none yet (deploy asks before building) or it is unknown.
	Scan *Scan
	// ScanChecked says this is the judgment made after the build, with the
	// scan's outcome in view (authorize_scan): a nil Scan then means the scan
	// is unknown for good (failed, never came, not supported), not "not yet".
	// It is what lets a rule require a scan (`scan.checked && !scan.known`)
	// without firing at the first judgment, where the scan never exists.
	ScanChecked bool
	// Services are the services the action brings (MS-3c): a deploy's or a
	// rollback's version, the new list of set_services. Empty for the
	// actions that are not about them.
	Services []Service
	// Changed names the services whose running image changes against what
	// the environment serves (deploy, rollback; set_services: those added,
	// removed or modified). A deployment from source knows it only after
	// its build: empty at its first judgment, filled at the second
	// (ScanChecked).
	Changed []string
	// TopologyChanged says the services themselves change (one added,
	// removed, renamed, moved to another folder or route) against what the
	// environment serves (always true for set_services and for an
	// environment that serves nothing yet).
	TopologyChanged bool
}

// Service is one service as IT's rules see it.
type Service struct {
	Name string
	Kind string // web or worker
	// Route is the path it answers on the app's address ("" internal).
	Route string
	// Public says it answers on the app's address.
	Public bool
}

// Scan is the count of findings of an image scan.
type Scan struct{ Critical, High int }

// Recipe is a resource's recipe as IT's rules see it: pinned and with its
// parameters normalized by the catalog. OutOfRange are the parameters
// outside what IT pre-approved, computed by the server (never sent by a
// client).
type Recipe struct {
	Name       string
	Version    int
	Engine     string
	Params     map[string]int
	OutOfRange []string
}

// Rule names the rule that decided.
type Rule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Verdict is the rulebook's answer to a Request.
type Verdict struct {
	Effect Effect
	// Rules are the ones that decided it (empty for allow).
	Rules []Rule
	// Approvers are the permissions whose holders can approve (approval only).
	Approvers []string
}

// Allowed reports whether nothing stands in the way.
func (v Verdict) Allowed() bool { return v.Effect == "" || v.Effect == Allow }

// With combines verdicts about one operation (a create that connects, a
// request outside the catalog's pre-approved range) into the one that
// decides it: the strongest effect wins (deny > approval > allow), and the
// rules and approvers of every verdict at that effect are kept, without
// repeats. One operation is held once, whatever asked for approval.
func (v Verdict) With(others ...Verdict) Verdict {
	out := Verdict{Effect: v.effect()}
	for _, o := range append([]Verdict{v}, others...) {
		if e := o.effect(); e.Stronger(out.Effect) {
			out.Effect = e
		}
	}
	if out.Effect == Allow {
		return out
	}
	for _, o := range append([]Verdict{v}, others...) {
		if o.effect() != out.Effect {
			continue
		}
		for _, r := range o.Rules {
			if !slices.Contains(out.Rules, r) {
				out.Rules = append(out.Rules, r)
			}
		}
		for _, a := range o.Approvers {
			if !slices.Contains(out.Approvers, a) {
				out.Approvers = append(out.Approvers, a)
			}
		}
	}
	return out
}

func (v Verdict) effect() Effect {
	if v.Effect == "" {
		return Allow
	}
	return v.Effect
}

// Err is the refusal a verdict stands for: nil for allow, POLICY_DENIED (403)
// for deny, APPROVAL_REQUIRED (409) for approval. Every governed service calls
// it in its one enforcement function (the seam 3b replaces, for approval, with
// "create the request and answer 202").
func (v Verdict) Err() error {
	switch v.Effect {
	case Deny:
		return refusal(errx.Forbidden("La regla de TI «"+v.names()+"» no permite esto.").WithCode(CodeDenied), v)
	case Approval:
		return refusal(errx.Conflict("Esta acción necesita aprobación de TI (regla «"+v.names()+"»).").WithCode(CodeApprovalRequired), v)
	}
	return nil
}

// Refusal reports whether e is one of the two refusals Err produces.
func Refusal(e *errx.Error) bool {
	return e != nil && (e.Code == CodeDenied || e.Code == CodeApprovalRequired)
}

func (v Verdict) names() string {
	names := make([]string, len(v.Rules))
	for i, r := range v.Rules {
		names[i] = r.Name
	}
	return strings.Join(names, "», «")
}

func refusal(e *errx.Error, v Verdict) error {
	ids := make([]string, len(v.Rules))
	for i, r := range v.Rules {
		ids[i] = r.ID
	}
	e = e.WithDetail("rules", ids)
	if v.Effect == Approval {
		e = e.WithDetail("approvers", v.Approvers)
	}
	return e
}
