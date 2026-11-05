package policy_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
)

// The payloads are the public contract: see testdata/events.
func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	key := policy.BuiltinSandboxPublic
	r := policy.Rule{ID: policy.MustParseRuleID("00000000-0000-4000-8000-0000000000aa"), Name: "Sin producción los viernes",
		Expression: "action == 'deploy'", Effect: policy.EffectDeny, Enabled: true, Version: 2}
	b := r
	b.Builtin, b.Enabled = &key, false
	for _, e := range []event.Event{
		policy.NewRuleCreated(org, user, r),
		policy.NewRuleUpdated(org, user, r),
		policy.NewRuleDeleted(org, user, r),
	} {
		eventtest.Golden(t, e)
	}
	if e := policy.NewRuleUpdated(org, user, b); e.Summary != "Apagó la regla de TI «Sin producción los viernes»" {
		t.Fatalf("summary %q", e.Summary)
	}
}
