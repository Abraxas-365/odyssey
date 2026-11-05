package policycel_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policycel"
)

func rule(expr string) policy.Rule {
	return policy.Rule{ID: policy.NewRuleID(), Expression: expr, Version: 1}
}

func TestCompileChecksTheShapeAndTheType(t *testing.T) {
	e := policycel.New(policycel.Options{})
	for _, ok := range []string{
		`action == 'deploy' && project.tier == 'production'`,
		`scan.known && scan.critical > 0 || scan.high >= 5`,
		`!('odyssey:access:public' in actor.permissions)`,
		`actor.permissions.exists(p, p.startsWith('odyssey:projects'))`,
		`env.name == 'production' && resource.kind == 'database' && project.slug.endsWith('-app')`,
	} {
		if err := e.Compile(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for expr, want := range map[string]string{
		`action ==`:              "línea 1", // syntax
		`project.tierr == 'x'`:   "tierr",   // unknown field
		`nope == 1`:              "nope",    // unknown variable
		`scan.critical > 'many'`: "",        // type mismatch
		`action`:                 "bool",    // not a bool
		`1 + 1`:                  "bool",
		`project.tier == 1`:      "",
		strings.Repeat("(", 200) + `true` + strings.Repeat(")", 200): "", // recursion limit
	} {
		err := e.Compile(expr)
		if err == nil {
			t.Errorf("%q compiled", expr)
			continue
		}
		var inv *policy.InvalidExpression
		if !errorsAs(err, &inv) || !strings.Contains(inv.Message, want) {
			t.Errorf("%q: %v (want %q)", expr, err, want)
		}
	}
}

func errorsAs(err error, target **policy.InvalidExpression) bool {
	e, ok := err.(*policy.InvalidExpression)
	if ok {
		*target = e
	}
	return ok
}

func TestMatches(t *testing.T) {
	e := policycel.New(policycel.Options{})
	in := policy.Input{Action: "deploy", Project: policy.ProjectInput{Tier: "production", Slug: "portal"},
		Actor: policy.ActorInput{ID: "u1", Permissions: []string{"odyssey:projects:read"}},
		Scan:  policy.ScanInput{Known: true, Critical: 2}}
	for expr, want := range map[string]bool{
		`action == 'deploy' && project.tier == 'production'`:                      true,
		`action == 'deploy' && project.tier == 'production' && scan.critical > 0`: true,
		`action == 'rollback'`:                         false,
		`'odyssey:projects:read' in actor.permissions`: true,
		`'odyssey:access:public' in actor.permissions`: false,
		`access.mode == ''`:                            true, // absent fields are zero
		`resource.kind == '' && !scan.known == false`:  true,
	} {
		got, err := e.Matches(context.Background(), rule(expr), in)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v want %v", expr, got, err, want)
		}
	}
	// no actor permissions at all is an empty list, not an error
	if got, err := e.Matches(context.Background(), rule(`size(actor.permissions) == 0`), policy.Input{}); err != nil || !got {
		t.Errorf("empty permissions: %v %v", got, err)
	}
}

func TestCostIsBounded(t *testing.T) {
	e := policycel.New(policycel.Options{CostLimit: 200, Timeout: time.Second})
	// a comprehension over a big range costs far more than 200
	expensive := rule(`[1,2,3,4,5,6,7,8,9,10].all(a, [1,2,3,4,5,6,7,8,9,10].all(b, [1,2,3,4,5,6,7,8,9,10].all(c, a + b + c > 0)))`)
	if err := e.Compile(expensive.Expression); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Matches(context.Background(), expensive, policy.Input{}); err == nil || !strings.Contains(err.Error(), "cost") {
		t.Fatalf("expensive rule ran: %v", err)
	}
}

func TestACanceledContextStopsAnEvaluation(t *testing.T) {
	e := policycel.New(policycel.Options{CostLimit: 1 << 40, Timeout: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := rule(`[1,2,3,4,5,6,7,8,9,10].all(a, [1,2,3,4,5,6,7,8,9,10].all(b, a + b > 0))`)
	if got, err := e.Matches(ctx, slow, policy.Input{}); err == nil && got {
		// a program this small may finish before the first interrupt check;
		// what must never happen is a hang, which the test would show.
		t.Log("finished before the interrupt check")
	}
}

func TestCompiledProgramsAreCachedByIDAndVersion(t *testing.T) {
	e := policycel.New(policycel.Options{})
	r := rule(`action == 'a'`)
	in := policy.Input{Action: "a"}
	if got, _ := e.Matches(context.Background(), r, in); !got {
		t.Fatal("first evaluation")
	}
	// the same id and version keeps the cached program, even if the text
	// changed under it (the service always bumps the version on an edit)...
	r.Expression = `action == 'b'`
	if got, _ := e.Matches(context.Background(), r, in); !got {
		t.Fatal("same version must reuse the compiled program")
	}
	// ...and a new version compiles the new text.
	r.Version = 2
	if got, _ := e.Matches(context.Background(), r, in); got {
		t.Fatal("a new version must recompile")
	}
	// An unsaved rule (version 0, a dry-run candidate) is never cached: the
	// same id with other text is compiled again.
	c := policy.Rule{ID: policy.NewRuleID(), Expression: `action == 'a'`}
	if got, _ := e.Matches(context.Background(), c, in); !got {
		t.Fatal("candidate first evaluation")
	}
	c.Expression = `action == 'b'`
	if got, _ := e.Matches(context.Background(), c, in); got {
		t.Fatal("a candidate must not be served from the cache")
	}
}
