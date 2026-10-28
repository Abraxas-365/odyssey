package fleetgrpc

import (
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// perAddress rate-limits calls by client address, behind a cap for the
// whole gateway: one address cannot use up what the others need (Enroll
// and Recover are the calls without a client certificate). The address is
// the TCP peer's, so the load balancer in front must preserve it (an L4
// passthrough with client IP preservation; see docs/api.md). An IPv6
// client is one /64 (what a single host usually controls).
type perAddress struct {
	every rate.Limit
	burst int
	max   int // addresses tracked; past it, idle ones are forgotten
	idle  time.Duration
	total *rate.Limiter
	// overflow is the one bucket every address beyond max shares while the
	// table is full: a flood of new addresses competes with itself, not
	// with the addresses already known
	overflow *rate.Limiter

	mu    sync.Mutex
	addrs map[string]*addressLimit
}

type addressLimit struct {
	limit *rate.Limiter
	seen  time.Time
}

func newPerAddress(every rate.Limit, burst int, total *rate.Limiter) *perAddress {
	return &perAddress{every: every, burst: burst, max: 10_000, idle: 10 * time.Minute, total: total,
		overflow: rate.NewLimiter(every, burst), addrs: map[string]*addressLimit{}}
}

// client is the key an address is limited by.
func client(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	if ip = ip.Unmap(); ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

// allow takes a token from the address's bucket, then from the gateway's.
func (p *perAddress) allow(addr string, now time.Time) bool {
	key := client(addr)
	p.mu.Lock()
	var limit *rate.Limiter
	if a, ok := p.addrs[key]; ok {
		a.seen, limit = now, a.limit
	} else {
		if len(p.addrs) >= p.max {
			p.forget(now)
		}
		if len(p.addrs) < p.max {
			a = &addressLimit{limit: rate.NewLimiter(p.every, p.burst), seen: now}
			p.addrs[key], limit = a, a.limit
		} else {
			limit = p.overflow
		}
	}
	allowed := limit.AllowN(now, 1)
	p.mu.Unlock()
	return allowed && p.total.AllowN(now, 1)
}

// forget drops the addresses idle for p.idle (their buckets are full again
// by then, so nothing is lost). Called with p.mu held.
func (p *perAddress) forget(now time.Time) {
	for k, a := range p.addrs {
		if now.Sub(a.seen) > p.idle {
			delete(p.addrs, k)
		}
	}
}
