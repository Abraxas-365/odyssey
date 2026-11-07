// Package policy is IT's rulebook: per-organization rules written in CEL
// that decide, for each action someone takes on an app, whether it is allowed,
// blocked, or needs IT's approval (docs/policy.md).
//
// Rules see a typed Input (the action, the app's tier, the environment, the
// access mode, who acts, the resource and the vulnerability scan). All enabled
// rules that match are combined: deny > approval > allow, and no match means
// allow. A rule that cannot be evaluated fails
// closed at its own strength: a deny rule denies, an approval rule asks for
// approval, an allow rule is ignored (it neither blocks nor grants).
//
// The domains that enforce rules (release, resource, ai) never import this
// package: each owns a port and an adapter in policy/adapters implements it.
package policy

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// Effect is what a rule does when its expression is true (gate.Effect: the
// domains read the verdict in the same words).
type Effect = gate.Effect

const (
	EffectAllow    = gate.Allow
	EffectDeny     = gate.Deny
	EffectApproval = gate.Approval
)

// EffectMessage is the 400 text for a value outside the set.
const EffectMessage = "effect must be allow, deny or approval"

// Limits.
const (
	MaxExpression = 2000
	// MaxRules per organization: the rulebook is read on every governed
	// action, so it stays small.
	MaxRules = 100
)

// Error codes.
const (
	CodeInvalid = "POLICY_INVALID"  // 422: the expression does not compile (message: the compiler's)
	CodeBuiltin = "POLICY_BUILTIN"  // 422: a built-in rule can be switched off, not changed or deleted
	CodeTooMany = "POLICY_TOO_MANY" // 422: the organization reached MaxRules
)

// ruleTag names the rule id.
type ruleTag struct{}

func (ruleTag) IDName() string { return "rule_id" }

// RuleID identifies one rule.
type RuleID = identity.ID[ruleTag]

func NewRuleID() RuleID                      { return identity.NewID[ruleTag]() }
func ParseRuleID(raw string) (RuleID, error) { return identity.ParseID[ruleTag](raw) }
func MustParseRuleID(raw string) RuleID      { return identity.MustParseID[ruleTag](raw) }

// Rule is one IT rule of an organization.
type Rule struct {
	ID           RuleID                  `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"-" db:"organization_id"`
	Name         string                  `json:"name" db:"name"`
	// Description is the rule as a sentence anyone can read; the console
	// shows it above the code.
	Description string `json:"description" db:"description"`
	// Expression is CEL over Input; it must be a bool.
	Expression string `json:"expression" db:"expression"`
	Effect     Effect `json:"effect" db:"effect"`
	// Approvers is the permission whose holders decide an approval (default
	// odyssey:approvals:decide).
	Approvers string `json:"approvers" db:"approvers"`
	Enabled   bool   `json:"enabled" db:"enabled"`
	// Builtin is the key of a rule every organization is born with (nil for
	// the ones IT wrote). Built-ins can be switched off, never changed or
	// deleted.
	Builtin   *string         `json:"builtin" db:"builtin"`
	Version   int             `json:"version" db:"version"`
	CreatedBy identity.UserID `json:"created_by" db:"created_by"`
	UpdatedBy identity.UserID `json:"updated_by" db:"updated_by"`
	CreatedAt time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt time.Time       `json:"updated_at" db:"updated_at"`
}

// IsBuiltin reports whether the rule came with the product.
func (r Rule) IsBuiltin() bool { return r.Builtin != nil }

// Ref is how a decision names a rule.
func (r Rule) Ref() RuleRef { return RuleRef{ID: r.ID, Name: r.Name} }

// RuleRef names a rule in a decision.
type RuleRef struct {
	ID   RuleID `json:"id"`
	Name string `json:"name"`
}

