package fleetsvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

var _ fleet.Resources = (*Service)(nil)

// ApplyResource places the environment on first use, then stores the desired
// resource and signals its cluster's gateway, in the caller's transaction (the
// cluster is locked against revocation and moves until it commits). A
// cluster that does not list the kind refuses a new or changed resource; a
// deletion is always sent (the cluster may have lost the capability since,
// and what it holds must still be removable).
func (s *Service) ApplyResource(ctx context.Context, d fleet.DesiredResource) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, placed, err := s.placement(ctx, d.Organization, d.Project, d.Environment)
		if err != nil {
			return err
		}
		if !d.Deleted {
			if err := c.Agent.Capabilities.CheckResources([]fleet.ResourceRef{{Name: d.Name, Kind: d.Kind}}); err != nil {
				return err
			}
		}
		d.Cluster = c.ID
		changed, err := s.repo.PutResource(ctx, d)
		if err != nil || !changed {
			return err
		}
		return s.events.Publish(ctx, fleet.NewResourceApplied(d.Organization, c, d, placed))
	})
}

func (s *Service) ResourceStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (fleet.ResourceStatus, error) {
	st, err := s.repo.FindResourceStatus(ctx, organization, environment, name)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.ResourceStatus{Environment: environment, Name: name}, nil
	}
	return st, err
}

// ResourceKinds answers fleet.Resources.ResourceKinds without placing anything.
func (s *Service) ResourceKinds(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]resourcekind.Kind, error) {
	var c fleet.Cluster
	id, err := s.repo.FindPlacement(ctx, organization, environment)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		if c, err = s.repo.FindDefaultCluster(ctx, organization); errx.IsType(err, errx.TypeNotFound) {
			return []resourcekind.Kind{}, nil
		}
	case err == nil:
		c, err = s.repo.FindCluster(ctx, organization, id)
	}
	if err != nil {
		return nil, err
	}
	out := []resourcekind.Kind{}
	if c.Status == fleet.StatusRevoked {
		return out, nil
	}
	for _, k := range resourcekind.Kinds {
		if c.Agent.Capabilities.CanProvision(k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// ---- Agents -----------------------------------------------------------

func (s *Service) DesiredResources(ctx context.Context, agent fleet.AgentRef) ([]fleet.DesiredResource, error) {
	return s.repo.ListResources(ctx, agent.Organization, agent.Cluster)
}

func (s *Service) DesiredResource(ctx context.Context, agent fleet.AgentRef, environment project.EnvironmentID, name string) (fleet.DesiredResource, bool, error) {
	d, err := s.repo.FindResource(ctx, agent.Organization, agent.Cluster, environment, name)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.DesiredResource{}, false, nil
	}
	return d, err == nil, err
}

// ReportResources stores what the agent observed. An invalid status is
// dropped (and the report answered with a validation error naming how many)
// while the valid ones are kept. A deleted resource reported gone is
// forgotten and announced (cluster.resource_removed) in the same
// transaction, so the resource domain never misses it.
func (s *Service) ReportResources(ctx context.Context, agent fleet.AgentRef, session fleet.Session, report fleet.ResourceReport) error {
	if err := report.Validate(); err != nil {
		return err
	}
	valid, invalid := report.Valid()
	live := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		ok, removed, err := s.repo.ObserveResources(ctx, agent.Organization, agent.Cluster, session, valid)
		if err != nil || !ok {
			return err
		}
		live = true
		if len(removed) == 0 {
			return nil
		}
		c, err := s.repo.FindCluster(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		for _, d := range removed {
			if err := s.events.Publish(ctx, fleet.NewResourceRemoved(agent.Organization, c, d)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !live {
		return s.lost(ctx, agent) // a superseded or revoked tunnel writes nothing
	}
	return invalid
}
