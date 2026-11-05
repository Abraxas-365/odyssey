package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifake"
	"github.com/Abraxas-365/odyssey/internal/ai/aisvc"
	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"github.com/jmoiron/sqlx"
)

type aiHarness struct {
	cluster  *releasefake.Cluster
	gateway  *aifake.Provider
	base     string
	target   release.Target
	token    string
	reader   string
	stranger string
	db       *sqlx.DB
	platform *bootstrap.Platform
}

// aiStack assembles the platform with a fake cluster and a fake AI gateway
// and a worker, and creates an app that is online.
func aiStack(t *testing.T, cluster *releasefake.Cluster, gateway ai.Provider) (*aiHarness, func(method, path, token string, body any) httpxtest.Response) {
	t.Helper()
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:   workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 30 * time.Millisecond }},
		Cluster:    cluster,
		AIProvider: gateway,
		AIService:  aisvc.Options{BaseURL: "https://llm.test", PollEvery: 30 * time.Millisecond, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	t.Cleanup(func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	})
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	h := &aiHarness{cluster: cluster, db: db, platform: platform,
		token:    httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate, permission.AuditRead),
		reader:   httpxtest.Token(org, permission.ProjectsRead),
		stranger: httpxtest.Token(other, permission.ProjectsRead, permission.ProjectsWrite)}
	h.gateway, _ = gateway.(*aifake.Provider)
	do := func(method, path, token string, body any) httpxtest.Response {
		t.Helper()
		return checked(t, app, method, path, token, body)
	}
	res := do(http.MethodPost, "/api/v1/projects", h.token, map[string]any{"name": "Pagos"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = do(http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", h.token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	h.base = "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	h.target = release.Target{Organization: org, Project: proj.ID, Environment: env.ID}
	// online: AI changes then apply as config deployments
	res = do(http.MethodPost, h.base+"/deployments", h.token, map[string]any{"image": image("1"), "port": 8080})
	httpxtest.Want(t, res, http.StatusAccepted)
	id := httpxtest.JSON[release.Deployment](t, res).ID.String()
	eventually(t, "the first deployment is ready", func() bool {
		d := httpxtest.JSON[release.Deployment](t, do(http.MethodGet, h.base+"/deployments/"+id, h.token, nil))
		return d.Status == release.StatusReady
	})
	return h, do
}

func codeOf(t *testing.T, res httpxtest.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code + body.Code
}

// TestAI: 2d-ai end to end over HTTP and the worker, with a fake cluster and
// a fake gateway. Enabling makes a key, seals it to the cluster, wires the
// app (a config deployment); the four variables are reserved; rotating
// replaces the key and revokes the old one only once the app runs with the
// new; disabling revokes everything; a crash after the gateway made a key but
// before it was saved leaves no orphan; an organization or cluster without AI
// is told so.
func TestAI(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, do := aiStack(t, cluster, aifake.New())
	gw := h.gateway
	url := h.base + "/ai"
	view := func(token string) ai.View {
		t.Helper()
		res := do(http.MethodGet, url, token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[ai.View](t, res)
	}
	last := func() release.DesiredApp {
		t.Helper()
		a := cluster.Applied()
		if len(a) == 0 {
			t.Fatal("nothing applied")
		}
		return a[len(a)-1].App
	}
	state := func(want string) func() bool { return func() bool { return view(h.token).Status == want } }

	// off and available
	if v := view(h.token); !v.Available || v.Enabled || v.Status != ai.StateOff || v.BaseURL != "https://llm.test" || len(v.Variables) != 4 {
		t.Fatalf("initial view %+v", v)
	}
	httpxtest.Want(t, do(http.MethodGet, url, h.stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodPut, url, h.reader, nil), http.StatusForbidden)
	httpxtest.Want(t, do(http.MethodPost, url+"/rotate", h.token, nil), http.StatusConflict) // not enabled
	httpxtest.Want(t, do(http.MethodDelete, url, h.token, nil), http.StatusAccepted)         // off already: nothing

	// a variable the AI sets, already defined: refused, nothing made
	httpxtest.Want(t, do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"set": map[string]string{"OPENAI_API_KEY": "mine"}}), http.StatusAccepted)
	res := do(http.MethodPut, url, h.token, nil)
	httpxtest.Want(t, res, http.StatusConflict)
	if c := codeOf(t, res); c != release.CodeVariableTaken || len(gw.Live()) != 0 {
		t.Fatalf("code %q, keys %v", c, gw.Live())
	}
	httpxtest.Want(t, do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"unset": []string{"OPENAI_API_KEY"}}), http.StatusAccepted)

	// enable
	res = do(http.MethodPut, url, h.token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	if v := httpxtest.JSON[ai.View](t, res); v.Status != ai.StateEnabling {
		t.Fatalf("enabling view %+v", v)
	}
	httpxtest.Want(t, do(http.MethodPut, url, h.token, nil), http.StatusAccepted) // asking again changes nothing
	eventually(t, "enabled", state(ai.StateEnabled))
	v := view(h.token)
	if !v.Enabled || v.EnabledBy == nil || v.EnabledAt == nil {
		t.Fatalf("enabled view %+v", v)
	}
	live := gw.Live()
	if len(live) != 1 {
		t.Fatalf("keys at the gateway: %v", live)
	}
	eventually(t, "the config deployment applied the AI", func() bool { return last().AI != nil })
	d := last()
	if d.AI == nil || d.AI.BaseURL != "https://llm.test" || d.AI.KeySecret != release.AIKeySecret {
		t.Fatalf("desired AI %+v", d.AI)
	}
	if !slices.ContainsFunc(d.Secrets, func(s release.DesiredSecret) bool {
		return s.Name == release.AIKeySecret && s.Source == release.SecretManaged
	}) {
		t.Fatalf("the key is not a managed secret of the app: %+v", d.Secrets)
	}
	got, ok := cluster.Received(h.target, "pagos", release.AIKeySecret)
	if !ok || !gw.Valid(got.Value) || got.Value != gw.Secret(live[0]) {
		t.Fatalf("the cluster did not receive the live key: %+v %v", got, ok)
	}
	first := got

	// the four variables are reserved while it is on, and the AI key is not a person's to set
	for _, name := range ai.Variables {
		res = do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"set": map[string]string{name: "x"}})
		if res.Status != http.StatusConflict || codeOf(t, res) != release.CodeVariableTaken {
			t.Errorf("setting %s while AI is on: %d %s", name, res.Status, res.Body)
		}
	}
	if res := do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"set": map[string]string{"ODICEA_AI_KEY": "x"}}); res.Status < 400 {
		t.Errorf("ODICEA_AI_KEY accepted: %d", res.Status)
	}

	// rotate: a new key at a newer version; the old one is revoked after
	httpxtest.Want(t, do(http.MethodPost, url+"/rotate", h.token, nil), http.StatusAccepted)
	eventually(t, "rotated", func() bool {
		cur, _ := cluster.Received(h.target, "pagos", release.AIKeySecret)
		return cur.Version > first.Version && view(h.token).Status == ai.StateEnabled && len(gw.Live()) == 1
	})
	cur, _ := cluster.Received(h.target, "pagos", release.AIKeySecret)
	if !gw.Valid(cur.Value) || gw.Valid(first.Value) || last().AI == nil {
		t.Fatalf("after rotating: new valid=%v old valid=%v", gw.Valid(cur.Value), gw.Valid(first.Value))
	}

	// disable: the app stops using it, then the keys are revoked
	res = do(http.MethodDelete, url, h.token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	eventually(t, "disabled", func() bool { return view(h.token).Status == ai.StateOff && len(gw.Live()) == 0 })
	if last().AI != nil || gw.Valid(cur.Value) {
		t.Fatalf("after disabling: AI %+v, key valid %v", last().AI, gw.Valid(cur.Value))
	}
	httpxtest.Want(t, do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"set": map[string]string{"OPENAI_API_KEY": "mine"}}), http.StatusAccepted) // free again

	// crash after the gateway made the key and before the run saved it: no orphan
	httpxtest.Want(t, do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"unset": []string{"OPENAI_API_KEY"}}), http.StatusAccepted)
	gw.FailOnce("created", errx.External("connection lost"))
	httpxtest.Want(t, do(http.MethodPut, url, h.token, nil), http.StatusAccepted)
	eventually(t, "enabled after the crash", state(ai.StateEnabled))
	if live := gw.Live(); len(live) != 1 {
		t.Fatalf("an orphan key stayed at the gateway: %v", live)
	}

	// the gateway down while enabling: it retries, nothing is lost; disable cleans up
	httpxtest.Want(t, do(http.MethodDelete, url, h.token, nil), http.StatusAccepted)
	eventually(t, "off again", func() bool { return view(h.token).Status == ai.StateOff && len(gw.Live()) == 0 })

	// the trail records the lifecycle and never a key
	res = do(http.MethodGet, "/api/v1/audit?limit=100", h.token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	types := map[string]bool{}
	for _, e := range httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Items {
		types[e.Type] = true
	}
	for _, want := range []string{ai.EventEnabled, ai.EventRotated, ai.EventDisabled} {
		if !types[want] {
			t.Errorf("no %s in the audit trail: %v", want, types)
		}
	}
	if contains(res.Body, "ik_svc_fake") || contains(res.Body, first.Value) {
		t.Error("a key reached the audit trail")
	}
}

