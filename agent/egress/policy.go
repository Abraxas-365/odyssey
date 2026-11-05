// Package egress is the agent's enforcement of the cluster's egress policy
// (docs/egress.md): what the apps of the environments may reach, written as
// NetworkPolicies. The control plane sends ClusterPolicy (proto/fleet/v1);
// the cluster's network plugin must enforce NetworkPolicy for it to bite
// (k3s does; the control plane shows whether the agent applied it, not
// whether the plugin enforces).
package egress

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

// Modes of ClusterPolicy.egress.
const (
	ModeInternet   = "internet"
	ModeRestricted = "restricted"
)

// maxAllow bounds the allow list (the control plane stops at 100).
const maxAllow = 1000

// Policy is a validated ClusterPolicy.
type Policy struct {
	Generation int64
	// Mode is ModeInternet or ModeRestricted.
	Mode  string
	Allow []netip.Prefix
}

// Default is what an agent enforces until the control plane sends a policy.
func Default() Policy { return Policy{Mode: ModeInternet} }

// Parse validates a received policy. An empty mode is "internet". A /0 allow
// entry is refused: it would open the private ranges the internet mode keeps
// closed (that is what the mode "internet" is for).
func Parse(p *fleetv1.ClusterPolicy) (Policy, error) {
	out := Policy{Generation: p.GetGeneration(), Mode: p.GetEgress().GetMode()}
	switch out.Mode {
	case "":
		out.Mode = ModeInternet
	case ModeInternet, ModeRestricted:
	default:
		return Policy{}, fmt.Errorf("unknown egress mode %q", out.Mode)
	}
	allow := p.GetEgress().GetAllow()
	if len(allow) > maxAllow {
		return Policy{}, fmt.Errorf("%d allowed addresses, at most %d", len(allow), maxAllow)
	}
	var errs []error
	for _, s := range allow {
		pr, err := parsePrefix(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out.Allow = append(out.Allow, pr)
	}
	if err := errors.Join(errs...); err != nil {
		return Policy{}, err
	}
	slices.SortFunc(out.Allow, netip.Prefix.Compare)
	out.Allow = slices.Compact(out.Allow)
	return out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	pr, err := netip.ParsePrefix(s)
	if err != nil {
		a, aerr := netip.ParseAddr(s)
		if aerr != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a CIDR", s)
		}
		pr = netip.PrefixFrom(a, a.BitLen())
	}
	pr = netip.PrefixFrom(pr.Addr().Unmap(), pr.Bits()-unmapBits(pr)).Masked()
	if pr.Bits() == 0 {
		return netip.Prefix{}, fmt.Errorf("%q allows everything: use the mode internet", s)
	}
	return pr, nil
}

// unmapBits is 96 for an IPv4-mapped IPv6 prefix (::ffff:a.b.c.d/n), else 0.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return 96
	}
	return 0
}

// Strings is the allow list as CIDR text.
func (p Policy) Strings() []string {
	out := make([]string, 0, len(p.Allow))
	for _, a := range p.Allow {
		out = append(out, a.String())
	}
	return out
}
