package bootstrap_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
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
)

// TestVariablesAndSecrets: 2a end to end over HTTP. Variables and secrets
// are settings revisions applied to the release online; a managed value is
// accepted only sealed (the fake cluster opens it as the agent would, which
// proves the console's binding), versions move forward one at a time, a key
// change fails closed, external secrets need their own permission, and no
// value ever reaches the trail.
func TestVariablesAndSecrets(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{SecretStores: []string{"vault-prod"},
		Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond},
		Cluster:  cluster,
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
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.AuditRead)
	ti := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.SecretsExternal)
	reader := httpxtest.Token(org, permission.ProjectsRead)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Pagos"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	target := release.Target{Organization: org, Project: proj.ID, Environment: env.ID}

	type view struct {
		Revision  int64                `json:"revision"`
		Env       map[string]string    `json:"env"`
		Secrets   []release.SecretView `json:"secrets"`
		SecretKey *release.ClusterKey  `json:"secret_key"`
		External  bool                 `json:"external_secrets"`
		Stores    []string             `json:"secret_stores"`
		Revoked   bool                 `json:"secrets_cluster_revoked"`
	}
	read := func() view {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/settings", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[view](t, res)
	}
	await := func(id release.DeploymentID) {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status == release.StatusReady {
				return
			}
			if d.Status == release.StatusFailed {
				t.Fatalf("deployment failed: %+v", d.Error)
			}
		}
		t.Fatalf("deployment %s is %s", id, d.Status)
	}
	change := func(res httpxtest.Response, want int) release.SettingsChange {
		t.Helper()
		httpxtest.Want(t, res, want)
		return httpxtest.JSON[release.SettingsChange](t, res)
	}
	last := func() release.DesiredApp {
		t.Helper()
		a := cluster.Applied()
		if len(a) == 0 {
			t.Fatal("nothing applied")
		}
		return a[len(a)-1].App
	}

	// The defaults: nothing set, the cluster's key and stores on offer.
	v := read()
	if v.Revision != 0 || len(v.Env) != 0 || len(v.Secrets) != 0 || v.SecretKey == nil || v.SecretKey.KeyID == "" ||
		v.SecretKey.App != "pagos" || !v.External || len(v.Stores) != 1 {
		t.Fatalf("defaults %+v", v)
	}

	// Variables before anything is online are only stored.
	c := change(checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"TZ": "America/Lima", "LOG": "info"}}),
		http.StatusAccepted)
	if c.Deployment != nil || c.Settings.Revision != 1 || c.Settings.Env["TZ"] != "America/Lima" {
		t.Fatalf("first env change %+v", c)
	}
	// The release's env loses to the environment's.
	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080,
		"env": map[string]string{"TZ": "UTC", "MODE": "web"}})
	httpxtest.Want(t, res, http.StatusAccepted)
	await(httpxtest.JSON[release.Deployment](t, res).ID)
	if a := last(); a.Env["TZ"] != "America/Lima" || a.Env["MODE"] != "web" || a.Env["LOG"] != "info" {
		t.Fatalf("applied env %v", a.Env)
	}
	// Online: a change is applied to the same release as a config deployment.
	c = change(checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"LOG": "debug"}, "unset": []string{"TZ"}}),
		http.StatusAccepted)
	if c.Deployment == nil || c.Deployment.Reason != release.ReasonConfig {
		t.Fatalf("online env change %+v", c)
	}
	await(c.Deployment.ID)
	if a := last(); a.Env["LOG"] != "debug" || a.Env["TZ"] != "UTC" {
		t.Fatalf("applied env after change %v", a.Env)
	}
	// The same values again add no revision.
	if c := change(checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"LOG": "debug"}}),
		http.StatusAccepted); c.Deployment != nil || c.Settings.Revision != 2 {
		t.Fatalf("no-op env change %+v", c)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"ODYSSEY_X": "1"}}),
		http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodPatch, base+"/env", reader, map[string]any{"set": map[string]string{"A": "1"}}),
		http.StatusForbidden)

	// A managed secret: requested (nothing reaches the cluster), then loaded.
	res = checked(t, app, http.MethodPut, base+"/secrets/STRIPE_API_KEY", token, map[string]any{"source": "managed", "note": "cobros"})
	httpxtest.Want(t, res, http.StatusOK)
	if s := httpxtest.JSON[release.SecretView](t, res); s.State != release.SecretStateMissing || s.Request == nil || s.Request.Note != "cobros" {
		t.Fatalf("requested %+v", s)
	}
	if v := read(); v.Revision != 2 || len(v.Secrets) != 1 || v.Secrets[0].State != release.SecretStateMissing {
		t.Fatalf("after the request %+v", v)
	}
	load := func(name string, version int64, value string) httpxtest.Response {
		keyID, sealed := cluster.SealFor(target, "pagos", name, version, value)
		return checked(t, app, http.MethodPut, base+"/secrets/"+name+"/value", token,
			map[string]any{"version": version, "key_id": keyID, "sealed": base64.StdEncoding.EncodeToString(sealed)})
	}
	c = change(load("STRIPE_API_KEY", 1, "sk_live_first"), http.StatusAccepted)
	if c.Deployment == nil || c.Settings.Revision != 3 {
		t.Fatalf("load %+v", c)
	}
	await(c.Deployment.ID)
	if got, ok := cluster.Received(target, "pagos", "STRIPE_API_KEY"); !ok || got.Value != "sk_live_first" || got.Version != 1 {
		t.Fatalf("the cluster holds %+v %v", got, ok)
	}
	if a := last(); len(a.Secrets) != 1 || a.Secrets[0].Version != 1 || a.Secrets[0].Name != "STRIPE_API_KEY" {
		t.Fatalf("applied secrets %+v", a.Secrets)
	}
	if v := read(); len(v.Secrets) != 1 || v.Secrets[0].State != release.SecretStateLoaded || v.Secrets[0].Request != nil {
		t.Fatalf("loaded %+v", v.Secrets)
	}
	// Versions move one at a time: a stale page cannot overwrite a newer value.
	httpxtest.Want(t, load("STRIPE_API_KEY", 1, "sk_live_stale"), http.StatusConflict)
	httpxtest.Want(t, load("STRIPE_API_KEY", 3, "sk_live_ahead"), http.StatusConflict)
	// A value sealed for another name cannot be passed off as this one.
	keyID, sealed := cluster.SealFor(target, "pagos", "OTHER", 2, "x")
	res = checked(t, app, http.MethodPut, base+"/secrets/STRIPE_API_KEY/value", token,
		map[string]any{"version": 2, "key_id": keyID, "sealed": base64.StdEncoding.EncodeToString(sealed)})
	if res.Status < 400 {
		t.Fatalf("a value sealed for another secret was accepted: %d", res.Status)
	}
	// Nor one sealed for another app (the control plane cannot redirect it).
	keyID, sealed = cluster.SealFor(target, "otra-app", "STRIPE_API_KEY", 2, "x")
	res = checked(t, app, http.MethodPut, base+"/secrets/STRIPE_API_KEY/value", token,
		map[string]any{"version": 2, "key_id": keyID, "sealed": base64.StdEncoding.EncodeToString(sealed)})
	if res.Status < 400 {
		t.Fatalf("a value sealed for another app was accepted: %d", res.Status)
	}
	// A variable and a secret cannot share a name.
	httpxtest.Want(t, checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"STRIPE_API_KEY": "x"}}),
		http.StatusBadRequest)

	// The cluster's key changed (its agent was reinstalled): fail closed.
	cluster.RotateKey()
	if v := read(); v.Secrets[0].State != release.SecretStateReload {
		t.Fatalf("after the key changed %+v", v.Secrets)
	}
	res = checked(t, app, http.MethodPut, base+"/secrets/STRIPE_API_KEY/value", token,
		map[string]any{"version": 2, "key_id": "stale-key-id", "sealed": base64.StdEncoding.EncodeToString(sealed)})
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if code := httpxtest.ErrorCode(res); code != release.CodeSecretKeyChanged {
		t.Fatalf("stale key %s", code)
	}
	c = change(load("STRIPE_API_KEY", 2, "sk_live_second"), http.StatusAccepted)
	await(c.Deployment.ID)
	if got, _ := cluster.Received(target, "pagos", "STRIPE_API_KEY"); got.Value != "sk_live_second" || got.Version != 2 {
		t.Fatalf("after reloading %+v", got)
	}

	// A value that did not reach the cluster shows as failed, with why,
	// until a newer one is loaded.
	cluster.Fail(target, "pagos", "STRIPE_API_KEY", 2, "el clúster no pudo guardarlo")
	if v := read(); v.Secrets[0].State != release.SecretStateFailed || v.Secrets[0].Failure == nil ||
		v.Secrets[0].Failure.Error != "el clúster no pudo guardarlo" || v.Secrets[0].NextVersion != 3 {
		t.Fatalf("failed %+v", v.Secrets)
	}
	c = change(load("STRIPE_API_KEY", 3, "sk_live_third"), http.StatusAccepted)
	await(c.Deployment.ID)
	if v := read(); v.Secrets[0].State != release.SecretStateLoaded || v.Secrets[0].Failure != nil {
		t.Fatalf("after loading again %+v", v.Secrets)
	}

	// Removed and added again, it keeps counting: the cluster may still hold
	// version 3, and a "version 1" would be taken for an old value.
	c = change(checked(t, app, http.MethodDelete, base+"/secrets/STRIPE_API_KEY", token, nil), http.StatusAccepted)
	await(c.Deployment.ID)
	res = checked(t, app, http.MethodPut, base+"/secrets/STRIPE_API_KEY", token, map[string]any{"source": "managed"})
	httpxtest.Want(t, res, http.StatusOK)
	if s := httpxtest.JSON[release.SecretView](t, res); s.State != release.SecretStateMissing || s.NextVersion != 4 {
		t.Fatalf("requested again %+v", s)
	}
	httpxtest.Want(t, load("STRIPE_API_KEY", 1, "sk_live_reused"), http.StatusConflict)
	c = change(load("STRIPE_API_KEY", 4, "sk_live_fourth"), http.StatusAccepted)
	await(c.Deployment.ID)
	if got, _ := cluster.Received(target, "pagos", "STRIPE_API_KEY"); got.Value != "sk_live_fourth" || got.Version != 4 {
		t.Fatalf("after adding again %+v", got)
	}

	// External secrets need TI's permission, a known store and a key.
	external := map[string]any{"source": "external", "store": "vault-prod", "key": "apps/pagos/db", "property": "password"}
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/secrets/DB_PASSWORD", token, external), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/secrets/DB_PASSWORD", ti,
		map[string]any{"source": "external", "store": "vault-dev", "key": "apps/pagos/db"}), http.StatusBadRequest)
	c = change(checked(t, app, http.MethodPut, base+"/secrets/DB_PASSWORD", ti, external), http.StatusAccepted)
	await(c.Deployment.ID)
	if a := last(); len(a.Secrets) != 2 || a.Secrets[0].Name != "DB_PASSWORD" || a.Secrets[0].Store != "vault-prod" || a.Secrets[0].Property != "password" {
		t.Fatalf("applied external %+v", a.Secrets)
	}
	// A builder can neither replace TI's external secret with a value nor
	// remove it: both need TI's permission. Removing one is a revision too.
	httpxtest.Want(t, load("DB_PASSWORD", 1, "hunter2"), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, base+"/secrets/DB_PASSWORD", token, nil), http.StatusForbidden)
	c = change(checked(t, app, http.MethodDelete, base+"/secrets/DB_PASSWORD", ti, nil), http.StatusAccepted)
	await(c.Deployment.ID)
	if a := last(); len(a.Secrets) != 1 || a.Secrets[0].Name != "STRIPE_API_KEY" {
		t.Fatalf("after removing %+v", a.Secrets)
	}
	httpxtest.Want(t, checked(t, app, http.MethodDelete, base+"/secrets/DB_PASSWORD", token, nil), http.StatusNotFound)

	// Another organization sees none of it and changes nothing.
	stranger := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead, permission.ProjectsWrite)
	httpxtest.Want(t, checked(t, app, http.MethodGet, base+"/settings", stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPatch, base+"/env", stranger, map[string]any{"set": map[string]string{"A": "1"}}), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/secrets/X", stranger, map[string]any{"source": "managed"}), http.StatusNotFound)

	// The trail has every change and no value.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		res := checked(t, app, http.MethodGet, "/api/v1/audit?limit=100", token, nil)
		trail := httpxtest.JSON[query.Paginated[audit.Entry]](t, res)
		var envs, secrets int
		for _, e := range trail.Items {
			for _, value := range []string{"sk_live", "America/Lima", "debug", "apps/pagos/db"} {
				if strings.Contains(string(e.Payload), value) || strings.Contains(e.Summary, value) {
					t.Fatalf("%s carries %q: %s", e.Type, value, e.Payload)
				}
			}
			switch e.Type {
			case release.EventEnvChanged:
				envs++
			case release.EventSecretChanged:
				secrets++
			}
		}
		// env: 2 changes; secret: requested, loaded ×3, removed, requested,
		// loaded, external, removed
		if envs == 2 && secrets == 9 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trail: %d env.changed, %d secret.changed", envs, secrets)
		}
	}

	// The 100 names of an environment are its variables, secrets and open
	// requests together, in every path that adds one.
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/secrets/PENDING", token, map[string]any{"source": "managed"}), http.StatusOK)
	fill := map[string]string{}
	for i := range 97 { // LOG + STRIPE_API_KEY + PENDING + 97 = 100
		fill[fmt.Sprintf("FILL_%02d", i)] = "x"
	}
	c = change(checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": fill}), http.StatusAccepted)
	await(c.Deployment.ID)
	const tooMany = "at most 100 variables, secrets and pending requests"
	refused := func(what string, res httpxtest.Response) {
		t.Helper()
		httpxtest.Want(t, res, http.StatusBadRequest)
		if !strings.Contains(string(res.Body), tooMany) {
			t.Errorf("%s: %s", what, res.Body)
		}
	}
	refused("a variable", checked(t, app, http.MethodPatch, base+"/env", token, map[string]any{"set": map[string]string{"ONE_MORE": "x"}}))
	refused("a request", checked(t, app, http.MethodPut, base+"/secrets/ONE_MORE", token, map[string]any{"source": "managed"}))
	refused("an external secret", checked(t, app, http.MethodPut, base+"/secrets/ONE_MORE", ti, external))
	refused("a loaded secret", load("ONE_MORE", 1, "x"))
	// the request of a name that becomes a secret takes no second place
	c = change(load("PENDING", 1, "sk_pending"), http.StatusAccepted)
	await(c.Deployment.ID)
	if v := read(); len(v.Env)+len(v.Secrets) != 100 {
		t.Fatalf("after answering the request: %d variables, %d secrets", len(v.Env), len(v.Secrets))
	}

	// A revoked cluster offers nothing, and says why.
	cluster.Revoke()
	if v := read(); !v.Revoked || v.SecretKey != nil || v.External {
		t.Fatalf("revoked %+v", v)
	}
}
