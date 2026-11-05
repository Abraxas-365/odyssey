package policyhttp_test

import (
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyhttp"
	"github.com/Abraxas-365/odyssey/internal/policy/policysvc"
	"github.com/Abraxas-365/odyssey/internal/policy/policytest"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

func TestRulesRoutes(t *testing.T) {
	engine := &policytest.Engine{
		Matching: map[string]bool{"deny-it": true},
		Invalid:  map[string]string{"nope ==": "línea 1:8: syntax error"},
	}
	svc := policysvc.New(policytest.NewRepo(), engine, txn.Passthrough{}, &eventtest.Recorder{})
	app := httpxtest.App()
	policyhttp.New(svc, svc, svc).Register(app.Group("/api/v1"))
	org := identity.NewOrganizationID()
	it := httpxtest.Token(org, permission.PolicyManage)
	builder := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite)
	other := httpxtest.Token(identity.NewOrganizationID(), permission.PolicyManage)
	do := func(method, path, token string, body any) httpxtest.Response {
		return httpxtest.Do(t, app, method, path, token, body)
	}

	// only policy:manage reaches any of it
	for _, r := range [][2]string{{http.MethodGet, "/api/v1/policy/rules"}, {http.MethodPost, "/api/v1/policy/rules"},
		{http.MethodPut, "/api/v1/policy/rules/" + policy.NewRuleID().String()},
		{http.MethodDelete, "/api/v1/policy/rules/" + policy.NewRuleID().String()}, {http.MethodPost, "/api/v1/policy/evaluate"}} {
		httpxtest.Want(t, do(r[0], r[1], builder, map[string]any{}), http.StatusForbidden)
	}

	// the five built-ins are there from the first read
	res := do(http.MethodGet, "/api/v1/policy/rules", it, nil)
	httpxtest.Want(t, res, http.StatusOK)
	list := httpxtest.JSON[struct{ Items []policy.Rule }](t, res)
	if len(list.Items) != 5 || list.Items[0].Builtin == nil {
		t.Fatalf("rules %+v", list.Items)
	}
	builtin := list.Items[0]

	// create; a bad expression is a 422 with the compiler's message
	res = do(http.MethodPost, "/api/v1/policy/rules", it, map[string]any{
		"name": "Sin producción los viernes", "expression": "deny-it", "effect": "deny"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[policy.Rule](t, res)
	if created.Enabled != true || created.Approvers != permission.ApprovalsDecide || res.Header.Get("Location") == "" {
		t.Fatalf("created %+v", created)
	}
	res = do(http.MethodPost, "/api/v1/policy/rules", it, map[string]any{"name": "x", "expression": "nope ==", "effect": "deny"})
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if httpxtest.ErrorCode(res) != policy.CodeInvalid {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}
	httpxtest.Want(t, do(http.MethodPost, "/api/v1/policy/rules", it, map[string]any{"name": "x", "expression": "true", "effect": "maybe"}), http.StatusBadRequest)
	httpxtest.Want(t, do(http.MethodPost, "/api/v1/policy/rules", it, `{"name":"x","expression":"true","effect":"deny","builtin":"hack"}`), http.StatusBadRequest)

	// replace; built-ins only take their switch
	path := "/api/v1/policy/rules/" + created.ID.String()
	res = do(http.MethodPut, path, it, map[string]any{"name": "Sin prod", "expression": "deny-it", "effect": "approval", "enabled": false})
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[policy.Rule](t, res); got.Effect != policy.EffectApproval || got.Enabled || got.Version != 2 {
		t.Fatalf("replaced %+v", got)
	}
	bpath := "/api/v1/policy/rules/" + builtin.ID.String()
	res = do(http.MethodPut, bpath, it, map[string]any{"name": builtin.Name, "expression": "false", "effect": "deny"})
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if httpxtest.ErrorCode(res) != policy.CodeBuiltin {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}
	res = do(http.MethodDelete, bpath, it, nil)
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)

	// another organization finds nothing
	httpxtest.Want(t, do(http.MethodPut, path, other, map[string]any{"name": "x", "expression": "true", "effect": "deny"}), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodDelete, path, other, nil), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodDelete, "/api/v1/policy/rules/not-a-uuid", it, nil), http.StatusBadRequest)

	// delete
	httpxtest.Want(t, do(http.MethodDelete, path, it, nil), http.StatusNoContent)
	httpxtest.Want(t, do(http.MethodDelete, path, it, nil), http.StatusNotFound)
}