func contains(b []byte, s string) bool { return s != "" && strings.Contains(string(b), s) }

// TestAIUnavailable: without a gateway, or on a cluster whose agent cannot wire
// the AI, the console is told and turning it on is a 422 AI_UNAVAILABLE.
func TestAIUnavailable(t *testing.T) {
	behavior := func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }
	for name, c := range map[string]struct {
		cluster *releasefake.Cluster
		gateway ai.Provider
		status  int
		code    string
	}{
		"no gateway":    {releasefake.New(releasefake.Options{Behavior: behavior}), nil, http.StatusUnprocessableEntity, ai.CodeUnavailable},
		"old agent":     {releasefake.New(releasefake.Options{Behavior: behavior, NoAI: true}), aifake.New(), http.StatusUnprocessableEntity, ai.CodeUnavailable},
		"no secret key": {releasefake.New(releasefake.Options{Behavior: behavior, NoSecrets: true}), aifake.New(), http.StatusUnprocessableEntity, ai.CodeSecretsUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h, do := aiStack(t, c.cluster, c.gateway)
			res := do(http.MethodGet, h.base+"/ai", h.token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if v := httpxtest.JSON[ai.View](t, res); v.Available || v.Message == "" || v.Status != ai.StateOff {
				t.Fatalf("view %+v", v)
			}
			res = do(http.MethodPut, h.base+"/ai", h.token, nil)
			httpxtest.Want(t, res, c.status)
			if got := codeOf(t, res); got != c.code {
				t.Fatalf("code %q, want %q: %s", got, c.code, res.Body)
			}
			if h.gateway != nil && len(h.gateway.Live()) != 0 {
				t.Fatalf("a key was made: %v", h.gateway.Live())
			}
		})
	}
}

