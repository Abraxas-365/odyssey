// Package resourceproject is the project.TierGuard adapter on the resource
// domain: before an app changes tier, project asks which of its resources
// holding data the catalog does not offer there, through resource's Queries
// port only.
package resourceproject

import (
	"context"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
)

type TierGuard struct{ resources resource.Queries }

func New(q resource.Queries) *TierGuard { return &TierGuard{resources: q} }

var _ project.TierGuard = (*TierGuard)(nil)

// Stranded names them as the person reads them: by name, once (the same
// name in two environments is one thing to fix in the catalog: its recipe).
func (g *TierGuard) Stranded(ctx context.Context, organization identity.OrganizationID, id project.ID, tier project.Tier) ([]string, error) {
	rows, err := g.resources.Stranded(ctx, organization, id, tier)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
