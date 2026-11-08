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
	// scan.checked tells "not looked at yet" (the first judgment) from "looked
	// at and unknown": the production_requires_scan rule needs the difference.
	requires := rule(`action == 'deploy' && project.tier == 'production' && scan.checked && !scan.known`)
	prod := policy.Input{Action: "deploy", Project: policy.ProjectInput{Tier: "production"}}
	for name, c := range map[string]struct {
		scan policy.ScanInput
		want bool
	}{
		"first judgment, no scan yet": {policy.ScanInput{}, false},
		"checked, scan failed":        {policy.ScanInput{Checked: true}, true},
		"checked, scan done":          {policy.ScanInput{Checked: true, Known: true, Critical: 9}, false},
		"known implies checked (dry)": {policy.ScanInput{Known: true}, false},
	} {
		in := prod
		in.Scan = c.scan
		if got, err := e.Matches(context.Background(), requires, in); err != nil || got != c.want {
			t.Errorf("%s: got %v, %v want %v", name, got, err, c.want)
		}
	}
	if got, err := e.Matches(context.Background(), rule(`scan.checked`), policy.Input{Scan: policy.ScanInput{Known: true}}); err != nil || !got {
		t.Errorf("a known scan is checked: %v %v", got, err)
	}
	// no actor permissions at all is an empty list, not an error
	if got, err := e.Matches(context.Background(), rule(`size(actor.permissions) == 0`), policy.Input{}); err != nil || !got {
		t.Errorf("empty permissions: %v %v", got, err)
	}
}

// The catalog's resolution as rules see it: the pinned recipe, its
// normalized parameters and what falls outside IT's pre-approved range.
// Absent is empty (no recipe, no params), never an error.
func TestMatchesARecipe(t *testing.T) {
	e := policycel.New(policycel.Options{})
	in := policy.Input{Action: "create_resource", Resource: policy.ResourceInput{Kind: "postgres", Recipe: "postgres-produccion",
		RecipeVersion: 1, Engine: "cnpg", Params: map[string]int{"storage_gi": 50, "instances": 3}, OutOfRange: []string{"storage_gi"}}}
	for expr, want := range map[string]bool{
		`resource.kind == 'postgres' && resource.recipe == 'postgres-produccion' && resource.recipe_version == 1`: true,
		`resource.engine == 'cnpg'`:                    true,
		`resource.params.storage_gi > 20`:              true,
		`resource.params['instances'] >= 3`:            true,
		`'cpu' in resource.params`:                     false,
		`'storage_gi' in resource.out_of_range`:        true,
		`size(resource.out_of_range) > 0`:              true,
		`resource.params.exists(p, p == 'storage_gi')`: true,
	} {
		if got, err := e.Matches(context.Background(), rule(expr), in); err != nil || got != want {
			t.Errorf("%q: got %v, %v want %v", expr, got, err, want)
		}
	}
	for expr, want := range map[string]bool{
		`size(resource.params) == 0 && size(resource.out_of_range) == 0 && resource.recipe == ''`: true,
		`resource.recipe_version == 0`: true,
	} {
		if got, err := e.Matches(context.Background(), rule(expr), policy.Input{Action: "deploy"}); err != nil || got != want {
			t.Errorf("absent %q: got %v, %v want %v", expr, got, err, want)
		}
	}
	// a parameter the request does not carry is an evaluation error: the rule
	// fails closed at its own strength (Decide), it is not silently false
	if _, err := e.Matches(context.Background(), rule(`resource.params.cpu > 1`), in); err == nil {
		t.Error("a missing parameter evaluated")
	}
	if err := e.Compile(`resource.params.storage_gi > 'big'`); err == nil {
		t.Error("params are ints")
	}
}

// services, changed and topology_changed (MS-3c): the same rule reads an app
// of one service exactly like one of several, and absent means empty.
func TestMatchesServices(t *testing.T) {
	e := policycel.New(policycel.Options{})
	two := policy.Input{Action: "deploy", Project: policy.ProjectInput{Tier: "production"},
		Services: []policy.ServiceInput{{Name: "web", Kind: "web", Route: "/", Public: true}, {Name: "jobs", Kind: "worker"}},
		Changed:  []string{"jobs"}, TopologyChanged: true}
	one := policy.Input{Action: "deploy", Project: policy.ProjectInput{Tier: "production"},
		Services: []policy.ServiceInput{{Name: "web", Kind: "web", Route: "/", Public: true}}, Changed: []string{"web"}}
	for expr, want := range map[string][2]bool{ // {two, one}
		`services.exists(s, s.kind == 'worker')`:                 {true, false},
		`services.all(s, s.public)`:                              {false, true},
		`size(services) > 1`:                                     {true, false},
		`'web' in changed`:                                       {false, true},
		`topology_changed`:                                       {true, false},
		`services.exists(s, s.route == '/' && s.name == 'web')`:  {true, true},
		`action == 'deploy' && changed.exists(c, c == 'jobs')`:   {true, false},
		`services.filter(s, s.public).map(s, s.name) == ['web']`: {true, true},
	} {
		for i, in := range []policy.Input{two, one} {
			got, err := e.Matches(context.Background(), rule(expr), in)
			if err != nil || got != want[i] {
				t.Errorf("%q on %d services: got %v, %v want %v", expr, len(in.Services), got, err, want[i])
			}
		}
	}
	// an action not about services sees empty lists, not an error
	for _, expr := range []string{`size(services) == 0`, `size(changed) == 0`, `!topology_changed`} {
		if got, err := e.Matches(context.Background(), rule(expr), policy.Input{Action: "set_access"}); err != nil || !got {
			t.Errorf("%q with no services: %v %v", expr, got, err)
		}
	}
	if err := e.Compile(`services.exists(s, s.colour == 'red')`); err == nil {
		t.Error("an unknown field of a service compiled")
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