// TestAIRevocationFailureIsRecoverable: a disable whose revocation cannot reach
// the gateway does not stay "disabling" forever. It ends as revoke_failed with
// a warning and the reason (the console offers a retry), the sweep revokes it
// once the gateway is back, and the app may turn the AI on again meanwhile
// (the new key is independent of the one waiting for revocation).
func TestAIRevocationFailureIsRecoverable(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, do := aiStack(t, cluster, aifake.New())
	gw, url := h.gateway, h.base+"/ai"
	view := func() ai.View {
		t.Helper()
		res := do(http.MethodGet, url, h.token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[ai.View](t, res)
	}
	state := func(want string) func() bool { return func() bool { return view().Status == want } }

	httpxtest.Want(t, do(http.MethodPut, url, h.token, nil), http.StatusAccepted)
	eventually(t, "enabled", state(ai.StateEnabled))
	if len(gw.Live()) != 1 {
		t.Fatalf("keys %v", gw.Live())
	}

	// the gateway goes down, then the AI is turned off
	gw.Down(errx.External("The AI gateway is not reachable."))
	httpxtest.Want(t, do(http.MethodDelete, url, h.token, nil), http.StatusAccepted)
	eventually(t, "revoke_failed", state(ai.StateRevokeFailed))
	v := view()
	if v.Enabled || v.Warning != ai.WarningRevokeFailed || v.RevokeError != "The AI gateway is not reachable." {
		t.Fatalf("view %+v", v)
	}
	if last := cluster.Applied(); last[len(last)-1].App.AI != nil {
		t.Fatal("the app still has the AI")
	}
	if len(gw.Live()) != 1 {
		t.Fatalf("the key was revoked while the gateway was down: %v", gw.Live())
	}
	org := h.target.Organization
	events := func(typ string) int {
		t.Helper()
		var n int
		if err := h.db.Get(&n, `SELECT count(*) FROM event.outbox WHERE organization_id = $1 AND type = $2`, org, typ); err != nil {
			t.Fatal(err)
		}
		return n
	}
	attempts := func() int {
		t.Helper()
		var n int
		if err := h.db.Get(&n, `SELECT coalesce(max(revoke_attempts), 0) FROM ai.keys WHERE environment_id = $1 AND status = 'revoke_failed'`,
			h.target.Environment); err != nil {
			t.Fatal(err)
		}
		return n
	}
	due := func() { // the wait of the backoff is over
		t.Helper()
		if _, err := h.db.Exec(`UPDATE ai.keys SET next_retry_at = now() - interval '1 minute' WHERE status = 'revoke_failed'`); err != nil {
			t.Fatal(err)
		}
	}
	if events(ai.EventRevokeFailed) != 1 || attempts() != 1 {
		t.Fatalf("first failure: %d events, %d attempts", events(ai.EventRevokeFailed), attempts())
	}

	// the sweep waits for the backoff: right away it has nothing to retry
	if n, err := h.platform.AI.Commands.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep before the wait is over: %d %v", n, err)
	}
	// once due it retries, publishes the retry, and failing again is not
	// published as a new failure (only the first one is)
	due()
	if n, err := h.platform.AI.Commands.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	eventually(t, "failed again", func() bool { return attempts() == 2 && view().Status == ai.StateRevokeFailed })
	if got := events(ai.EventRevocationRetried); got != 1 {
		t.Errorf("%d ai.revocation_retried events, want 1", got)
	}
	if got := events(ai.EventRevokeFailed); got != 1 {
		t.Errorf("a failed retry published ai.revoke_failed again: %d", got)
	}
	if n, _ := h.platform.AI.Commands.Sweep(context.Background()); n != 0 {
		t.Errorf("the sweep did not back off: %d", n)
	}

	// the gateway is back: turning it on is allowed while the old key waits
	gw.Down(nil)
	httpxtest.Want(t, do(http.MethodPut, url, h.token, nil), http.StatusAccepted)
	eventually(t, "enabled again", state(ai.StateEnabled))
	if v := view(); v.Warning != ai.WarningRevokeFailed {
		t.Errorf("the pending revocation is not shown: %+v", v)
	}
	if len(gw.Live()) != 2 {
		t.Fatalf("want the failed key and the new one: %v", gw.Live())
	}

	// the sweep revokes the failed key and leaves the new one alone
	due()
	n, err := h.platform.AI.Commands.Sweep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	eventually(t, "the old key revoked", func() bool { return len(gw.Live()) == 1 && view().Warning == "" })
	if v := view(); v.Status != ai.StateEnabled || !v.Enabled {
		t.Fatalf("the sweep disturbed the new key: %+v", v)
	}
	if n, _ := h.platform.AI.Commands.Sweep(context.Background()); n != 0 {
		t.Errorf("nothing was left to sweep: %d", n)
	}

	// failing again, and this time the person retries by turning it off again
	gw.Down(errx.External("The AI gateway is not reachable."))
	httpxtest.Want(t, do(http.MethodDelete, url, h.token, nil), http.StatusAccepted)
	eventually(t, "revoke_failed again", state(ai.StateRevokeFailed))
	gw.Down(nil)
	httpxtest.Want(t, do(http.MethodDelete, url, h.token, nil), http.StatusAccepted) // "Reintentar"
	eventually(t, "off", func() bool { return view().Status == ai.StateOff && len(gw.Live()) == 0 })
}

