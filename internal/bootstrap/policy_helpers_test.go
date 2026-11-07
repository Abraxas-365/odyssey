package bootstrap_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/policy"
)

// switchOff turns the organization's built-in rules off (by key), as IT does
// in the console: tests about something else than IT's rules use it so a
// default rule does not stand in their way.
func switchOff(t *testing.T, platform *bootstrap.Platform, org identity.OrganizationID, keys ...string) {
	t.Helper()
	switchBuiltins(t, platform, org, false, keys...)
}

// switchOn turns built-in rules that are born off on.
func switchOn(t *testing.T, platform *bootstrap.Platform, org identity.OrganizationID, keys ...string) {
	t.Helper()
	switchBuiltins(t, platform, org, true, keys...)
}

func switchBuiltins(t *testing.T, platform *bootstrap.Platform, org identity.OrganizationID, on bool, keys ...string) {
	t.Helper()
	ctx := context.Background()
	rules, err := platform.Policy.Queries.List(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	enabled := on
	for _, key := range keys {
		found := false
		for _, r := range rules {
			if r.Builtin == nil || *r.Builtin != key {
				continue
			}
			found = true
			draft := policy.Draft{Name: r.Name, Description: r.Description, Expression: r.Expression,
				Effect: r.Effect, Approvers: r.Approvers, Enabled: &enabled}
			if _, err := platform.Policy.Commands.Replace(ctx, org, r.ID,
				policy.Replace{Draft: draft, UpdatedBy: identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")}); err != nil {
				t.Fatal(err)
			}
		}
		if !found {
			t.Fatalf("no built-in rule %q", key)
		}
	}
}