func TestEvaluateIsADryRun(t *testing.T) {
	engine := &policytest.Engine{Matching: map[string]bool{"deny-it": true, "approve-it": true}, Invalid: map[string]string{"1 +": "syntax error"}}
	events := &eventtest.Recorder{}
	svc := policysvc.New(policytest.NewRepo(), engine, txn.Passthrough{}, events)
	app := httpxtest.App()
	policyhttp.New(svc, svc, svc).Register(app.Group("/api/v1"))
	org := identity.NewOrganizationID()
	it := httpxtest.Token(org, permission.PolicyManage)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/rules", it,
		map[string]any{"name": "bloquea", "expression": "deny-it", "effect": "deny"}), http.StatusCreated)

	res := httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, map[string]any{
		"action": "deploy", "project": map[string]any{"tier": "production"}, "scan": map[string]any{"known": true, "critical": 1}})
	httpxtest.Want(t, res, http.StatusOK)
	got := httpxtest.JSON[policy.Evaluation](t, res)
	if got.Effect != policy.EffectDeny || len(got.Rules) != 1 || got.Rules[0].Name != "bloquea" || len(got.Results) != 6 {
		t.Fatalf("evaluation %+v", got)
	}
	before := len(events.Events())

	// a bad input is a 400 naming the field; nothing is written
	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, map[string]any{"action": "fly"})
	httpxtest.Want(t, res, http.StatusBadRequest)
	if httpxtest.ErrorField(res) != "action" {
		t.Fatalf("field %q", httpxtest.ErrorField(res))
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it,
		map[string]any{"action": "deploy", "project": map[string]any{"tier": "gold"}}), http.StatusBadRequest)
	if len(events.Events()) != before {
		t.Fatal("the dry run wrote events")
	}

	// an unsaved rule is tried among the saved ones (and writes nothing)
	rule := func(r map[string]any) map[string]any {
		return map[string]any{"action": "rollback", "rule": r}
	}
	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it,
		rule(map[string]any{"expression": "approve-it", "effect": "approval"}))
	httpxtest.Want(t, res, http.StatusOK)
	got = httpxtest.JSON[policy.Evaluation](t, res)
	// one more rule (7 results); the saved deny rule still wins over the candidate's approval
	if got.Effect != policy.EffectDeny || len(got.Rules) != 1 || got.Rules[0].Name != "bloquea" || len(got.Results) != 7 ||
		got.Results[6].Rule.Name != "Regla de prueba" || !got.Results[6].Matched {
		t.Fatalf("candidate evaluation %+v", got)
	}
	// a candidate that does not compile is a 422, like saving it
	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, rule(map[string]any{"expression": "1 +", "effect": "deny"}))
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if httpxtest.ErrorCode(res) != policy.CodeInvalid {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it,
		rule(map[string]any{"expression": "deny-it", "effect": "maybe"})), http.StatusBadRequest)
	// `replaces` swaps the stored rule for the candidate: with the rule
	// bloquea replaced by one that does not match, the deploy is allowed.
	list := httpxtest.JSON[struct{ Items []policy.Rule }](t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/policy/rules", it, nil))
	var bloquea policy.Rule
	for _, r := range list.Items {
		if r.Name == "bloquea" {
			bloquea = r
		}
	}
	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, map[string]any{"action": "deploy",
		"rule": map[string]any{"name": "bloquea", "expression": "never", "effect": "deny", "replaces": bloquea.ID.String()}})
	httpxtest.Want(t, res, http.StatusOK)
	got = httpxtest.JSON[policy.Evaluation](t, res)
	if got.Effect != policy.EffectAllow || len(got.Results) != 6 {
		t.Fatalf("replaced evaluation %+v", got)
	}
	if len(events.Events()) != before {
		t.Fatal("the candidate dry run wrote events")
	}
}
