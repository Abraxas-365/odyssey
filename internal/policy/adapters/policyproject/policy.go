// Package policyproject is the project.Policy adapter on the policy domain:
// project asks IT's rules (may the app's services change like this?) through
// its own port and never imports policy.
package policyproject

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type Policy struct{ evaluator policy.Evaluator }

func New(e policy.Evaluator) *Policy { return &Policy{evaluator: e} }

var _ project.Policy = (*Policy)(nil)

func (p *Policy) Check(ctx context.Context, organization identity.OrganizationID, request gate.Request) (gate.Verdict, error) {
	return policy.Ask(ctx, p.evaluator, organization, request)
}
