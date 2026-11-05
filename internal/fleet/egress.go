package fleet

import (
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// EgressMode is how much of the network an app may reach (the cluster's
// policy, enforced by the agent with Kubernetes NetworkPolicies; docs/egress.md).
type EgressMode string

const (
	// EgressInternet: the public internet (every address but the private,
	// link-local, carrier-grade NAT and loopback ranges) plus the allow list.
	// The default of every cluster.
	EgressInternet EgressMode = "internet"
	// EgressRestricted: only the allow list.
	EgressRestricted EgressMode = "restricted"
)

// MaxEgressAllow bounds the allow list (a NetworkPolicy with thousands of
// ipBlocks slows down every node's dataplane).
const MaxEgressAllow = 100

// CapabilityEgress is listed in Capabilities.APIs when the agent enforces
// the egress policy (it keeps a NetworkPolicy in every app namespace).
const CapabilityEgress = "odicea.egress"

// CanEnforceEgress reports whether the cluster lists CapabilityEgress.
func (c Capabilities) CanEnforceEgress() bool {
	return slices.Contains(c.APIs, CapabilityEgress)
}

// Egress is the setting TI chooses for a cluster.
type Egress struct {
	Mode EgressMode `json:"mode"`
	// Allow are CIDRs (canonical, sorted, unique) every app of the cluster
	// may reach in either mode (an internal LLM gateway, a database on the
	// company network).
	Allow []string `json:"allow"`
}

// DefaultEgress is what a cluster has until TI changes it.
func DefaultEgress() Egress { return Egress{Mode: EgressInternet, Allow: []string{}} }

// Same reports whether e and other are the same normalized setting.
func (e Egress) Same(other Egress) bool {
	return e.Mode == other.Mode && slices.Equal(e.Allow, other.Allow)
}

// SetEgress replaces the egress policy of a cluster.
type SetEgress struct {
	// Mode is "internet" or "restricted" (empty: internet).
	Mode EgressMode `json:"mode"`
	// Allow are CIDRs; a bare address is a /32 (or /128). 0.0.0.0/0 and ::/0
	// are refused: "everything" is the internet mode, which keeps the
	// private ranges (cloud metadata, the Kubernetes API) closed.
	Allow     []string        `json:"allow"`
	ChangedBy identity.UserID `json:"-"` // taken from the token, never the body
}

func (s SetEgress) Validate() error {
	if s.ChangedBy.IsZero() {
		return errx.Validation("changed_by is required")
	}
	_, err := s.Policy()
	return err
}

// Policy is the normalized setting: the mode defaulted, every CIDR parsed
// and written canonically (host bits cleared, IPv4-mapped IPv6 unmapped),
// sorted, without duplicates.
func (s SetEgress) Policy() (Egress, error) {
	mode := s.Mode
	if mode == "" {
		mode = EgressInternet
	}
	if mode != EgressInternet && mode != EgressRestricted {
		return Egress{}, errx.Validation("mode must be internet or restricted").WithField("mode")
	}
	if len(s.Allow) > MaxEgressAllow {
		return Egress{}, errx.Validation("la lista admite hasta 100 direcciones").WithField("allow")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(s.Allow))
	for _, raw := range s.Allow {
		p, err := ParseCIDR(raw)
		if err != nil {
			return Egress{}, err
		}
		if p.Bits() == 0 {
			return Egress{}, errx.Validation("«" + strings.TrimSpace(raw) + "» abriría todo Internet y las redes privadas del clúster. Para salir a Internet elige «Internet pública».").WithField("allow")
		}
		if c := p.String(); !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return Egress{Mode: mode, Allow: out}, nil
}

// ParseCIDR reads an address range: a CIDR, or a single address (a /32 or
// /128). The result is canonical: the network address with the mask.
func ParseCIDR(raw string) (netip.Prefix, error) {
	s := strings.TrimSpace(raw)
	bad := errx.Validation("«" + s + "» no es una dirección válida. Usa el formato 203.0.113.0/24 o una dirección sola.").WithField("allow")
	if s == "" || len(s) > 64 {
		return netip.Prefix{}, bad
	}
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return netip.Prefix{}, bad
		}
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || p.Addr().Zone() != "" {
		return netip.Prefix{}, bad
	}
	if p.Addr().Is4In6() { // ::ffff:1.2.3.0/120 is an IPv4 range
		if p.Bits() < 96 {
			return netip.Prefix{}, bad
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
}

// EgressState is how a cluster's agent has taken the policy.
type EgressState string

const (
	// EgressApplied: the agent confirmed this generation of the policy.
	EgressApplied EgressState = "applied"
	// EgressPending: the policy waits for the agent (offline, or it has not
	// answered yet).
	EgressPending EgressState = "pending"
	// EgressFailed: the agent answered with an error.
	EgressFailed EgressState = "failed"
	// EgressUnsupported: the cluster's agent does not list CapabilityEgress
	// (it is older, or it never connected): nothing enforces the policy.
	EgressUnsupported EgressState = "unsupported"
)

// EgressSetting is the stored policy of a cluster and what the agent answered.
type EgressSetting struct {
	Egress
	// Generation grows with every change; the agent acknowledges it.
	Generation int64 `json:"generation"`
	// Acknowledged is the last generation the agent answered for (nil: none yet).
	Acknowledged *int64 `json:"acknowledged_generation"`
	// Error is the agent's words when it could not apply Acknowledged.
	Error string `json:"error"`
	// AcknowledgedAt is when the agent answered.
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	// Warnings are what the agent says TI should review although the policy is
	// applied (an allow entry that includes a node of the cluster). Only of
	// the generation the agent answered for; empty while it is pending.
	Warnings []string `json:"warnings"`
	// Supported reports CapabilityEgress (set by the service).
	Supported bool `json:"supported"`
}

// Standing is where the policy stands.
func (e EgressSetting) Standing() EgressState {
	switch {
	case !e.Supported:
		return EgressUnsupported
	case e.Acknowledged == nil || *e.Acknowledged != e.Generation:
		return EgressPending
	case e.Error != "":
		return EgressFailed
	}
	return EgressApplied
}

// EgressStatus is the API's view of EgressSetting: the policy plus its state.
type EgressStatus struct {
	EgressSetting
	State EgressState `json:"state"`
	// Enforced is true when State is applied.
	Enforced bool `json:"enforced"`
}

// Status adds the derived fields.
func (e EgressSetting) Status() EgressStatus {
	s := e.Standing()
	if s == EgressPending || s == EgressUnsupported || e.Warnings == nil {
		e.Warnings = []string{}
	}
	return EgressStatus{EgressSetting: e, State: s, Enforced: s == EgressApplied}
}

// Policy is what the gateway sends an agent: the cluster's setting at a generation.
type Policy struct {
	Generation int64
	Egress     Egress
}

// PolicyAck is the agent's answer to a Policy.
type PolicyAck struct {
	Generation int64
	// Error is empty when the agent applied the policy.
	Error string
	// Warnings are what TI should review although it did (at most
	// MaxPolicyWarnings, each at most MaxAckError runes).
	Warnings []string
}

// MaxPolicyWarnings bounds the warnings an ack stores.
const MaxPolicyWarnings = 10

// EgressTarget is the egress policy of the cluster an environment runs on
// (or would first run on: the organization's default), for the app's view.
type EgressTarget struct {
	// Cluster is zero when the organization has no cluster for it, or its
	// cluster was revoked: no policy applies.
	Cluster ClusterID
	Status  EgressStatus
}
