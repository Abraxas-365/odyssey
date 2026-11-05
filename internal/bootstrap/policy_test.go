package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// refusal is the error body of a governed action that IT's rules stopped.
type refusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details struct {
		Rules     []string `json:"rules"`
		Approvers []string `json:"approvers"`
	} `json:"details"`
}

func refused(t *testing.T, res httpxtest.Response, status int, code string) refusal {
	t.Helper()
	httpxtest.Want(t, res, status)
	var body struct {
		Error refusal `json:"error"`
		refusal
	}
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	got := body.refusal
	if got.Code == "" {
		got = body.Error
	}
	if got.Code != code {
		t.Fatalf("code %q, want %q: %s", got.Code, code, res.Body)
	}
	return got
}

// TestITRulesGovernTheActions: 3a end to end over HTTP. The four built-in
// rules are there from the first read; the tier is IT's to set; a deploy of a
// production app asks for approval (409) before anything is built; a public
// sandbox is denied (403) with the rule's name; a rule IT writes governs a
// rollback; switching a built-in off lets the action through; another
// organization's rules never apply; the dry run and the permission gate work.
func TestITRulesGovernTheActions(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond}, Cluster: cluster})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	dev := []string{permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.DeploymentsRollback, permission.AccessPublic}
	token := httpxtest.Token(org, dev...)
	it := httpxtest.Token(org, append(dev, permission.ProjectsTier, permission.PolicyManage)...)
	stranger := httpxtest.Token(other, append(dev, permission.ProjectsTier, permission.PolicyManage)...)

	create := func(tok, name string) (project.Project, string) {
		t.Helper()
		res := checked(t, app, http.MethodPost, "/api/v1/projects", tok, map[string]any{"name": name})
		httpxtest.Want(t, res, http.StatusCreated)
		p := httpxtest.JSON[project.Project](t, res)
		res = checked(t, app, http.MethodGet, "/api/v1/projects/"+p.ID.String()+"/environments", tok, nil)
		env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
		return p, "/api/v1/projects/" + p.ID.String() + "/environments/" + env.ID.String()
	}
	proj, base := create(token, "Portal")
	tierURL := "/api/v1/projects/" + proj.ID.String() + "/tier"
	deploy := func(tok, base, v string) httpxtest.Response {
		return checked(t, app, http.MethodPost, base+"/deployments", tok, map[string]any{"image": image(v), "port": 8080})
	}
	ready := func(res httpxtest.Response) release.Deployment {
		t.Helper()
		httpxtest.Want(t, res, http.StatusAccepted)
		d := httpxtest.JSON[release.Deployment](t, res)
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			got := httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+d.ID.String(), token, nil))
			if got.Status == release.StatusReady {
				return got
			}
		}
		t.Fatalf("deployment %s did not become ready", d.ID)
		return d
	}
	type rules struct {
		Items []policy.Rule `json:"items"`
	}
	list := func(tok string) []policy.Rule {
		t.Helper()
		res := checked(t, app, http.MethodGet, "/api/v1/policy/rules", tok, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[rules](t, res).Items
	}
	byBuiltin := func(key string) policy.Rule {
		t.Helper()
		for _, r := range list(it) {
			if r.Builtin != nil && *r.Builtin == key {
				return r
			}
		}
		t.Fatalf("no built-in %q", key)
		return policy.Rule{}
	}
	setEnabled := func(tok string, r policy.Rule, on bool) httpxtest.Response {
		return checked(t, app, http.MethodPut, "/api/v1/policy/rules/"+r.ID.String(), tok, map[string]any{
			"name": r.Name, "description": r.Description, "expression": r.Expression, "effect": r.Effect, "approvers": r.Approvers, "enabled": on})
	}

	// Only policy:manage reads or writes the rulebook; tenants do not mix.
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/policy/rules", token, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/policy/evaluate", token, map[string]any{"action": "deploy"}), http.StatusForbidden)
	first := list(it)
	if len(first) != 4 || first[0].Name != "Publicar en producción requiere aprobación de TI" {
		t.Fatalf("the four built-ins from the first read: %+v", first)
	}

	// A new app is internal; the tier is IT's.
	if proj.Tier != project.TierInternal {
		t.Fatalf("default tier %q", proj.Tier)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, token, map[string]any{"tier": "production"}), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "huge"}), http.StatusBadRequest)

	// Internal apps deploy freely: no rule matches.
	v1 := ready(deploy(token, base, "1"))

	// Production: the deploy asks for IT's approval, before anything is stored.
	res := checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "production"})
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[project.Project](t, res); got.Tier != project.TierProduction {
		t.Fatalf("tier after the change %q", got.Tier)
	}
	applied := len(cluster.Applied())
	got := refused(t, deploy(token, base, "2"), http.StatusConflict, gate.CodeApprovalRequired)
	prod := byBuiltin(policy.BuiltinProductionDeploy)
	if !strings.Contains(got.Message, "Publicar en producción requiere aprobación de TI") ||
		len(got.Details.Rules) != 1 || got.Details.Rules[0] != prod.ID.String() ||
		len(got.Details.Approvers) != 1 || got.Details.Approvers[0] != permission.ApprovalsDecide {
		t.Fatalf("approval refusal %+v", got)
	}
	res = checked(t, app, http.MethodGet, base+"/deployments", token, nil)
	if n := len(httpxtest.JSON[query.Paginated[release.Deployment]](t, res).Items); n != 1 {
		t.Fatalf("a refused deploy left a deployment: %d", n)
	}
	if len(cluster.Applied()) != applied {
		t.Fatal("a refused deploy reached the cluster")
	}

	// Switching the built-in off lets the deploy through; back on, it asks again.
	httpxtest.Want(t, setEnabled(it, prod, false), http.StatusOK)
	ready(deploy(token, base, "2"))
	httpxtest.Want(t, setEnabled(it, prod, true), http.StatusOK)
	refused(t, deploy(token, base, "3"), http.StatusConflict, gate.CodeApprovalRequired)

	// Sandbox cannot be public: deny names the rule, and the mode stays.
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "sandbox"}), http.StatusOK)
	got = refused(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "public"}), http.StatusForbidden, gate.CodeDenied)
	if got.Message != "La regla de TI «Sandbox no puede ser pública» no permite esto." {
		t.Fatalf("denial message %q", got.Message)
	}
	// deny beats approval: both public rules match, the sandbox one decides.
	if len(got.Details.Rules) != 1 || got.Details.Rules[0] != byBuiltin(policy.BuiltinSandboxPublic).ID.String() {
		t.Fatalf("deny should win: %+v", got.Details)
	}
	// a mode that does not match any rule goes through.
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"}), http.StatusAccepted)

	// Internal and public: only the approval rule matches.
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "internal"}), http.StatusOK)
	refused(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "public"}), http.StatusConflict, gate.CodeApprovalRequired)

	// Only a CHANGE is judged: asking for the mode already in force is a no-op
	// that no rule refuses, even one that matches that very mode.
	res = checked(t, app, http.MethodPost, "/api/v1/policy/rules", it, map[string]any{
		"name": "Nada de modo organización", "expression": "action == 'set_access' && access.mode == 'organization'", "effect": "deny"})
	httpxtest.Want(t, res, http.StatusCreated)
	noOrg := httpxtest.JSON[policy.Rule](t, res)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "organization"}), http.StatusAccepted)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/policy/rules/"+noOrg.ID.String(), it, nil), http.StatusNoContent)
	// A rule IT writes governs a rollback (`actor.permissions` is the token's).
	res = checked(t, app, http.MethodPost, "/api/v1/policy/rules", it, map[string]any{
		"name": "Solo TI vuelve atrás", "description": "Quien no sea de TI no puede volver a una versión anterior.",
		"expression": "action == 'rollback' && !('" + permission.ProjectsTier + "' in actor.permissions)",
		"effect":     "deny"})
	httpxtest.Want(t, res, http.StatusCreated)
	custom := httpxtest.JSON[policy.Rule](t, res)
	if custom.Builtin != nil || custom.Version != 1 || !custom.Enabled {
		t.Fatalf("custom rule %+v", custom)
	}
	got = refused(t, checked(t, app, http.MethodPost, base+"/deployments/"+v1.ID.String()+"/rollback", token, nil), http.StatusForbidden, gate.CodeDenied)
	if !strings.Contains(got.Message, "Solo TI vuelve atrás") {
		t.Fatalf("rollback denial %q", got.Message)
	}
	// ... and does not reach another organization.
	_, otherBase := create(stranger, "Otra")
	httpxtest.Want(t, deploy(stranger, otherBase, "9"), http.StatusAccepted)
	// deleting the rule frees the rollback.
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/policy/rules/"+custom.ID.String(), it, nil), http.StatusNoContent)
	httpxtest.Want(t, checked(t, app, http.MethodPost, base+"/deployments/"+v1.ID.String()+"/rollback", token, nil), http.StatusAccepted)

	// Built-ins: switched off, never changed nor deleted.
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/policy/rules/"+prod.ID.String(), it, nil), http.StatusUnprocessableEntity)
	res = checked(t, app, http.MethodPut, "/api/v1/policy/rules/"+prod.ID.String(), it, map[string]any{
		"name": prod.Name, "expression": "true", "effect": "deny"})
	if c := codeOf(t, res); res.Status != http.StatusUnprocessableEntity || c != policy.CodeBuiltin {
		t.Fatalf("changing a built-in → %d %q", res.Status, c)
	}
	// Another organization cannot touch our rules.
	httpxtest.Want(t, checked(t, app, http.MethodPut, "/api/v1/policy/rules/"+prod.ID.String(), stranger, map[string]any{
		"name": prod.Name, "expression": prod.Expression, "effect": "approval"}), http.StatusNotFound)

	// An expression that does not compile is POLICY_INVALID with the engine's words.
	for _, expr := range []string{"action ==", "project.tier", "project.colour == 'red'"} {
		res = checked(t, app, http.MethodPost, "/api/v1/policy/rules", it, map[string]any{"name": "Rota", "expression": expr, "effect": "deny"})
		if c := codeOf(t, res); res.Status != http.StatusUnprocessableEntity || c != policy.CodeInvalid {
			t.Fatalf("%q → %d %q: %s", expr, res.Status, c, res.Body)
		}
	}
	if n := len(list(it)); n != 4 {
		t.Fatalf("an invalid rule was stored: %d rules", n)
	}

	// The dry run decides nothing and says what each rule thought.
	res = checked(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, map[string]any{
		"action": "deploy", "project": map[string]any{"tier": "production"}, "scan": map[string]any{"known": true, "critical": 2}})
	httpxtest.Want(t, res, http.StatusOK)
	eval := httpxtest.JSON[policy.Evaluation](t, res)
	if eval.Effect != gate.Deny || len(eval.Rules) != 1 || eval.Rules[0].Name != "Producción no publica con vulnerabilidades críticas" || len(eval.Results) != 4 {
		t.Fatalf("dry run %+v", eval)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/policy/evaluate", it, map[string]any{"action": "teleport"}), http.StatusBadRequest)
	if n := len(list(it)); n != 4 {
		t.Fatalf("the dry run wrote: %d", n)
	}
	// The other organization has its own, untouched rulebook.
	for _, r := range list(stranger) {
		if !r.Enabled || r.Builtin == nil {
			t.Fatalf("the other organization's rulebook was touched: %+v", r)
		}
	}
}
