package tunnel

import (
	"context"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"google.golang.org/protobuf/proto"
)

// Scans is what the tunnel reports of the vulnerability scans (3c). Builds
// that also implement it have their scans reported; an agent without it
// reports none (and does not list the capability).
type Scans interface {
	// Scan is the outcome of the build's scan; nil while it has not ended
	// (or the cluster has none).
	Scan(ctx context.Context, buildID string) (*fleetv1.ScanStatus, error)
	// Scans are the outcomes of every scan that ended.
	Scans(ctx context.Context) ([]*fleetv1.ScanStatus, error)
}

// maxReportScans bounds a ScanReport (matches the gateway's limit,
// fleet.MaxReportScans).
const maxReportScans = 200

// ScanChanged marks a build whose scan may have ended (the agent's watch on
// scan Jobs calls it).
func (c *Client) ScanChanged(buildID string) {
	c.mu.Lock()
	c.scansChanged[buildID] = true
	c.mu.Unlock()
	c.poke()
}

// reportScans sends the scans that ended: all of them (on connect and on the
// periodic full report) or those that changed and differ from what was last
// sent.
func (c *Client) reportScans(ctx context.Context, send func(*fleetv1.AgentMessage) error, all bool) error {
	sc, ok := c.o.Builds.(Scans)
	if !ok {
		return nil
	}
	var out []*fleetv1.ScanStatus
	if all {
		list, err := sc.Scans(ctx)
		if err != nil {
			c.o.Log.Info("read scans", "err", err)
		}
		out = list
		c.mu.Lock()
		c.scansChanged = map[string]bool{}
		c.scansSent = map[string]*fleetv1.ScanStatus{}
		for _, s := range out {
			c.scansSent[s.GetBuildId()] = s
		}
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		changed := c.scansChanged
		c.scansChanged = map[string]bool{}
		c.mu.Unlock()
		for id := range changed {
			s, err := sc.Scan(ctx, id)
			if err != nil {
				c.o.Log.Info("read scan", "build", id, "err", err)
				time.AfterFunc(time.Second, func() { c.ScanChanged(id) })
				continue
			}
			c.mu.Lock()
			same := s == nil || proto.Equal(c.scansSent[id], s)
			if !same {
				c.scansSent[id] = s
			}
			c.mu.Unlock()
			if !same {
				out = append(out, s)
			}
		}
	}
	for len(out) > 0 {
		n := min(len(out), maxReportScans)
		if err := send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_ScanReport{ScanReport: &fleetv1.ScanReport{Scans: out[:n]}}}); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}
