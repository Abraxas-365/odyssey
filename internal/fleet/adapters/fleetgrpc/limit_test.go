package fleetgrpc

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestPerAddressLimit(t *testing.T) {
	now := time.Now()
	p := newPerAddress(rate.Every(time.Minute), 2, rate.NewLimiter(rate.Inf, 0))
	if !p.allow("10.0.0.1", now) || !p.allow("10.0.0.1", now) {
		t.Fatal("the burst of one address was refused")
	}
	if p.allow("10.0.0.1", now) {
		t.Fatal("an address past its burst was allowed")
	}
	if !p.allow("10.0.0.2", now) {
		t.Fatal("one address used up another's allowance")
	}
	if !p.allow("10.0.0.1", now.Add(time.Minute)) {
		t.Fatal("the address did not refill")
	}

	// the gateway-wide cap still holds across addresses
	total := rate.NewLimiter(rate.Every(time.Hour), 3)
	p = newPerAddress(rate.Every(time.Minute), 5, total)
	allowed := 0
	for i := range 10 {
		if p.allow(string(rune('a'+i)), now) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("%d allowed across addresses; want the cap of 3", allowed)
	}

	// tracked addresses are bounded: idle ones are forgotten; while the table
	// is full, new addresses share one overflow bucket (known ones keep
	// theirs)
	p = newPerAddress(rate.Every(time.Minute), 1, rate.NewLimiter(rate.Inf, 0))
	p.max = 2
	p.allow("a", now)
	p.allow("b", now)
	if !p.allow("c", now) || p.allow("d", now) || len(p.addrs) != 2 {
		t.Fatalf("overflow: want one call for all new addresses (%d tracked)", len(p.addrs))
	}
	if !p.allow("a", now.Add(time.Minute)) {
		t.Fatal("a flood of new addresses used up a known one's allowance")
	}
	if !p.allow("c", now.Add(p.idle+2*time.Minute)) || len(p.addrs) > 2 {
		t.Fatalf("idle addresses were not forgotten (%d tracked)", len(p.addrs))
	}
}

// One IPv6 host is one /64: rotating the low bits does not buy a new
// budget. IPv4 (also mapped into IPv6) is one address.
func TestClientKey(t *testing.T) {
	for addr, want := range map[string]string{
		"10.0.0.1":             "10.0.0.1",
		"::ffff:10.0.0.1":      "10.0.0.1",
		"2001:db8:1:2::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"":                     "",
		"pipe":                 "pipe",
	} {
		if got := client(addr); got != want {
			t.Errorf("client(%q) = %q, want %q", addr, got, want)
		}
	}
	p := newPerAddress(rate.Every(time.Hour), 1, rate.NewLimiter(rate.Inf, 0))
	now := time.Now()
	if !p.allow("2001:db8::1", now) || p.allow("2001:db8::2", now) {
		t.Fatal("two addresses of one /64 got two budgets")
	}
}
