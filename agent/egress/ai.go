package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The agent reads the namespace's OdysseyApps to know which gateways its apps
// with AI reach (cluster-wide read the controllers already hold).

// Resolver looks a name up (net.DefaultResolver; a test's fake).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// maxGatewayAddrs bounds the addresses of one gateway host in the policy (a
// round-robin name with hundreds of records must not bloat it).
const maxGatewayAddrs = 16

// lookupTimeout bounds resolving one gateway host.
const lookupTimeout = 5 * time.Second

// gatewayOf reads where an app's spec.ai.baseURL points: the host, and the port
// (the scheme's default when the URL names none).
func gatewayOf(raw string) (host string, port int32, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("the AI base URL %q is not a URL", raw)
	}
	switch u.Scheme {
	case "http":
		port = 80
	case "https":
		port = 443
	default:
		return "", 0, fmt.Errorf("the AI base URL %q is not http or https", raw)
	}
	if s := u.Port(); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, fmt.Errorf("the AI base URL %q has an invalid port", raw)
		}
		port = int32(n)
	}
	return strings.ToLower(u.Hostname()), port, nil
}

// serviceNamespace is the namespace of an in-cluster Service host
// (name.namespace.svc or name.namespace.svc.cluster.local); "" for any other.
func serviceNamespace(host string) string {
	h := strings.TrimSuffix(host, ".")
	h = strings.TrimSuffix(strings.TrimSuffix(h, ".cluster.local"), ".svc")
	if h == strings.TrimSuffix(host, ".") { // neither suffix
		return ""
	}
	parts := strings.Split(h, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[1]
}

func (e *Enforcer) resolver() Resolver {
	if e.Resolver != nil {
		return e.Resolver
	}
	return net.DefaultResolver
}

// gateways are the AI gateways of the namespace's apps with AI, resolved now.
// An error means a gateway could not be resolved: the caller leaves the AI
// policy as it is (a gateway whose name does not resolve for a moment must not
// cut the apps that already reach it).
func (e *Enforcer) gateways(ctx context.Context, namespace string) ([]AIGateway, error) {
	var apps v1alpha1.OdysseyAppList
	if err := e.reader().List(ctx, &apps, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list the apps of %s: %w", namespace, err)
	}
	type target struct {
		host string
		port int32
	}
	seen := map[target]bool{}
	var out []AIGateway
	var errs []error
	for _, a := range apps.Items {
		if a.Spec.AI == nil || a.DeletionTimestamp != nil {
			continue
		}
		host, port, err := gatewayOf(a.Spec.AI.BaseURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("app %s: %w", a.Name, err))
			continue
		}
		if seen[target{host, port}] {
			continue
		}
		seen[target{host, port}] = true
		if ns := serviceNamespace(host); ns != "" {
			// Through the Service: its port is not the pod's (targetPort), so
			// the whole namespace's pods are allowed on any port (Port 0).
			out = append(out, AIGateway{Service: ns})
			continue
		}
		addrs, err := e.addresses(ctx, host)
		if err != nil {
			errs = append(errs, fmt.Errorf("app %s: the AI gateway %s: %w", a.Name, host, err))
			continue
		}
		out = append(out, AIGateway{Addrs: addrs, Port: port})
	}
	slices.SortFunc(out, func(a, b AIGateway) int {
		if c := strings.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		if c := slices.CompareFunc(a.Addrs, b.Addrs, netip.Addr.Compare); c != 0 {
			return c
		}
		return int(a.Port - b.Port)
	})
	return out, errors.Join(errs...)
}

// addresses are the IPs of host (an IP literal is itself), sorted, bounded.
func (e *Enforcer) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a.Unmap()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	found, err := e.resolver().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(found))
	for _, a := range found {
		out = append(out, a.Unmap())
	}
	slices.SortFunc(out, netip.Addr.Compare)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil, errors.New("it has no addresses")
	}
	if len(out) > maxGatewayAddrs {
		out = out[:maxGatewayAddrs]
	}
	return out, nil
}