// approversPattern is the shape of a permission name: colon-separated words.
var approversPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(:[a-z0-9_*]+){1,3}$`)

// Draft is what a person writes to create or replace a rule.
type Draft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Expression  string `json:"expression"`
	Effect      Effect `json:"effect"`
	// Approvers defaults to odyssey:approvals:decide.
	Approvers string `json:"approvers"`
	// Enabled is a pointer so a new rule is on unless said otherwise.
	Enabled *bool `json:"enabled"`
}

// Normalize trims the text and fills the defaults.
func (d Draft) Normalize() Draft {
	d.Name, d.Description = text.Clean(d.Name), text.Clean(d.Description)
	d.Expression = strings.TrimSpace(d.Expression)
	d.Approvers = strings.TrimSpace(d.Approvers)
	if d.Approvers == "" {
		d.Approvers = permission.ApprovalsDecide
	}
	return d
}

// On is whether the rule is enabled (true when unset).
func (d Draft) On() bool { return d.Enabled == nil || *d.Enabled }

// Validate checks the fields; whether the expression compiles is the
// engine's to say (the service asks it).
func (d Draft) Validate() error {
	if err := text.Required("name", d.Name, text.MaxName); err != nil {
		return err
	}
	if err := text.Optional("description", d.Description, text.MaxDescription); err != nil {
		return err
	}
	if d.Expression == "" {
		return errx.InvalidField("expression", "expression is required")
	}
	if len(d.Expression) > MaxExpression {
		return errx.InvalidField("expression", "expression must be at most 2000 characters")
	}
	if !d.Effect.Valid() {
		return errx.InvalidField("effect", EffectMessage)
	}
	if !approversPattern.MatchString(d.Approvers) {
		return errx.InvalidField("approvers", "approvers must be a permission name such as a:b")
	}
	return nil
}

// Create is a Draft by a person (the actor comes from the token).
type Create struct {
	Draft
	CreatedBy identity.UserID `json:"-"`
}

// Replace overwrites a rule with a Draft (PUT).
type Replace struct {
	Draft
	UpdatedBy identity.UserID `json:"-"`
}

// Same reports whether the draft would change nothing but whether the rule
// is on: the only thing a built-in rule lets people change.
func (d Draft) Same(r Rule) bool {
	return d.Name == r.Name && d.Description == r.Description && d.Expression == r.Expression &&
		d.Effect == r.Effect && d.Approvers == r.Approvers
}

// Delete is the removal of a rule.
type Delete struct {
	DeletedBy identity.UserID
}

// InvalidExpression is the compiler's complaint about an expression.
type InvalidExpression struct{ Message string }

func (e *InvalidExpression) Error() string { return e.Message }

// InvalidError is the 422 POLICY_INVALID that carries the compiler's message
// (in the engine's words, so IT can fix the expression).
func InvalidError(message string) error {
	return errx.Business("La expresión no es válida: " + message).WithCode(CodeInvalid).WithField("expression")
}

// BuiltinError is the 422 for changing or deleting a built-in rule.
func BuiltinError() error {
	return errx.Business("Las reglas incorporadas solo se pueden apagar: para cambiarlas, apágala y crea la tuya.").WithCode(CodeBuiltin)
}

// Input is everything a rule can look at. Its JSON names are the CEL names
// (`project.tier`, `access.mode`, ...). Fields the action does not have are
// zero: an empty string, an empty list, false, 0.
type Input struct {
	Action   string        `json:"action"`
	Project  ProjectInput  `json:"project"`
	Env      EnvInput      `json:"env"`
	Access   AccessInput   `json:"access"`
	Actor    ActorInput    `json:"actor"`
	Resource ResourceInput `json:"resource"`
	Scan     ScanInput     `json:"scan"`
	// Services are the services the action brings: a deploy's or a
	// rollback's version, the new list of set_services (MS-3c).
	Services []ServiceInput `json:"services"`
	// Changed names the services whose running image changes, appears or
	// goes away against what the environment serves (set_services: those
	// added, removed or modified). A deployment from source knows it only
	// after the build (empty while scan.checked is false).
	Changed []string `json:"changed"`
	// TopologyChanged says the services themselves change (one added,
	// removed, or moved to another folder, route or kind) against what the
	// environment serves; always true for set_services.
	TopologyChanged bool `json:"topology_changed"`
}

// ServiceInput is one service as a rule sees it.
type ServiceInput struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Route string `json:"route"`
	// Public says it answers on the app's address (a web service with a route).
	Public bool `json:"public"`
}

type ProjectInput struct {
	Tier project.Tier `json:"tier"`
	Slug string       `json:"slug"`
}

type EnvInput struct {
	Name string `json:"name"`
}

type AccessInput struct {
	// Mode is the access mode being set: team, organization or public.
	Mode string `json:"mode"`
}

type ActorInput struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
}

type ResourceInput struct {
	// Kind is the resource's kind (database, cache, storage).
	Kind string `json:"kind"`
}

// ScanInput is the vulnerability scan of the image being published.
//
// Checked says the scan has been looked at: it is false at the deployment's
// first judgment (authorize, before anything is built, so there is no scan to
// know) and true at the second (authorize_scan, after the build), whatever
// came of the scan. Known says the scan exists and is usable (it was done, of
// this image); Critical and High are meaningful only then. So:
//
//	!checked           first judgment: nothing is known yet
//	checked && !known  the scan failed, never came, or the cluster has none
//	known              the counts are real (and checked is true)
type ScanInput struct {
	Known    bool `json:"known"`
	Checked  bool `json:"checked"`
	Critical int  `json:"critical"`
	High     int  `json:"high"`
}

// Validate checks a dry-run input.
func (in Input) Validate() error {
	if !slices.Contains(gate.Actions, in.Action) {
		return errx.InvalidField("action", "action must be one of "+strings.Join(gate.Actions, ", "))
	}
	if in.Project.Tier != "" && !in.Project.Tier.Valid() {
		return errx.InvalidField("project.tier", project.TierMessage)
	}
	if in.Scan.Critical < 0 || in.Scan.High < 0 {
		return errx.InvalidField("scan", "scan counts cannot be negative")
	}
	if len(in.Services) > project.MaxServices || len(in.Changed) > 2*project.MaxServices {
		return errx.InvalidField("services", "an app has at most "+strconv.Itoa(project.MaxServices)+" services")
	}
	return nil
}

// Decision is the verdict over all the enabled rules.
type Decision struct {
	Effect Effect `json:"effect"`
	// Rules are those that decided it (the matching rules of the winning
	// effect; empty when nothing matched).
	Rules []RuleRef `json:"rules"`
	// Approvers are the permissions that can approve (only for approval).
	Approvers []string `json:"approvers"`
}

// RuleResult is what one rule said about an input (the dry run shows them
// all, not only the deciding ones).
type RuleResult struct {
	Rule    RuleRef `json:"rule"`
	Effect  Effect  `json:"effect"`
	Enabled bool    `json:"enabled"`
	Matched bool    `json:"matched"`
	// Error is why the rule could not be evaluated; it then counts at its own
	// strength if it is a deny or approval rule, and is ignored if it allows.
	Error string `json:"error,omitempty"`
}

// Evaluation is a Decision with every rule's part in it.
type Evaluation struct {
	Decision
	Results []RuleResult `json:"results"`
}

// Candidate is an unsaved rule to try in a dry run. Replaces, when set, is the
// stored rule it would overwrite (editing): that one is left out of the dry
// run and the candidate takes its place; otherwise it is one more rule.
type Candidate struct {
	Draft
	Replaces RuleID `json:"-"`
}

// Dry is the body of POST /policy/evaluate: the input to judge and,
// optionally, an unsaved rule to try among the saved ones.
type Dry struct {
	Input
	// Rule is the unsaved rule (expression + effect, optional name); its
	// `replaces` names the stored rule it stands for.
	Rule *DryRule `json:"rule,omitempty"`
}

// DryRule is the wire form of a Candidate.
type DryRule struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Effect     Effect `json:"effect"`
	Approvers  string `json:"approvers"`
	Replaces   RuleID `json:"replaces"`
}

// Candidate is the rule to try, nil when the dry run has none.
func (d Dry) Candidate() *Candidate {
	if d.Rule == nil {
		return nil
	}
	return &Candidate{Draft: Draft{Name: d.Rule.Name, Expression: d.Rule.Expression, Effect: d.Rule.Effect,
		Approvers: d.Rule.Approvers}, Replaces: d.Rule.Replaces}
}

// Decide combines rule results: deny > approval > allow. Results of disabled
// rules are ignored. A rule that could not be evaluated (Error) fails closed at
// its own strength: an erroring deny rule denies, an erroring approval rule
// asks for approval, and an erroring allow rule is ignored (allow rules only
// make things explicit: an error must neither block nor grant).
func Decide(results []RuleResult, approvers map[RuleID]string) Decision {
	d := Decision{Effect: EffectAllow, Rules: []RuleRef{}, Approvers: []string{}}
	effective := func(r RuleResult) (Effect, bool) {
		switch {
		case !r.Enabled:
			return "", false
		case r.Error != "":
			if r.Effect == EffectDeny || r.Effect == EffectApproval {
				return r.Effect, true
			}
			return "", false
		case r.Matched:
			return r.Effect, true
		}
		return "", false
	}
	for _, r := range results {
		if e, ok := effective(r); ok && e.Stronger(d.Effect) {
			d.Effect = e
		}
	}
	for _, r := range results {
		if e, ok := effective(r); ok && e == d.Effect {
			d.Rules = append(d.Rules, r.Rule)
			if e == EffectApproval {
				if a := approvers[r.Rule.ID]; a != "" && !slices.Contains(d.Approvers, a) {
					d.Approvers = append(d.Approvers, a)
				}
			}
		}
	}
	return d
}
