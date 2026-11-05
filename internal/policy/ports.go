package policy

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Commands are the write operations on the rulebook, implemented by
// policysvc.Service. The caller needs odyssey:policy:manage (the HTTP adapter
// gates it); the organization comes from the token.
type Commands interface {
	// Create adds a rule. 422 POLICY_INVALID when the expression does not
	// compile to a bool (the message is the compiler's).
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Rule, error)
	// Replace overwrites a rule. A built-in rule only takes `enabled`
	// changes (422 POLICY_BUILTIN otherwise).
	Replace(ctx context.Context, organization identity.OrganizationID, id RuleID, input Replace) (Rule, error)
	// Delete removes a rule; a built-in one cannot be deleted (422
	// POLICY_BUILTIN): switch it off instead.
	Delete(ctx context.Context, organization identity.OrganizationID, id RuleID, input Delete) error
}

// Queries are the read operations on the rulebook. The first read of an
// organization seeds its built-in rules (docs/policy.md).
type Queries interface {
	// List returns the organization's rules: built-ins first, then by age.
	List(ctx context.Context, organization identity.OrganizationID) ([]Rule, error)
	Find(ctx context.Context, organization identity.OrganizationID, id RuleID) (Rule, error)
}

// Evaluator decides actions. The domains reach it through their own ports
// (release.Policy, ...), implemented by adapters in policy/adapters.
type Evaluator interface {
	// Evaluate combines every enabled rule: deny > approval > allow; no
	// match is allow. A rule that fails to evaluate fails closed at its own
	// strength (deny denies, approval asks for approval; an allow rule that
	// errors is ignored: it neither blocks nor grants).
	Evaluate(ctx context.Context, organization identity.OrganizationID, input Input) (Decision, error)
	// Explain is Evaluate with each rule's own result (the dry run). A
	// non-nil candidate is an unsaved rule tried as if it were saved: it
	// replaces the stored rule with its ID, or joins the rules; nothing is
	// stored. The candidate must compile (422 POLICY_INVALID).
	Explain(ctx context.Context, organization identity.OrganizationID, input Input, candidate *Candidate) (Evaluation, error)
}

// Repository is the persistence contract of policy.rules; only the service
// depends on it. Every statement filters by organization (another tenant's
// rule is NotFound).
type Repository interface {
	// List returns the organization's rules: built-ins first, then oldest first.
	List(ctx context.Context, organization identity.OrganizationID) ([]Rule, error)
	Find(ctx context.Context, organization identity.OrganizationID, id RuleID) (Rule, error)
	// FindForUpdate locks the row until the transaction ends.
	FindForUpdate(ctx context.Context, organization identity.OrganizationID, id RuleID) (Rule, error)
	Count(ctx context.Context, organization identity.OrganizationID) (int, error)
	// LockRules takes a transaction-scoped lock on the organization's
	// rulebook, so two writers cannot interleave (the limit stays true).
	LockRules(ctx context.Context, organization identity.OrganizationID) error
	Create(ctx context.Context, rule Rule) error
	// Update stores the rule's editable fields and moves its version up by one.
	Update(ctx context.Context, rule Rule) error
	// Delete removes a rule that is not built-in; NotFound otherwise.
	Delete(ctx context.Context, organization identity.OrganizationID, id RuleID) error
	// SeedBuiltins inserts the rules whose builtin key the organization lacks
	// (idempotent: ON CONFLICT DO NOTHING) and returns how many it added.
	SeedBuiltins(ctx context.Context, rules []Rule) (int, error)
}

// Engine is the expression language (the policycel adapter: CEL). The domain
// and the service know only this port.
type Engine interface {
	// Compile checks the expression: it must compile against Input and
	// produce a bool. A failure is an *InvalidExpression.
	Compile(expression string) error
	// Matches evaluates the rule's expression over the input. The cost is
	// bounded; an error (cost over the limit, a missing field, a timeout)
	// is the caller's to treat as a failed rule.
	Matches(ctx context.Context, rule Rule, input Input) (bool, error)
}