// Without a gateway connected, turning the AI off still takes the settings
// from the app and leaves the keys marked for revocation, with the warning.
func TestAIDisableWithoutGateway(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, do := aiStack(t, cluster, nil)
	url := h.base + "/ai"
	var env, org string
	env, org = h.target.Environment.String(), h.target.Organization.String()
	_, err := h.db.Exec(`INSERT INTO ai.keys (id, organization_id, project_id, environment_id, provider_key_id, name, status, created_by)
		VALUES ($1, $2, $3, $4, 'sa-1', $5, 'active', $6)`, ai.NewKeyID(), org, h.target.Project.String(), env,
		"odicea-"+env+"-orphan", identity.NewOrganizationID().String())
	if err != nil {
		t.Fatal(err)
	}
	res := do(http.MethodDelete, url, h.token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	v := httpxtest.JSON[ai.View](t, res)
	if v.Status != ai.StateRevokeFailed || v.Warning != ai.WarningRevokeFailed || v.RevokeError != ai.ReasonNoGateway {
		t.Fatalf("view %+v", v)
	}
	// still there, still failed: no gateway, the sweep does nothing
	if n, err := h.platform.AI.Commands.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if got := httpxtest.JSON[ai.View](t, do(http.MethodGet, url, h.token, nil)); got.Status != ai.StateRevokeFailed {
		t.Fatalf("view %+v", got)
	}
}

// A revocation that failed for an environment that no longer exists (its
// project is gone) does not stay revoke_failed forever: the sweep revokes the
// key at the gateway from the ids the row holds, with no app to wait for or
// to update, and drops the row.
func TestAISweepRevokesTheKeysOfAnEnvironmentThatIsGone(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, _ := aiStack(t, cluster, aifake.New())
	gw, ctx := h.gateway, context.Background()
	org, goneProject, goneEnv := h.target.Organization, project.NewID(), project.NewEnvironmentID()
	id := ai.NewKeyID()
	name := ai.KeyName(goneEnv, id)
	pk, err := gw.CreateKey(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO ai.keys (id, organization_id, project_id, environment_id, provider_key_id, name, status, created_by, revoke_error, revoke_attempts)
		VALUES ($1, $2, $3, $4, $5, $6, 'revoke_failed', $7, 'The AI gateway is not reachable.', 1)`,
		id, org.String(), goneProject.String(), goneEnv.String(), pk.ID, name, identity.NewOrganizationID().String()); err != nil {
		t.Fatal(err)
	}
	if n, err := h.platform.AI.Commands.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	eventually(t, "the key revoked and its row gone", func() bool {
		var rows int
		if err := h.db.Get(&rows, `SELECT count(*) FROM ai.keys WHERE environment_id = $1`, goneEnv.String()); err != nil {
			t.Fatal(err)
		}
		return rows == 0 && !slices.Contains(gw.Live(), name)
	})
	var retried int
	if err := h.db.Get(&retried, `SELECT count(*) FROM event.outbox WHERE organization_id = $1 AND type = $2 AND subject = $3`,
		org.String(), ai.EventRevocationRetried, ai.EnvironmentSubjectKind+"/"+goneEnv.String()); err != nil || retried != 1 {
		t.Fatalf("retry events: %d %v", retried, err)
	}
	if n, _ := h.platform.AI.Commands.Sweep(ctx); n != 0 {
		t.Errorf("nothing was left to sweep: %d", n)
	}
}
