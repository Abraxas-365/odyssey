package policysvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/policy/policysvc"
	"github.com/Abraxas-365/odyssey/internal/policy/policytest"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

func setup(engine *policytest.Engine) (*policysvc.Service, *eventtest.Recorder, *policytest.Repo) {
	events, repo := &eventtest.Recorder{}, policytest.NewRepo()
	return policysvc.New(repo, engine, txn.Passthrough{}, events), events, repo
}

func code(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestFirstReadSeedsTheBuiltinsOnceAndInOrder(t *testing.T) {
	svc, events, repo := setup(&policytest.Engine{})
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	rules, err := svc.List(context.Background(), org)
	if err != nil || len(rules) != builtins {
		t.Fatalf("list: %d rules, %v", len(rules), err)
	}
	for _, r := range rules {
		// production_requires_scan and production_services_approval are born
		// off: they hold back actions of organizations that did not ask for it
		wantOn := *r.Builtin != policy.BuiltinRequiresScan && *r.Builtin != policy.BuiltinProductionServices
		if !r.IsBuiltin() || r.Enabled != wantOn || r.Approvers != permission.ApprovalsDecide || r.Version != 1 {
			t.Errorf("built-in %+v", r)
		}
	}
	if rules[0].Name != "Publicar en producción requiere aprobación de TI" || rules[4].Name != "Producción exige revisión de vulnerabilidades" ||
		rules[5].Name != "Cambiar los servicios de producción requiere aprobación de TI" {
		t.Errorf("names/order: %q … %q … %q", rules[0].Name, rules[4].Name, rules[5].Name)
	}
	// reading again (even through a second service: another replica) adds nothing
	again := policysvc.New(repo, &policytest.Engine{}, txn.Passthrough{}, events)
	if rules, _ := again.List(context.Background(), org); len(rules) != builtins {
		t.Fatalf("re-seeded: %d", len(rules))
	}
	if rules, _ := svc.List(context.Background(), other); len(rules) != builtins {
		t.Fatalf("other org: %d", len(rules))
	}
	if len(events.Events()) != 0 {
		t.Fatalf("seeding published %v", events.Types())
	}
}

// builtins is how many rules every organization is born with.
var builtins = len(policy.Builtins(identity.NewOrganizationID(), time.Now()))

// An organization born before a builtin existed gets it on its next read, and
// a builtin born off arrives OFF for it, whatever else it switched.
func TestANewBuiltinReachesAnExistingOrganizationDisabled(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := setup(&policytest.Engine{})
	org := identity.NewOrganizationID()
	old := policy.Builtins(org, time.Now().UTC())
	old = old[:len(old)-2] // the rulebook before production_requires_scan and production_services_approval existed
	if _, err := repo.SeedBuiltins(ctx, old); err != nil {
		t.Fatal(err)
	}
	rules, err := svc.List(ctx, org)
	if err != nil || len(rules) != builtins {
		t.Fatalf("list: %d rules, %v", len(rules), err)
	}
	for _, r := range rules {
		if *r.Builtin == policy.BuiltinRequiresScan || *r.Builtin == policy.BuiltinProductionServices {
			if r.Enabled {
				t.Fatalf("%s must arrive disabled", *r.Builtin)
			}
			continue
		}
		if !r.Enabled {
			t.Errorf("%s was switched off by the seeding", *r.Builtin)
		}
	}
}

func TestEveryWritePublishesItsEvent(t *testing.T) {
	events := &eventtest.Recorder{}
	repo := policytest.NewRepo()
	svc := policysvc.New(repo, &policytest.Engine{}, txn.Passthrough{}, events)
	ctx, org := context.Background(), identity.NewOrganizationID()

	created, err := svc.Create(ctx, org, policy.Create{CreatedBy: user, Draft: policy.Draft{
		Name: " Sin prod los viernes ", Expression: "action == 'deploy'", Effect: policy.EffectDeny}})
	if err != nil || created.Name != "Sin prod los viernes" || created.Approvers != permission.ApprovalsDecide || !created.Enabled || created.Version != 1 {
		t.Fatalf("create %+v %v", created, err)
	}
	off := false
	edited, err := svc.Replace(ctx, org, created.ID, policy.Replace{UpdatedBy: user, Draft: policy.Draft{
		Name: created.Name, Expression: created.Expression, Effect: policy.EffectApproval, Enabled: &off}})
	if err != nil || edited.Effect != policy.EffectApproval || edited.Enabled || edited.Version != 2 {
		t.Fatalf("replace %+v %v", edited, err)
	}
	// the same content again: no write, no event, same version
	same, err := svc.Replace(ctx, org, created.ID, policy.Replace{UpdatedBy: user, Draft: policy.Draft{
		Name: created.Name, Expression: created.Expression, Effect: policy.EffectApproval, Enabled: &off}})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op replace %+v %v", same, err)
	}
	if err := svc.Delete(ctx, org, created.ID, policy.Delete{DeletedBy: user}); err != nil {
		t.Fatal(err)
	}
	want := []string{policy.EventRuleCreated, policy.EventRuleUpdated, policy.EventRuleDeleted}
	got := events.Types()
	if len(got) != len(want) {
		t.Fatalf("published %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
	if _, err := svc.Find(ctx, org, created.ID); errx.IsType(err, errx.TypeNotFound) == false {
		t.Fatalf("deleted rule still found: %v", err)
	}
}

func TestInvalidExpressionIsRefusedWithTheCompilersMessage(t *testing.T) {
	engine := &policytest.Engine{Invalid: map[string]string{"nope ==": "token recognition error"}}
	svc, events, _ := setup(engine)
	org := identity.NewOrganizationID()
	_, err := svc.Create(context.Background(), org, policy.Create{CreatedBy: user,
		Draft: policy.Draft{Name: "x", Expression: "nope ==", Effect: policy.EffectDeny}})
	var e *errx.Error
	if !errors.As(err, &e) || e.Code != policy.CodeInvalid || e.HTTPStatus != 422 || e.Message == "" {
		t.Fatalf("got %v", err)
	}
	if len(events.Events()) != 0 {
		t.Fatal("an invalid rule published an event")
	}
	if _, err := svc.Create(context.Background(), org, policy.Create{CreatedBy: user,
		Draft: policy.Draft{Name: "x", Expression: "true", Effect: "maybe"}}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("effect: %v", err)
	}
}

func TestBuiltinsCanBeSwitchedOffNotChangedNorDeleted(t *testing.T) {
	svc, events, _ := setup(&policytest.Engine{})
	ctx, org := context.Background(), identity.NewOrganizationID()
	rules, _ := svc.List(ctx, org)
	b := rules[0]
	draft := policy.Draft{Name: b.Name, Description: b.Description, Expression: b.Expression, Effect: b.Effect, Approvers: b.Approvers}

	if err := svc.Delete(ctx, org, b.ID, policy.Delete{DeletedBy: user}); code(err) != policy.CodeBuiltin {
		t.Fatalf("delete: %v", err)
	}
	changed := draft
	changed.Expression = "false"
	if _, err := svc.Replace(ctx, org, b.ID, policy.Replace{UpdatedBy: user, Draft: changed}); code(err) != policy.CodeBuiltin {
		t.Fatalf("change: %v", err)
	}
	off := false
	draft.Enabled = &off
	got, err := svc.Replace(ctx, org, b.ID, policy.Replace{UpdatedBy: user, Draft: draft})
	if err != nil || got.Enabled || got.Version != 2 {
		t.Fatalf("switch off: %+v %v", got, err)
	}
	if types := events.Types(); len(types) != 1 || types[0] != policy.EventRuleUpdated {
		t.Fatalf("events %v", types)
	}
}

func TestAnotherOrganizationsRuleIsNotFound(t *testing.T) {
	svc, _, _ := setup(&policytest.Engine{})
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r, _ := svc.Create(ctx, org, policy.Create{CreatedBy: user, Draft: policy.Draft{Name: "x", Expression: "true", Effect: policy.EffectAllow}})
	if _, err := svc.Find(ctx, other, r.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("find: %v", err)
	}
	d := policy.Draft{Name: "x", Expression: "true", Effect: policy.EffectAllow}
	if _, err := svc.Replace(ctx, other, r.ID, policy.Replace{UpdatedBy: user, Draft: d}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("replace: %v", err)
	}
	if err := svc.Delete(ctx, other, r.ID, policy.Delete{DeletedBy: user}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("delete: %v", err)
	}
}

func TestTheRulebookHasALimit(t *testing.T) {
	svc, _, _ := setup(&policytest.Engine{})
	ctx, org := context.Background(), identity.NewOrganizationID()
	d := policy.Draft{Name: "x", Expression: "true", Effect: policy.EffectAllow}
	for i := builtins; i < policy.MaxRules; i++ { // the built-ins count
		if _, err := svc.Create(ctx, org, policy.Create{CreatedBy: user, Draft: d}); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, org, policy.Create{CreatedBy: user, Draft: d}); code(err) != policy.CodeTooMany {
		t.Fatalf("over the limit: %v", err)
	}
}

// ---- evaluation ---------------------------------------------------------

func rules(t *testing.T, svc *policysvc.Service, org identity.OrganizationID, drafts ...policy.Draft) map[string]policy.Rule {
	t.Helper()
	out := map[string]policy.Rule{}
	for _, d := range drafts {
		r, err := svc.Create(context.Background(), org, policy.Create{CreatedBy: user, Draft: d})
		if err != nil {
			t.Fatal(err)
		}
		out[d.Name] = r
	}
	return out
}

func switchOffBuiltins(t *testing.T, svc *policysvc.Service, org identity.OrganizationID) {
	t.Helper()
	all, _ := svc.List(context.Background(), org)
	off := false
	for _, r := range all {
		if r.IsBuiltin() {
			d := policy.Draft{Name: r.Name, Description: r.Description, Expression: r.Expression, Effect: r.Effect, Approvers: r.Approvers, Enabled: &off}
			if _, err := svc.Replace(context.Background(), org, r.ID, policy.Replace{UpdatedBy: user, Draft: d}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPrecedenceDenyThenApprovalThenAllow(t *testing.T) {
	engine := &policytest.Engine{Matching: map[string]bool{"allow-it": true, "approve-it": true, "deny-it": true}}
	svc, _, _ := setup(engine)
	org := identity.NewOrganizationID()
	switchOffBuiltins(t, svc, org)
	ctx := context.Background()

	// nothing matches: allow, no rules
	d, err := svc.Evaluate(ctx, org, policy.Input{Action: gate.ActionDeploy})
	if err != nil || d.Effect != policy.EffectAllow || len(d.Rules) != 0 || len(d.Approvers) != 0 {
		t.Fatalf("empty: %+v %v", d, err)
	}

	rs := rules(t, svc, org,
		policy.Draft{Name: "permite", Expression: "allow-it", Effect: policy.EffectAllow},
		policy.Draft{Name: "aprobar", Expression: "approve-it", Effect: policy.EffectApproval, Approvers: "a:b"},
		policy.Draft{Name: "aprobar 2", Expression: "approve-it", Effect: policy.EffectApproval})
	d, _ = svc.Evaluate(ctx, org, policy.Input{Action: gate.ActionDeploy})
	if d.Effect != policy.EffectApproval || len(d.Rules) != 2 || d.Rules[0].ID != rs["aprobar"].ID {
		t.Fatalf("approval beats allow: %+v", d)
	}
	if len(d.Approvers) != 2 || d.Approvers[0] != "a:b" || d.Approvers[1] != permission.ApprovalsDecide {
		t.Fatalf("approvers %v", d.Approvers)
	}

	rs = rules(t, svc, org, policy.Draft{Name: "bloquear", Expression: "deny-it", Effect: policy.EffectDeny})
	d, _ = svc.Evaluate(ctx, org, policy.Input{Action: gate.ActionDeploy})
	if d.Effect != policy.EffectDeny || len(d.Rules) != 1 || d.Rules[0].ID != rs["bloquear"].ID || len(d.Approvers) != 0 {
		t.Fatalf("deny beats approval: %+v", d)
	}
}

func TestDisabledRulesAreIgnored(t *testing.T) {
	engine := &policytest.Engine{Matching: map[string]bool{"deny-it": true}}
	svc, _, _ := setup(engine)
	org := identity.NewOrganizationID()
	switchOffBuiltins(t, svc, org)
	off := false
	rules(t, svc, org, policy.Draft{Name: "apagada", Expression: "deny-it", Effect: policy.EffectDeny, Enabled: &off})
	if d, _ := svc.Evaluate(context.Background(), org, policy.Input{Action: gate.ActionDeploy}); d.Effect != policy.EffectAllow {
		t.Fatalf("%+v", d)
	}
}

func TestARuleThatCannotBeEvaluatedFailsClosedAtItsOwnStrength(t *testing.T) {
	engine := &policytest.Engine{Failing: map[string]bool{"boom": true}}
	svc, _, _ := setup(engine)
	org := identity.NewOrganizationID()
	switchOffBuiltins(t, svc, org)
	ctx := context.Background()
	deploy := policy.Input{Action: gate.ActionDeploy}

	// an erroring ALLOW rule is ignored: it neither blocks nor grants
	rs := rules(t, svc, org, policy.Draft{Name: "rota", Expression: "boom", Effect: policy.EffectAllow})
	ev, err := svc.Explain(ctx, org, deploy, nil)
	if err != nil || ev.Effect != policy.EffectAllow || len(ev.Rules) != 0 {
		t.Fatalf("an erroring allow rule blocked: %+v %v", ev, err)
	}
	if len(ev.Results) != builtins+1 || ev.Results[builtins].Rule.ID != rs["rota"].ID || ev.Results[builtins].Error == "" || ev.Results[builtins].Matched {
		t.Fatalf("results %+v", ev.Results)
	}

	// an erroring APPROVAL rule asks for approval
	ap := rules(t, svc, org, policy.Draft{Name: "aprobar-rota", Expression: "boom", Effect: policy.EffectApproval})
	ev, _ = svc.Explain(ctx, org, deploy, nil)
	if ev.Effect != policy.EffectApproval || len(ev.Rules) != 1 || ev.Rules[0].ID != ap["aprobar-rota"].ID {
		t.Fatalf("an erroring approval rule: %+v", ev)
	}

	// an erroring DENY rule denies
	dn := rules(t, svc, org, policy.Draft{Name: "bloquear-rota", Expression: "boom", Effect: policy.EffectDeny})
	ev, _ = svc.Explain(ctx, org, deploy, nil)
	if ev.Effect != policy.EffectDeny || len(ev.Rules) != 1 || ev.Rules[0].ID != dn["bloquear-rota"].ID {
		t.Fatalf("an erroring deny rule: %+v", ev)
	}
}

func TestACandidateRuleIsTriedWithoutSavingIt(t *testing.T) {
	engine := &policytest.Engine{Matching: map[string]bool{"deny-it": true}, Invalid: map[string]string{"nope ==": "syntax"}}
	svc, events, repo := setup(engine)
	org := identity.NewOrganizationID()
	switchOffBuiltins(t, svc, org)
	ctx := context.Background()
	deploy := policy.Input{Action: gate.ActionDeploy}
	stored := rules(t, svc, org, policy.Draft{Name: "guardada", Expression: "deny-it", Effect: policy.EffectDeny})
	before, published := len(mustList(t, repo, org)), len(events.Events())

	ev, err := svc.Explain(ctx, org, deploy, &policy.Candidate{Draft: policy.Draft{Expression: "deny-it", Effect: policy.EffectApproval}})
	if err != nil || ev.Effect != policy.EffectDeny || len(ev.Results) != builtins+2 {
		t.Fatalf("candidate among saved rules: %+v %v", ev, err)
	}
	ev, _ = svc.Explain(ctx, org, deploy, &policy.Candidate{Replaces: stored["guardada"].ID,
		Draft: policy.Draft{Name: "editada", Expression: "other", Effect: policy.EffectDeny}})
	if ev.Effect != policy.EffectAllow || len(ev.Results) != builtins+1 || ev.Results[builtins].Rule.Name != "editada" {
		t.Fatalf("a candidate replacing the stored rule: %+v", ev)
	}
	_, err = svc.Explain(ctx, org, deploy, &policy.Candidate{Draft: policy.Draft{Expression: "nope ==", Effect: policy.EffectDeny}})
	if code(err) != policy.CodeInvalid {
		t.Fatalf("a candidate that does not compile: %v", err)
	}
	if len(mustList(t, repo, org)) != before || len(events.Events()) != published {
		t.Fatal("trying a candidate stored or published something")
	}
}

func mustList(t *testing.T, repo *policytest.Repo, org identity.OrganizationID) []policy.Rule {
	t.Helper()
	out, err := repo.List(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEvaluationsStayInTheirOrganization(t *testing.T) {
	engine := &policytest.Engine{Matching: map[string]bool{"deny-it": true}}
	svc, _, _ := setup(engine)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	switchOffBuiltins(t, svc, org)
	switchOffBuiltins(t, svc, other)
	rules(t, svc, org, policy.Draft{Name: "d", Expression: "deny-it", Effect: policy.EffectDeny})
	if d, _ := svc.Evaluate(context.Background(), other, policy.Input{Action: gate.ActionDeploy}); d.Effect != policy.EffectAllow {
		t.Fatalf("another organization's rule applied: %+v", d)
	}
}
