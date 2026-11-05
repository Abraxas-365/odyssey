package fleetsvc

import (
	"context"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/text"
	"unicode/utf8"
)

// SetEgress stores the policy, raises its generation and signals the
// cluster's gateway in one transaction. Asking for what is already stored
// changes nothing (and the audit trail stays quiet).
func (s *Service) SetEgress(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, input fleet.SetEgress) (fleet.EgressStatus, error) {
	if err := input.Validate(); err != nil {
		return fleet.EgressStatus{}, err
	}
	policy, err := input.Policy()
	if err != nil {
		return fleet.EgressStatus{}, err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.FindClusterForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if c.Status == fleet.StatusRevoked {
			return errx.Business("Este clúster está desconectado para siempre: su salida a internet ya no se puede cambiar.").WithCode(fleet.CodeClusterRevoked)
		}
		before, err := s.repo.FindEgress(ctx, organization, id)
		if err != nil {
			return err
		}
		if before.Generation > 0 && before.Same(policy) {
			return nil
		}
		generation, err := s.repo.PutEgress(ctx, organization, id, policy)
		if err != nil {
			return err
		}
		after := fleet.EgressSetting{Egress: policy, Generation: generation}
		return s.events.Publish(ctx, fleet.NewEgressChanged(organization, input.ChangedBy, c, before, after))
	})
	if err != nil {
		return fleet.EgressStatus{}, err
	}
	return s.Egress(ctx, organization, id)
}

// Egress is the stored policy with its state (Supported follows what the
// agent reported when it last connected).
func (s *Service) Egress(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.EgressStatus, error) {
	c, err := s.repo.FindCluster(ctx, organization, id)
	if err != nil {
		return fleet.EgressStatus{}, err
	}
	e, err := s.repo.FindEgress(ctx, organization, id)
	if err != nil {
		return fleet.EgressStatus{}, err
	}
	e.Supported = c.Agent.Capabilities.CanEnforceEgress()
	return e.Status(), nil
}

func (s *Service) DesiredPolicy(ctx context.Context, agent fleet.AgentRef) (fleet.Policy, error) {
	e, err := s.repo.FindEgress(ctx, agent.Organization, agent.Cluster)
	if err != nil {
		return fleet.Policy{}, err
	}
	return fleet.Policy{Generation: e.Generation, Egress: e.Egress}, nil
}

// AckPolicy records the agent's answer. It is an observation (no event): the
// change itself was published when TI made it.
func (s *Service) AckPolicy(ctx context.Context, agent fleet.AgentRef, session fleet.Session, ack fleet.PolicyAck) error {
	failure := ack.Error
	if utf8.RuneCountInString(failure) > fleet.MaxAckError || text.HasForbiddenControl(failure) {
		failure = "el clúster no pudo aplicar la política"
	}
	ack.Error = failure
	ack.Warnings = cleanWarnings(ack.Warnings)
	_, err := s.repo.ObservePolicy(ctx, agent.Organization, agent.Cluster, session, ack)
	return err
}

// cleanWarnings keeps the agent's warnings that are plain bounded text, at
// most fleet.MaxPolicyWarnings (what the console shows to TI is never
// markup or a wall of text).
func cleanWarnings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, w := range in {
		if w = strings.TrimSpace(w); w == "" || utf8.RuneCountInString(w) > fleet.MaxAckError || text.HasForbiddenControl(w) {
			continue
		}
		out = append(out, w)
		if len(out) == fleet.MaxPolicyWarnings {
			break
		}
	}
	return out
}
