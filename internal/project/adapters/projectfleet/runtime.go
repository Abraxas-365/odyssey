// Package projectfleet is the project.Runtime adapter on the fleet domain:
// project asks whether the clusters its environments run on can run an app
// made of several services, through fleet's Dispatch port only.
package projectfleet

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type Runtime struct{ fleet fleet.Dispatch }

func New(f fleet.Dispatch) *Runtime { return &Runtime{fleet: f} }

var _ project.Runtime = (*Runtime)(nil)

func (r *Runtime) CanRunServices(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (bool, error) {
	return r.fleet.CanRunServices(ctx, organization, environments)
}
