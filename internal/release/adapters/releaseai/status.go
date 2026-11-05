// Package releaseai is the release.AIStatus adapter on the ai domain: release
// asks how an app's AI stands through ai's Queries port only (never aisvc),
// and the release domain itself never imports ai (architecture test). It is
// the only way release reaches ai.
package releaseai

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Status struct{ queries ai.Queries }

func New(q ai.Queries) *Status { return &Status{queries: q} }

var _ release.AIStatus = (*Status)(nil)

func (s *Status) AIState(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (release.AIState, error) {
	v, err := s.queries.Get(ctx, org, id, env)
	if err != nil {
		return release.AIState{}, err
	}
	return release.AIState{Available: v.Available, Status: v.Status, Warning: v.Warning != ""}, nil
}
