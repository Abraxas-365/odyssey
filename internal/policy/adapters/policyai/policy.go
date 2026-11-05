// Package policyai is the ai.Policy adapter on the policy domain: ai asks
// IT's rules through its own port and never imports policy.
package policyai

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
)

type Policy struct{ evaluator policy.Evaluator }

func New(e policy.Evaluator) *Policy { return &Policy{evaluator: e} }

var _ ai.Policy = (*Policy)(nil)

func (p *Policy) Check(ctx context.Context, organization identity.OrganizationID, request gate.Request) (gate.Verdict, error) {
	return policy.Ask(ctx, p.evaluator, organization, request)
}
