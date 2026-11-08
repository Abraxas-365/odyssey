package gate_test

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/gate"
)

func TestWithKeepsTheStrongestVerdictAndItsRules(t *testing.T) {
	rule := func(id string) gate.Rule { return gate.Rule{ID: id, Name: id} }
	allow := gate.Verdict{}
	approveA := gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{rule("a")}, Approvers: []string{"p:x"}}
	approveB := gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{rule("b"), rule("a")}}
	deny := gate.Verdict{Effect: gate.Deny, Rules: []gate.Rule{rule("d")}}

	if v := allow.With(gate.Verdict{Effect: gate.Allow}); v.Effect != gate.Allow || len(v.Rules) != 0 || !v.Allowed() {
		t.Fatalf("allow %+v", v)
	}
	// one operation, two reasons to ask: held once, with both rules
	v := approveA.With(allow, approveB)
	if v.Effect != gate.Approval || !slices.Equal(v.Rules, []gate.Rule{rule("a"), rule("b")}) || !slices.Equal(v.Approvers, []string{"p:x"}) {
		t.Fatalf("approval %+v", v)
	}
	// an allow never lifts an approval; a deny beats both, alone
	if v := allow.With(approveB); v.Effect != gate.Approval {
		t.Fatalf("allow lifted %+v", v)
	}
	if v := approveA.With(deny, approveB); v.Effect != gate.Deny || !slices.Equal(v.Rules, []gate.Rule{rule("d")}) || len(v.Approvers) != 0 {
		t.Fatalf("deny %+v", v)
	}
}
