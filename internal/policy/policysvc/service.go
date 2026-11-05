// Package policysvc implements policy.Commands, policy.Queries and
// policy.Evaluator over the rule repository and the expression engine.
package policysvc

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type Service struct {
	repo   policy.Repository
	engine policy.Engine
	tx     txn.Runner
	events event.Publisher

	// seeded remembers the organizations whose built-ins this process has
	// already ensured, so the hot path (every governed action) does not
	// write. A restart forgets it: the seeding is idempotent.
	seeded sync.Map
}

func New(repo policy.Repository, engine policy.Engine, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, engine: engine, tx: tx, events: events}
}

var (
	_ policy.Commands  = (*Service)(nil)
	_ policy.Queries   = (*Service)(nil)
	_ policy.Evaluator = (*Service)(nil)
)

// ensureBuiltins inserts the built-in rules the organization lacks, once per
// process and organization (lazy seeding: docs/policy.md). The insert is
// ON CONFLICT DO NOTHING on (organization, builtin key), so concurrent first
// reads and several replicas converge on one set. It writes no event: the
// built-ins are the product, not something a person did.
func (s *Service) ensureBuiltins(ctx context.Context, organization identity.OrganizationID) error {
	if _, ok := s.seeded.Load(organization); ok {
		return nil
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		_, err := s.repo.SeedBuiltins(ctx, policy.Builtins(organization, time.Now().UTC()))
		return err
	})
	if err != nil {
		return err
	}
	// Inside a caller's transaction the seeding is only as durable as that
	// transaction: remember it only when it committed on its own.
	if _, joined := txn.Tx(ctx); !joined {
		s.seeded.Store(organization, true)
	}
	return nil
}

func (s *Service) List(ctx context.Context, organization identity.OrganizationID) ([]policy.Rule, error) {
	if err := s.ensureBuiltins(ctx, organization); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, organization)
}

func (s *Service) Find(ctx context.Context, organization identity.OrganizationID, id policy.RuleID) (policy.Rule, error) {
	if err := s.ensureBuiltins(ctx, organization); err != nil {
		return policy.Rule{}, err
	}
	return s.repo.Find(ctx, organization, id)
}

// check validates a draft: its fields, then that the expression compiles to a
// bool (POLICY_INVALID with the compiler's message otherwise).
func (s *Service) check(d policy.Draft) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := s.engine.Compile(d.Expression); err != nil {
		var invalid *policy.InvalidExpression
		if errors.As(err, &invalid) {
			return policy.InvalidError(invalid.Message)
		}
		return err
	}
	return nil
}

func (s *Service) Create(ctx context.Context, organization identity.OrganizationID, input policy.Create) (policy.Rule, error) {
	input.Draft = input.Normalize()
	if input.CreatedBy.IsZero() {
		return policy.Rule{}, errx.Validation("creator is required")
	}
	if err := s.check(input.Draft); err != nil {
		return policy.Rule{}, err
	}
	if err := s.ensureBuiltins(ctx, organization); err != nil {
		return policy.Rule{}, err
	}
	now := time.Now().UTC()
	rule := policy.Rule{ID: policy.NewRuleID(), Organization: organization, Name: input.Name,
		Description: input.Description, Expression: input.Expression, Effect: input.Effect,
		Approvers: input.Approvers, Enabled: input.On(), Version: 1,
		CreatedBy: input.CreatedBy, UpdatedBy: input.CreatedBy, CreatedAt: now, UpdatedAt: now}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRules(ctx, organization); err != nil {
			return err
		}
		n, err := s.repo.Count(ctx, organization)
		if err != nil {
			return err
		}
		if n >= policy.MaxRules {
			return errx.Business("La organización ya tiene el máximo de reglas de TI (100).").WithCode(policy.CodeTooMany)
		}
		if err := s.repo.Create(ctx, rule); err != nil {
			return err
		}
		return s.events.Publish(ctx, policy.NewRuleCreated(organization, input.CreatedBy, rule))
	})
	if err != nil {
		return policy.Rule{}, err
	}
	return rule, nil
}

