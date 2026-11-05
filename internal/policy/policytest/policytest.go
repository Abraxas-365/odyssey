// Package policytest has an in-memory policy.Repository and a scriptable
// policy.Engine for unit tests of the service and the HTTP adapter.
package policytest

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
)

// Repo is a policy.Repository in memory, scoped by organization.
type Repo struct {
	mu   sync.Mutex
	rows map[policy.RuleID]policy.Rule
}

func NewRepo() *Repo { return &Repo{rows: map[policy.RuleID]policy.Rule{}} }

var _ policy.Repository = (*Repo)(nil)

func notFound() error { return errx.NotFound("rule not found") }

func (m *Repo) List(_ context.Context, org identity.OrganizationID) ([]policy.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []policy.Rule{}
	for _, r := range m.rows {
		if r.Organization == org {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsBuiltin() != out[j].IsBuiltin() {
			return out[i].IsBuiltin()
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (m *Repo) Find(_ context.Context, org identity.OrganizationID, id policy.RuleID) (policy.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok && r.Organization == org {
		return r, nil
	}
	return policy.Rule{}, notFound()
}

func (m *Repo) FindForUpdate(ctx context.Context, org identity.OrganizationID, id policy.RuleID) (policy.Rule, error) {
	return m.Find(ctx, org, id)
}

func (m *Repo) Count(ctx context.Context, org identity.OrganizationID) (int, error) {
	rules, _ := m.List(ctx, org)
	return len(rules), nil
}

func (m *Repo) LockRules(context.Context, identity.OrganizationID) error { return nil }

func (m *Repo) Create(_ context.Context, r policy.Rule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[r.ID] = r
	return nil
}

func (m *Repo) Update(_ context.Context, r policy.Rule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[r.ID]
	if !ok || cur.Organization != r.Organization {
		return notFound()
	}
	r.Version = cur.Version + 1
	r.CreatedAt, r.CreatedBy, r.Builtin = cur.CreatedAt, cur.CreatedBy, cur.Builtin
	m.rows[r.ID] = r
	return nil
}

func (m *Repo) Delete(_ context.Context, org identity.OrganizationID, id policy.RuleID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; !ok || r.Organization != org || r.IsBuiltin() {
		return notFound()
	}
	delete(m.rows, id)
	return nil
}

func (m *Repo) SeedBuiltins(_ context.Context, rules []policy.Rule) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	added := 0
	for _, r := range rules {
		exists := false
		for _, x := range m.rows {
			if x.Organization == r.Organization && x.Builtin != nil && *x.Builtin == *r.Builtin {
				exists = true
			}
		}
		if !exists {
			m.rows[r.ID] = r
			added++
		}
	}
	return added, nil
}

// Engine is a policy.Engine whose answers a test scripts by expression text:
// Matching[expr] is the result; Failing[expr] is an evaluation error;
// Invalid[expr] is a compile error (text: its message). Unscripted
// expressions do not match.
type Engine struct {
	Matching map[string]bool
	Failing  map[string]bool
	Invalid  map[string]string
}

var _ policy.Engine = (*Engine)(nil)

func (e *Engine) Compile(expr string) error {
	if msg, ok := e.Invalid[expr]; ok {
		return &policy.InvalidExpression{Message: msg}
	}
	return nil
}

func (e *Engine) Matches(_ context.Context, r policy.Rule, _ policy.Input) (bool, error) {
	if e.Failing[r.Expression] {
		return false, errors.New("evaluation failed")
	}
	return e.Matching[r.Expression], nil
}
