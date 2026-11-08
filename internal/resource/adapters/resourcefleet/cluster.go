// Package resourcefleet is the resource.Cluster adapter on the fleet domain:
// resources run on the environment's enrolled cluster through fleet's
// Resources port only (never fleetsvc). It is the only way resource reaches
// the fleet.
package resourcefleet

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

type Cluster struct{ fleet fleet.Resources }

func New(f fleet.Resources) *Cluster { return &Cluster{fleet: f} }

var _ resource.Cluster = (*Cluster)(nil)

func (c *Cluster) Apply(ctx context.Context, d resource.Desired) error {
	return c.fleet.ApplyResource(ctx, fleet.DesiredResource{Organization: d.Organization, Project: d.Project, Environment: d.Environment,
		Name: d.Name, Kind: d.Kind, Generation: d.Generation, StorageGi: d.StorageGi, Recipe: d.Recipe, Spec: d.Spec, Deleted: d.Deleted})
}

func (c *Cluster) Observed(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) (resource.Observed, error) {
	s, err := c.fleet.ResourceStatus(ctx, org, env, name)
	if err != nil {
		return resource.Observed{}, err
	}
	return resource.Observed{Phase: resource.Phase(s.Phase), Message: s.Message}, nil
}

func (c *Cluster) Kinds(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID) ([]resourcekind.Kind, error) {
	return c.fleet.ResourceKinds(ctx, org, env)
}

// Capabilities are every resource capability the environment's cluster
// announces (fleet's Capabilities.Resources).
func (c *Cluster) Capabilities(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID) ([]string, error) {
	return c.fleet.ResourceCapabilities(ctx, org, env)
}
