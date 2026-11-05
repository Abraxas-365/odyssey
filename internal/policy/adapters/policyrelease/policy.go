// Package policyrelease is the release.Policy adapter on the policy domain:
// release asks IT's rules through its own port and never imports policy.
package policyrelease

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Policy struct{ evaluator policy.Evaluator }

func New(e policy.Evaluator) *Policy { return &Policy{evaluator: e} }

var _ release.Policy = (*Policy)(nil)

func (p *Policy) Check(ctx context.Context, organization identity.OrganizationID, request gate.Request) (gate.Verdict, error) {
	return policy.Ask(ctx, p.evaluator, organization, request)
}
