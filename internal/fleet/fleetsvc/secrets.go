package fleetsvc

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// SecretTarget answers fleet.Dispatch.SecretTarget without placing anything
// (like CanProtect).
func (s *Service) SecretTarget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (fleet.SecretTarget, error) {
	var c fleet.Cluster
	id, err := s.repo.FindPlacement(ctx, organization, environment)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		if c, err = s.repo.FindDefaultCluster(ctx, organization); errx.IsType(err, errx.TypeNotFound) {
			return fleet.SecretTarget{}, nil
		}
	case err == nil:
		c, err = s.repo.FindCluster(ctx, organization, id)
	}
	if err != nil {
		return fleet.SecretTarget{}, err
	}
	if c.Status == fleet.StatusRevoked {
		return fleet.SecretTarget{Cluster: c.ID, Revoked: true, Stores: []string{}, Failures: []fleet.SecretFailure{}}, nil
	}
	failures, err := s.repo.ListSecretFailures(ctx, organization, environment)
	if err != nil {
		return fleet.SecretTarget{}, err
	}
	caps := c.Agent.Capabilities
	stores := caps.SecretStores
	if stores == nil {
		stores = []string{}
	}
	return fleet.SecretTarget{Cluster: c.ID, Key: caps.SecretKey, External: caps.ExternalSecrets(), Stores: stores, Failures: failures, AI: caps.CanUseAI(),
		ScopedSettings: caps.CanScopeSettings()}, nil
}

// DeliverSecret places the environment (like Apply), checks the value was
// sealed to its cluster's current key, and stores it for the gateway. The
// cluster stays locked against revocation and moves until the caller
// commits, so a value never waits for a cluster its environment just left.
func (s *Service) DeliverSecret(ctx context.Context, d fleet.SecretDelivery) error {
	if d.ID.IsZero() {
		d.ID = fleet.NewSecretDeliveryID()
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, placed, err := s.placement(ctx, d.Organization, d.Project, d.Environment)
		if err != nil {
			return err
		}
		key := c.Agent.Capabilities.SecretKey
		if key == nil {
			return errx.Business(fleet.SecretsUnavailableMessage).WithCode(fleet.CodeSecretsUnavailable)
		}
		if key.KeyID != d.KeyID {
			return errx.Business("El clúster de este entorno cambió su llave mientras escribías: vuelve a cargar el valor.").
				WithCode(fleet.CodeSecretKeyChanged)
		}
		d.Cluster = c.ID
		d.CreatedAt = s.opts.Now().UTC() // the TTL is judged by the same clock
		if err := d.Validate(); err != nil {
			return err
		}
		changed, err := s.repo.PutSecretDelivery(ctx, d)
		if err != nil {
			return err
		}
		if !changed {
			// A newer version of this secret is already on its way: this one
			// would be dropped, so say so instead of reporting it sent.
			return errx.Conflict("someone loaded a newer value of this secret meanwhile: reload the page").
				WithCode(fleet.CodeSecretVersionConflict)
		}
		return s.events.Publish(ctx, fleet.NewSecretSent(d.Organization, d.CreatedBy, c, d, placed))
	})
}

// DesiredSecrets are the values waiting for the agent's cluster. The ones
// that waited longer than SecretDeliveryTTL are dropped here (published as
// failed: the person loads them again).
func (s *Service) DesiredSecrets(ctx context.Context, agent fleet.AgentRef) ([]fleet.SecretDelivery, error) {
	cutoff := s.opts.Now().Add(-fleet.SecretDeliveryTTL)
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		expired, err := s.repo.DeleteSecretDeliveries(ctx, agent.Organization, agent.Cluster, cutoff)
		if err != nil {
			return err
		}
		for _, d := range expired {
			e, err := s.settle(ctx, d, expiredMessage)
			if err != nil {
				return err
			}
			if err := s.events.Publish(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.repo.ListSecretDeliveries(ctx, agent.Organization, agent.Cluster, cutoff)
}

// sweepSecretBatch bounds the deliveries one sweep transaction drops.
const sweepSecretBatch = 500

// SweepSecrets drops the expired deliveries of every organization, in
// batches, each reported as failed.
func (s *Service) SweepSecrets(ctx context.Context) (int, error) {
	cutoff := s.opts.Now().Add(-fleet.SecretDeliveryTTL)
	total := 0
	for {
		var n int
		err := s.tx.Do(ctx, func(ctx context.Context) error {
			expired, err := s.repo.ExpireSecretDeliveries(ctx, cutoff, sweepSecretBatch)
			if err != nil {
				return err
			}
			for _, d := range expired {
				e, err := s.settle(ctx, d, expiredMessage)
				if err != nil {
					return err
				}
				if err := s.events.Publish(ctx, e); err != nil {
					return err
				}
			}
			n = len(expired)
			return nil
		})
		total += n
		if err != nil || n < sweepSecretBatch {
			return total, err
		}
	}
}

func (s *Service) DesiredSecret(ctx context.Context, agent fleet.AgentRef, id fleet.SecretDeliveryID) (fleet.SecretDelivery, bool, error) {
	d, err := s.repo.FindSecretDelivery(ctx, agent.Organization, agent.Cluster, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.SecretDelivery{}, false, nil
	}
	return d, err == nil, err
}

// AckSecret deletes the delivery and publishes how it went. An ack for a
// delivery that is gone (a newer version replaced it, it was answered
// already, or this tunnel is no longer the cluster's) changes nothing: the
// heartbeat ends a superseded tunnel, and the current one sends the value
// again if it still waits.
func (s *Service) AckSecret(ctx context.Context, agent fleet.AgentRef, session fleet.Session, ack fleet.SecretAck) error {
	failure := strings.TrimSpace(ack.Error)
	if utf8.RuneCountInString(failure) > fleet.MaxAckError || text.HasForbiddenControl(failure) {
		failure = "el clúster no pudo guardarlo"
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		d, found, err := s.repo.DeleteSecretDelivery(ctx, agent.Organization, agent.Cluster, session, ack.Delivery)
		if err != nil || !found {
			return err
		}
		e, err := s.settle(ctx, d, failure)
		if err != nil {
			return err
		}
		return s.events.Publish(ctx, e)
	})
}

// expiredMessage is the failure of a delivery nobody took in time.
const expiredMessage = "nadie lo recibió a tiempo (el clúster estuvo desconectado)"

// settle records how a delivery ended (stored: its older failures go;
// failed: the console shows it until a newer value is sent) and returns the
// event its caller publishes.
func (s *Service) settle(ctx context.Context, d fleet.SecretDelivery, failure string) (event.Event, error) {
	var err error
	if failure == "" {
		err = s.repo.ClearSecretFailure(ctx, d.Organization, d.Environment, d.App, d.Name, d.Version)
	} else {
		err = s.repo.RecordSecretFailure(ctx, d.Failure(failure))
	}
	if err != nil {
		return event.Event{}, err
	}
	return fleet.NewSecretAcked(d.Organization, d, failure), nil
}
