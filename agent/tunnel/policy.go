package tunnel

import (
	"context"
	"slices"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

// CapabilityEgress is listed among the APIs of Hello when this agent enforces
// the cluster's egress policy (fleet.CapabilityEgress is the control plane's
// copy of the string).
const CapabilityEgress = "odicea.egress"

// Policy keeps and enforces the cluster policy the control plane sends
// (agent/egress). Optional: without it the agent takes none and does not
// list CapabilityEgress.
type Policy interface {
	// ReceivePolicy enforces the policy; an error is reported in the ack
	// (the policy in force before stays when it was refused).
	ReceivePolicy(ctx context.Context, p *fleetv1.ClusterPolicy) error
	// Status is the generation in force (0: none received), why it is not
	// fully applied (empty: it is) and what TI should review although it is.
	// The periodic report sends it again: the periodic check may have fixed
	// (or broken) what the last ack said, and a node joining changes the
	// warnings.
	Status() (generation int64, failure string, warnings []string)
}

// policyTimeout bounds applying one policy (it touches every environment
// namespace).
const policyTimeout = 2 * time.Minute

// withEgress adds CapabilityEgress to what the cluster offers.
func (c *Client) withEgress(caps *fleetv1.Capabilities) *fleetv1.Capabilities {
	if c.o.Policy == nil {
		return caps
	}
	if caps == nil {
		caps = &fleetv1.Capabilities{}
	}
	if !slices.Contains(caps.Apis, CapabilityEgress) {
		caps.Apis = append(caps.Apis, CapabilityEgress)
		slices.Sort(caps.Apis)
	}
	return caps
}

// receivePolicy enforces a delivered policy and answers with an ack either
// way. It reports whether m was a policy.
func (c *Client) receivePolicy(ctx context.Context, m *fleetv1.ControlMessage, send func(*fleetv1.AgentMessage) error) (bool, error) {
	p := m.GetClusterPolicy()
	if p == nil {
		return false, nil
	}
	var failure string
	if c.o.Policy == nil {
		failure = "this agent does not enforce a cluster policy"
	} else {
		pctx, cancel := context.WithTimeout(ctx, policyTimeout)
		err := c.o.Policy.ReceivePolicy(pctx, p)
		cancel()
		if err != nil {
			failure = err.Error()
			c.o.Log.Info("cluster policy not applied", "generation", p.GetGeneration(), "err", failure)
		}
	}
	if r := []rune(failure); len(r) > maxAckError {
		failure = string(r[:maxAckError])
	}
	var warnings []string
	if c.o.Policy != nil && failure == "" {
		_, _, warnings = c.o.Policy.Status()
	}
	return true, send(policyAck(p.GetGeneration(), failure, warnings))
}

// maxWarnings and maxWarning bound what an ack carries.
const (
	maxWarnings = 10
	maxWarning  = 300
)

func policyAck(generation int64, failure string, warnings []string) *fleetv1.AgentMessage {
	if len(warnings) > maxWarnings {
		warnings = warnings[:maxWarnings]
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if r := []rune(w); len(r) > maxWarning {
			w = string(r[:maxWarning])
		}
		out = append(out, w)
	}
	return &fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_PolicyAck{PolicyAck: &fleetv1.PolicyAck{Generation: generation, Error: failure, Warnings: out}}}
}

// reportPolicy sends the state of the policy in force again (every full
// report): the periodic check of the agent may have changed it since its last
// ack. Nothing before the first policy.
func (c *Client) reportPolicy(send func(*fleetv1.AgentMessage) error) error {
	if c.o.Policy == nil {
		return nil
	}
	generation, failure, warnings := c.o.Policy.Status()
	if generation == 0 {
		return nil
	}
	if r := []rune(failure); len(r) > maxAckError {
		failure = string(r[:maxAckError])
	}
	return send(policyAck(generation, failure, warnings))
}
