package fleet_test

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
)

func TestEgressPolicyIsNormalized(t *testing.T) {
	got, err := fleet.SetEgress{Mode: fleet.EgressRestricted, ChangedBy: user, Allow: []string{
		" 203.0.113.9/24 ", "10.1.2.3", "10.1.2.3/32", "::ffff:192.0.2.7", "2001:DB8::1/32", "10.0.0.0/8"}}.Policy()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "10.1.2.3/32", "192.0.2.7/32", "2001:db8::/32", "203.0.113.0/24"}
	if got.Mode != fleet.EgressRestricted || !slices.Equal(got.Allow, want) {
		t.Fatalf("policy %+v, want %v", got, want)
	}
	def, err := fleet.SetEgress{ChangedBy: user}.Policy()
	if err != nil || def.Mode != fleet.EgressInternet || def.Allow == nil || len(def.Allow) != 0 {
		t.Fatalf("default %+v %v", def, err)
	}
	if !def.Same(fleet.DefaultEgress()) {
		t.Error("the empty setting is the default")
	}
}

func TestEgressPolicyRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, bad := range []string{"", "  ", "example.com", "1.2.3.4/33", "1.2.3", "1.2.3.4/", "fe80::1%eth0", "0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "999.1.1.1"} {
		err := fleet.SetEgress{ChangedBy: user, Mode: fleet.EgressRestricted, Allow: []string{bad}}.Validate()
		if !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("%q: %v, want a validation error", bad, err)
		}
	}
	if err := (fleet.SetEgress{ChangedBy: user, Mode: "open"}).Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("unknown mode: %v", err)
	}
	if err := (fleet.SetEgress{Mode: fleet.EgressInternet}).Validate(); err == nil {
		t.Error("changed_by is required")
	}
	many := make([]string, fleet.MaxEgressAllow+1)
	for i := range many {
		many[i] = "10.0.0." + string(rune('0'+i%10)) + "/32"
	}
	if err := (fleet.SetEgress{ChangedBy: user, Allow: many}).Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("too many: %v", err)
	}
}

func TestEgressStanding(t *testing.T) {
	one, two := int64(1), int64(2)
	cases := []struct {
		name string
		s    fleet.EgressSetting
		want fleet.EgressState
	}{
		{"old agent", fleet.EgressSetting{Generation: 1, Acknowledged: &one}, fleet.EgressUnsupported},
		{"never answered", fleet.EgressSetting{Generation: 1, Supported: true}, fleet.EgressPending},
		{"answered an older one", fleet.EgressSetting{Generation: 2, Acknowledged: &one, Supported: true}, fleet.EgressPending},
		{"applied", fleet.EgressSetting{Generation: 2, Acknowledged: &two, Supported: true}, fleet.EgressApplied},
		{"failed", fleet.EgressSetting{Generation: 2, Acknowledged: &two, Error: "no", Supported: true}, fleet.EgressFailed},
	}
	for _, c := range cases {
		if got := c.s.Status(); got.State != c.want || got.Enforced != (c.want == fleet.EgressApplied) {
			t.Errorf("%s: %+v, want %s", c.name, got, c.want)
		}
	}
}

func TestCapabilityEgress(t *testing.T) {
	if (fleet.Capabilities{APIs: []string{"traefik.io"}}).CanEnforceEgress() || !(fleet.Capabilities{APIs: []string{fleet.CapabilityEgress}}).CanEnforceEgress() {
		t.Error("CanEnforceEgress follows the odicea.egress capability")
	}
}