// Replace overwrites a rule (PUT). A built-in rule takes only its `enabled`
// switch; an edit that changes nothing writes nothing (no version, no event).
func (s *Service) Replace(ctx context.Context, organization identity.OrganizationID, id policy.RuleID, input policy.Replace) (policy.Rule, error) {
	input.Draft = input.Normalize()
	if input.UpdatedBy.IsZero() {
		return policy.Rule{}, errx.Validation("editor is required")
	}
	if err := s.check(input.Draft); err != nil {
		return policy.Rule{}, err
	}
	if err := s.ensureBuiltins(ctx, organization); err != nil {
		return policy.Rule{}, err
	}
	var out policy.Rule
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		current, err := s.repo.FindForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if current.IsBuiltin() && !input.Same(current) {
			return policy.BuiltinError()
		}
		if input.Same(current) && input.On() == current.Enabled {
			out = current
			return nil
		}
		next := current
		next.Name, next.Description, next.Expression = input.Name, input.Description, input.Expression
		next.Effect, next.Approvers, next.Enabled = input.Effect, input.Approvers, input.On()
		next.UpdatedBy, next.UpdatedAt = input.UpdatedBy, time.Now().UTC()
		if err := s.repo.Update(ctx, next); err != nil {
			return err
		}
		next.Version++
		out = next
		return s.events.Publish(ctx, policy.NewRuleUpdated(organization, input.UpdatedBy, next))
	})
	return out, err
}

func (s *Service) Delete(ctx context.Context, organization identity.OrganizationID, id policy.RuleID, input policy.Delete) error {
	if input.DeletedBy.IsZero() {
		return errx.Validation("deleting user is required")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		current, err := s.repo.FindForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if current.IsBuiltin() {
			return policy.BuiltinError()
		}
		if err := s.repo.Delete(ctx, organization, id); err != nil {
			return err
		}
		return s.events.Publish(ctx, policy.NewRuleDeleted(organization, input.DeletedBy, current))
	})
}

// Evaluate is the verdict over every enabled rule.
func (s *Service) Evaluate(ctx context.Context, organization identity.OrganizationID, input policy.Input) (policy.Decision, error) {
	if err := input.Validate(); err != nil {
		return policy.Decision{}, err
	}
	e, err := s.Explain(ctx, organization, input, nil)
	return e.Decision, err
}

// candidateName names an unsaved rule in a dry run that gave it no name.
const candidateName = "Regla de prueba"

// candidateRule is the unsaved rule as the engine runs it: enabled, with a
// fresh ID (so it can never hit another rule's compiled program in the cache).
// The draft is checked like a save would (422 POLICY_INVALID when the
// expression does not compile).
func (s *Service) candidateRule(organization identity.OrganizationID, c policy.Candidate) (policy.Rule, error) {
	d := c.Draft.Normalize()
	if d.Name == "" {
		d.Name = candidateName
	}
	if err := s.check(d); err != nil {
		return policy.Rule{}, err
	}
	return policy.Rule{ID: policy.NewRuleID(), Organization: organization, Name: d.Name, Description: d.Description,
		Expression: d.Expression, Effect: d.Effect, Approvers: d.Approvers, Enabled: true}, nil
}

// Explain evaluates every enabled rule over the input. The semantics
// (docs/policy.md): deny > approval > allow; no match is allow; a rule that
// cannot be evaluated (cost limit, timeout, a failure of the engine) fails
// closed at its own strength (a deny rule denies, an approval rule asks for
// approval, an allow rule is ignored), with its reason. A rule whose stored
// expression no longer compiles fails the same way.
//
// A candidate (the dry run of an unsaved rule) replaces the stored rule it
// names by ID, or is added to the rules, and is judged like the rest.
func (s *Service) Explain(ctx context.Context, organization identity.OrganizationID, input policy.Input, candidate *policy.Candidate) (policy.Evaluation, error) {
	if err := input.Validate(); err != nil {
		return policy.Evaluation{}, err
	}
	rules, err := s.List(ctx, organization)
	if err != nil {
		return policy.Evaluation{}, err
	}
	if candidate != nil {
		try, err := s.candidateRule(organization, *candidate)
		if err != nil {
			return policy.Evaluation{}, err
		}
		if !candidate.Replaces.IsZero() {
			rules = slices.DeleteFunc(rules, func(r policy.Rule) bool { return r.ID == candidate.Replaces })
		}
		rules = append(rules, try)
	}
	results := make([]policy.RuleResult, 0, len(rules))
	approvers := make(map[policy.RuleID]string, len(rules))
	for _, rule := range rules {
		res := policy.RuleResult{Rule: rule.Ref(), Effect: rule.Effect, Enabled: rule.Enabled}
		approvers[rule.ID] = rule.Approvers
		if rule.Enabled {
			matched, err := s.engine.Matches(ctx, rule, input)
			if err != nil {
				slog.Warn("policy rule could not be evaluated: it fails closed at its own strength", "rule", rule.ID, "organization", organization, "error", err)
				res.Error = err.Error()
			}
			res.Matched = matched && err == nil
		}
		results = append(results, res)
	}
	return policy.Evaluation{Decision: policy.Decide(results, approvers), Results: results}, nil
}
