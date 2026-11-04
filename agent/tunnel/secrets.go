package tunnel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/secretbox"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

// Secrets stores the managed secrets the control plane delivers (agent/secretstore).
type Secrets interface {
	// Receive opens the sealed value with key (bound to cluster) and stores
	// it; nil also when the cluster already had that version or a newer one.
	// The error must not carry the value.
	Receive(ctx context.Context, key secretbox.Key, cluster string, s *fleetv1.Secret) error
}

// secretTimeout bounds storing one delivery.
const secretTimeout = 30 * time.Second

// maxAckError bounds the error an ack carries.
const maxAckError = 500

// loadSecretKey reads the cluster's secret key from the store, making and
// keeping it the first time. The first key kept wins (a replica racing this
// one reads it back), so values sealed to a published key stay openable.
func (c *Client) loadSecretKey(ctx context.Context) (secretbox.Key, error) {
	raw, err := c.o.Store.SecretKey(ctx)
	if err != nil {
		return secretbox.Key{}, err
	}
	if len(raw) > 0 {
		if k, err := secretbox.ParseKey(raw); err == nil {
			return k, nil
		}
		// Not replacing a key we cannot read: values sealed to it would be
		// lost silently. An operator must look.
		return secretbox.Key{}, errors.New("the secret key in the agent's secret is not valid; remove " + KeySecret + " from it to make a new one (managed secrets will have to be loaded again)")
	}
	k, err := secretbox.GenerateKey()
	if err != nil {
		return secretbox.Key{}, err
	}
	b, err := k.Bytes()
	if err != nil {
		return secretbox.Key{}, err
	}
	if err := c.o.Store.SaveSecretKey(ctx, b); err != nil {
		return secretbox.Key{}, fmt.Errorf("keep the new secret key: %w", err)
	}
	// Read back: if another replica won the race its key is the one.
	if raw, err = c.o.Store.SecretKey(ctx); err != nil {
		return secretbox.Key{}, err
	}
	stored, err := secretbox.ParseKey(raw)
	if err != nil {
		return secretbox.Key{}, errors.New("the secret key kept in the agent's secret is not valid")
	}
	return stored, nil
}

// secretSession is what one tunnel needs to take managed secrets: the key
// (have is false when the cluster cannot take them).
type secretSession struct {
	key  secretbox.Key
	have bool
}

// newSecretSession loads the key for a stream when this agent stores secrets.
// A key it cannot load only costs the capability (apps without managed
// secrets keep working).
func (c *Client) newSecretSession(ctx context.Context) secretSession {
	if c.o.Secrets == nil {
		return secretSession{}
	}
	k, err := c.loadSecretKey(ctx)
	if err != nil {
		c.o.Log.Error(err, "no secret key: this cluster cannot take managed secrets for now")
		return secretSession{}
	}
	return secretSession{key: k, have: true}
}

// capabilities adds the secret key to what the cluster offers.
func (s secretSession) capabilities(caps *fleetv1.Capabilities) *fleetv1.Capabilities {
	if caps == nil {
		caps = &fleetv1.Capabilities{}
	}
	if s.have {
		caps.SecretKey = &fleetv1.SecretKey{KeyId: s.key.ID(), PublicKey: s.key.Public(), Suite: secretbox.Suite}
	}
	return caps
}

// receiveSecret stores one delivered secret and answers with an ack either
// way. The value is never logged.
func (c *Client) receiveSecret(ctx context.Context, ss secretSession, cluster string, s *fleetv1.Secret, send func(*fleetv1.AgentMessage) error) error {
	var failure string
	switch {
	case c.o.Secrets == nil || !ss.have:
		failure = "this cluster cannot take managed secrets"
	default:
		sctx, cancel := context.WithTimeout(ctx, secretTimeout)
		err := c.o.Secrets.Receive(sctx, ss.key, cluster, s)
		cancel()
		if err != nil {
			failure = err.Error()
			c.o.Log.Info("secret not stored", "delivery", s.GetDeliveryId(), "app", s.GetApp(), "secret", s.GetName(), "err", failure)
		}
	}
	if r := []rune(failure); len(r) > maxAckError {
		failure = string(r[:maxAckError])
	}
	return send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_SecretAck{SecretAck: &fleetv1.SecretAck{DeliveryId: s.GetDeliveryId(), Error: failure}}})
}
