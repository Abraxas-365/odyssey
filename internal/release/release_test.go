package release_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	prev := release.MustParseID("00000000-0000-4000-8000-0000000000c0")
	r := release.Release{ID: release.MustParseID("00000000-0000-4000-8000-0000000000c1"), Organization: org,
		Project: project.MustParseID("00000000-0000-4000-8000-0000000000aa"), Image: "registry.local/visitas@" + digest,
		Config: release.Config{Port: 8080, HealthPath: "/health", Replicas: 2, Env: map[string]string{"TZ": "America/Lima", "LOG": "debug"}}}
	failed := release.MustParseDeploymentID("00000000-0000-4000-8000-0000000000d0")
	d := release.Deployment{ID: release.MustParseDeploymentID("00000000-0000-4000-8000-0000000000d1"), Organization: org,
		Project: r.Project, Environment: project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb"),
		Release: &r.ID, Image: r.Image, Reason: release.ReasonAutoRollback, Step: release.StepWaitReady, Revision: 7,
		Previous: &prev, RollbackOf: &failed,
		Error: &release.Failure{Code: release.CodeUnhealthy, Message: "La app arrancó pero no responde en su ruta de salud.", Detail: "GET /health 503"}}
	restore := release.MustParseDeploymentID("00000000-0000-4000-8000-0000000000d2")
	n := release.Names{Project: "Visitas", Environment: "production"}
	ticket := release.UploadTicket{ID: release.MustParseUploadTicketID("00000000-0000-4000-8000-0000000000e1"), Organization: org,
		Project: r.Project, Environment: d.Environment, Token: "odu_secret", TokenHash: []byte("secret-hash"), Client: "mcp:claude",
		RequestedBy: user, ExpiresAt: time.Date(2026, 11, 3, 12, 10, 0, 0, time.UTC)}
	for _, e := range []event.Event{
		release.NewCreated(org, user, r, n),
		release.NewDeploymentStarted(org, user, d, n),
		release.NewDeploymentReady(org, user, d, "https://visitas.apps.example.com", n),
		release.NewDeploymentFailed(org, user, d, &restore, n),
		release.NewDeploymentCancelled(org, user, d, restore, n),
		release.NewUploadTicketIssued(ticket, n),
		release.NewUploadTicketUsed(ticket, n),
		release.NewAccessChanged(org, user, r.Project.String(), release.DefaultSettings(org, d.Environment),
			release.Settings{Environment: d.Environment, Organization: org, Revision: 3, Access: release.AccessSettings{Mode: release.AccessOrganization}}, &restore, n),
		release.NewEnvChanged(org, user, r.Project.String(), release.Settings{Environment: d.Environment, Organization: org, Revision: 4,
			Env: map[string]string{"LOG_LEVEL": "debug", "TZ": "America/Lima"}}, []string{"TZ", "LOG_LEVEL"}, []string{"OLD"}, &restore, n),
		release.NewSecretChanged(org, user, r.Project.String(), release.Settings{Environment: d.Environment, Revision: 5}, release.SecretRef{Name: "STRIPE_API_KEY",
			Source: release.SecretManaged, Version: 2, KeyID: "k3yIdK3yIdK3yId0"}, release.SecretChangeLoaded, &restore, n),
		release.NewResourcesChanged(org, user, r.Project.String(), release.Settings{Environment: d.Environment, Organization: org, Revision: 6},
			release.ResourceConnected, release.ResourceUse{Name: "db", Kind: resourcekind.Postgres, Prefix: "ANALYTICS"}, &restore, n),
		release.NewAIChanged(org, user, r.Project.String(), release.Settings{Environment: d.Environment, Organization: org, Revision: 7,
			AI: release.AISettings{Enabled: true, KeyVersion: 2, KeyID: "k3yIdK3yIdK3yId0", BaseURL: "https://llm.example.com"}}, &restore, n),
	} {
		eventtest.Golden(t, e)
	}
}

// TestSecretChangesNeverCarryAValue: every kind of change names the secret,
// says what changed, and has nothing a value could hide in.
func TestSecretChangesNeverCarryAValue(t *testing.T) {
	org, user := identity.NewOrganizationID(), identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	env := project.NewEnvironmentID()
	n := release.Names{Project: "Visitas", Environment: "production"}
	for change, ref := range map[string]release.SecretRef{
		release.SecretChangeRequested: {Name: "STRIPE_API_KEY", Source: release.SecretManaged},
		release.SecretChangeLoaded:    {Name: "STRIPE_API_KEY", Source: release.SecretManaged, Version: 1, KeyID: "k"},
		release.SecretChangeExternal:  {Name: "DB_PASSWORD", Source: release.SecretExternal, Store: "vault-prod", Key: "apps/db"},
		release.SecretChangeRemoved:   {Name: "DB_PASSWORD", Source: release.SecretExternal},
	} {
		e := release.NewSecretChanged(org, user, "p", release.Settings{Environment: env, Revision: 1}, ref, change, nil, n)
		var got release.SecretChanged
		if err := json.Unmarshal(e.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if e.Type != release.EventSecretChanged || got.Change != change || got.Name != ref.Name || e.Summary == "" ||
			!strings.Contains(e.Summary, ref.Name) {
			t.Errorf("%s: %+v %q", change, got, e.Summary)
		}
	}
}

func TestEnvValuesStayOutOfTheTrail(t *testing.T) {
	r := release.Release{ID: release.NewID(), Image: "r.local/a@" + digest,
		Config: release.Config{Port: 80, Env: map[string]string{"API_KEY": "s3cr3t"}}}
	e := release.NewCreated(identity.NewOrganizationID(), identity.UserID{}, r, release.Names{Project: "A"})
	if strings.Contains(string(e.Payload), "s3cr3t") || !strings.Contains(string(e.Payload), "API_KEY") {
		t.Fatalf("payload %s", e.Payload)
	}
}

func TestDeployValidation(t *testing.T) {
	ok := release.Deploy{Image: "ghcr.io/inedge/visitas@" + digest, Config: release.Config{Port: 3000}}.Normalize()
	if err := ok.Validate(); err != nil || ok.HealthPath != "/" || ok.Replicas != 1 || ok.Env == nil {
		t.Fatalf("%+v %v", ok, err)
	}
	// No port: the platform's default, the same one the agent puts in $PORT.
	if d := (release.Deploy{Image: ok.Image}).Normalize(); d.Validate() != nil || d.Port != release.DefaultPort || release.DefaultPort != 8080 {
		t.Fatalf("default port: %+v", d)
	}
	for _, c := range []release.Config{
		release.DeploySource{}.Normalize().Config,
		release.DeployUploaded{}.Normalize().Config,
	} {
		if c.Port != release.DefaultPort {
			t.Fatalf("default port: %+v", c)
		}
	}
	for field, d := range map[string]release.Deploy{
		"image":       {Image: "ghcr.io/inedge/visitas:latest", Config: release.Config{Port: 3000}},
		"port":        {Image: ok.Image, Config: release.Config{Port: 70000}},
		"health_path": {Image: ok.Image, Config: release.Config{Port: 1, HealthPath: "health"}},
		"replicas":    {Image: ok.Image, Config: release.Config{Port: 1, Replicas: 11}},
		"env":         {Image: ok.Image, Config: release.Config{Port: 1, Env: map[string]string{"ODYSSEY_URL": "x"}}},
	} {
		err := d.Normalize().Validate()
		var e *errx.Error
		if !errx.As(err, &e) || e.Details["field"] != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, img := range []string{"localhost:5000/app@" + digest, "registry.local/team/app@" + digest} {
		if !release.ValidImage(img) {
			t.Errorf("%s rejected", img)
		}
	}
	for _, img := range []string{"app@" + digest, "r.local/app@sha256:abc", "R.local/app@" + digest, "r.local/app:1@" + digest + " "} {
		if release.ValidImage(img) {
			t.Errorf("%s accepted", img)
		}
	}
}

func TestProgress(t *testing.T) {
	steps := func(d release.Deployment) string {
		var out []string
		for _, s := range d.WithProgress().Steps {
			out = append(out, string(s.Status))
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		status release.Status
		step   string
		want   string
	}{
		{release.StatusPending, release.StepApply, "pending,pending,pending"},
		{release.StatusApplying, release.StepApply, "running,pending,pending"},
		{release.StatusVerifying, release.StepWaitReady, "done,running,pending"},
		{release.StatusVerifying, release.StepPromote, "done,done,running"},
		{release.StatusFailed, release.StepPromote, "done,done,failed"},
		{release.StatusReady, release.StepPromote, "done,done,done"},
		{release.StatusFailed, release.StepWaitReady, "done,failed,skipped"},
		{release.StatusCancelled, release.StepApply, "skipped,skipped,skipped"},
	} {
		if got := steps(release.Deployment{Status: c.status, Step: c.step}); got != c.want {
			t.Errorf("%s at %s: %s, want %s", c.status, c.step, got, c.want)
		}
	}
}

func TestAppName(t *testing.T) {
	prod := project.Environment{Slug: "production", Kind: project.KindProduction}
	stg := project.Environment{Slug: "staging", Kind: project.KindStaging}
	if release.AppName("visitas", prod) != "visitas" || release.AppName("visitas", stg) != "visitas-staging" {
		t.Fatal("names")
	}
	long := strings.Repeat("a", 40)
	if n := release.AppName(long, project.Environment{Slug: strings.Repeat("b", 21) + "-c", Kind: project.KindPreview}); len(n) > 63 || strings.HasSuffix(n, "-") {
		t.Fatalf("%q", n)
	}
}

func TestFailureOf(t *testing.T) {
	unhealthy := release.Failed(release.CodeUnhealthy, "No responde.")
	for _, c := range []struct {
		step  string
		cause error
		code  string
		msg   string
	}{
		{release.StepWaitReady, unhealthy, release.CodeUnhealthy, "No responde."},
		// OnFailure retried: the engine hands back the cause as text
		{release.StepWaitReady, errors.New(unhealthy.Error()), release.CodeUnhealthy, "No responde."},
		{release.StepWaitReady, errors.New("[PROJECT_ARCHIVED] x"), release.CodeNotReady, "La app no quedó lista a tiempo."},
		{release.StepApply, errors.New("dial tcp: refused"), release.CodeApplyFailed, "No se pudo entregar la app al clúster."},
		{release.StepPromote, errx.Internal("db"), release.CodePromoteFailed, "No se pudo dejar la nueva versión en línea."},
	} {
		if f := release.FailureOf(c.step, c.cause); f.Code != c.code || f.Message != c.msg || f.Detail != "" {
			t.Errorf("%s / %v: %+v", c.step, c.cause, f)
		}
	}
}

func TestUnready(t *testing.T) {
	for _, c := range []struct {
		reason release.FailureReason
		code   string
		msg    string
	}{
		{release.FailureUnhealthy, release.CodeUnhealthy, "no responde en su ruta de salud."},
		{"", release.CodeUnhealthy, "no responde en su ruta de salud"},
		{release.FailureImagePull, release.CodeImagePull, "no pudo descargar la imagen"},
		{release.FailureCrash, release.CodeCrashed, "se cierra al arrancar."},
		{release.FailureConfig, release.CodeApplyFailed, "no pudo aplicar la configuración"},
		{release.FailureResource, release.CodeResourceNotReady, "Un recurso de la app (base de datos, caché o archivos) no estuvo listo a tiempo."},
	} {
		err := release.Unready(release.AppStatus{Phase: release.PhaseFailed, Reason: c.reason, Message: "GET /x: 404 Not Found"})
		// Code, message and detail survive as the error and as text (the
		// engine hands a retried OnFailure the cause as text).
		for _, cause := range []error{err, workflowFail(err), errors.New(err.Error())} {
			f := release.FailureOf(release.StepWaitReady, cause)
			if f.Code != c.code || !strings.Contains(f.Message, c.msg) || strings.Contains(f.Message, "404") || f.Detail != "GET /x: 404 Not Found" {
				t.Errorf("%q via %T: %+v", c.reason, cause, f)
			}
		}
	}
	// The cluster said nothing: no detail.
	if f := release.FailureOf(release.StepWaitReady, release.Unready(release.AppStatus{Phase: release.PhaseFailed})); f.Detail != "" || f.Code != release.CodeUnhealthy {
		t.Errorf("no detail: %+v", f)
	}
}

// workflowFail wraps like workflow.Fail does (the domain test cannot import it).
func workflowFail(err error) error { return fmt.Errorf("workflow: %w", err) }

func TestUploadTicketEventsNeverCarryTheToken(t *testing.T) {
	token, hash := release.NewUploadToken()
	ticket := release.UploadTicket{ID: release.NewUploadTicketID(), Organization: identity.NewOrganizationID(), Token: token, TokenHash: hash,
		Client: "mcp:x", ExpiresAt: time.Now().Add(release.UploadTicketTTL)}
	for _, e := range []event.Event{release.NewUploadTicketIssued(ticket, release.Names{}), release.NewUploadTicketUsed(ticket, release.Names{})} {
		if strings.Contains(string(e.Payload)+e.Summary, token) || strings.Contains(string(e.Payload), fmt.Sprintf("%x", hash)) {
			t.Fatalf("%s leaks the ticket: %s", e.Type, e.Payload)
		}
	}
}

func TestUploadToken(t *testing.T) {
	token, hash := release.NewUploadToken()
	if !release.ValidUploadToken(token) || len(hash) != 32 || string(release.HashUploadToken(token)) != string(hash) {
		t.Fatalf("token %q", token)
	}
	for _, bad := range []string{"", "odu_", "odu_short", strings.Repeat("a", 47), "odu_" + strings.Repeat("!", 43), token + "x"} {
		if release.ValidUploadToken(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestViaFor(t *testing.T) {
	const agy = "agy-client"
	for name, c := range map[string]struct {
		reason release.Reason
		client string
		want   string
	}{
		"console":           {release.ReasonDeploy, "web-client", "console"},
		"no client":         {release.ReasonDeploy, "", "console"},
		"claude code":       {release.ReasonDeploy, "mcp:https://claude.ai/oauth/claude-code-client-metadata", "assistant:claude_code"},
		"codex":             {release.ReasonRollback, "mcp:https://chatgpt.com/oauth/codex/client.json", "assistant:codex"},
		"vscode":            {release.ReasonDeploy, "mcp:https://vscode.dev/oauth/client-metadata.json", "assistant:vscode"},
		"antigravity":       {release.ReasonDeploy, "mcp:" + agy, "assistant:antigravity"},
		"unknown assistant": {release.ReasonDeploy, "mcp:other-client", "assistant:other"},
		"auto rollback":     {release.ReasonAutoRollback, "", "platform"},
		"auto rollback mcp": {release.ReasonAutoRollback, "mcp:" + agy, "platform"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := release.ViaFor(c.reason, c.client, agy); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAccessMode(t *testing.T) {
	for _, m := range []release.AccessMode{release.AccessTeam, release.AccessOrganization, release.AccessPublic} {
		if !m.Valid() || release.AccessLabel(m) == string(m) {
			t.Errorf("%q", m)
		}
		if err := (release.SetAccess{Mode: m}).Validate(); err != nil {
			t.Errorf("%q refused: %v", m, err)
		}
	}
	for _, m := range []release.AccessMode{"", "everyone", "Team", "private"} {
		if m.Valid() || (release.SetAccess{Mode: m}).Validate() == nil {
			t.Errorf("%q accepted", m)
		}
	}
	if !release.AccessTeam.Protected() || !release.AccessOrganization.Protected() || release.AccessPublic.Protected() || release.AccessMode("").Protected() {
		t.Error("only team and organization are protected")
	}
	if got := release.DefaultSettings(identity.NewOrganizationID(), project.NewEnvironmentID()); got.Revision != 0 || got.Access.Mode != release.AccessTeam {
		t.Errorf("default %+v", got)
	}
	var d release.Deployment
	if d.AppliedSettings() != -1 {
		t.Error("a deployment that never recorded settings has applied none")
	}
	two := int64(2)
	d.SettingsRevision = &two
	if d.AppliedSettings() != 2 {
		t.Error("applied revision")
	}
}

// TestSettingsVarLimit: the 100 names of an environment are its variables,
// secrets and open requests together, each name once, in every path that adds
// one.
func TestSettingsVarLimit(t *testing.T) {
	org, env := identity.NewOrganizationID(), project.NewEnvironmentID()
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	base := release.DefaultSettings(org, env)
	for i := range 40 {
		base.Env[fmt.Sprintf("V%d", i)] = "x"
	}
	for i := range 30 {
		base.Secrets = append(base.Secrets, release.SecretRef{Name: fmt.Sprintf("S%d", i), Source: release.SecretManaged, Version: 1, KeyID: "k"})
	}
	requests := func(names ...string) []release.SecretRequest {
		var out []release.SecretRequest
		for _, n := range names {
			out = append(out, release.SecretRequest{Name: n})
		}
		return out
	}
	// 70 names + 30 requests = 100: full
	var open []release.SecretRequest
	for i := range 30 {
		open = append(open, requests(fmt.Sprintf("R%d", i))...)
	}
	if got := base.NameCount(open); got != 100 {
		t.Fatalf("count %d", got)
	}
	// a request of a name that is already a secret or a variable is not counted twice
	if got := base.NameCount(append(requests("S0", "V0"), open...)); got != 100 {
		t.Fatalf("double count %d", got)
	}
	tooMany := func(what string, err error) {
		t.Helper()
		var e *errx.Error
		if !errors.As(err, &e) || !strings.Contains(e.Error(), "at most 100 variables, secrets and pending requests") {
			t.Errorf("%s: %v", what, err)
		}
	}

	// WithEnv: a 101st name is refused, replacing or removing is fine
	if _, _, _, err := base.WithEnv(release.SetEnv{Set: map[string]string{"NEW": "x"}, RequestedBy: user}, open); err == nil {
		t.Error("WithEnv took a 101st name")
	} else {
		tooMany("WithEnv", err)
	}
	if _, _, _, err := base.WithEnv(release.SetEnv{Set: map[string]string{"V0": "y"}, RequestedBy: user}, open); err != nil {
		t.Errorf("WithEnv replacing: %v", err)
	}
	if _, _, _, err := base.WithEnv(release.SetEnv{Set: map[string]string{"NEW": "x"}, Unset: []string{"V0"}, RequestedBy: user}, open); err != nil {
		t.Errorf("WithEnv swapping: %v", err)
	}
	// a name with an open request becomes a variable: still 100 (the request is not a second place)
	if _, _, _, err := base.WithEnv(release.SetEnv{Set: map[string]string{"R0": "x"}, RequestedBy: user}, open); err != nil {
		t.Errorf("WithEnv over a request: %v", err)
	}

	// WithSecret: a new secret is refused; answering a request is not
	ref := func(name string) release.SecretRef {
		return release.SecretRef{Name: name, Source: release.SecretManaged, Version: 1, KeyID: "k"}
	}
	if _, err := base.WithSecret(ref("NEW"), user, open); err == nil {
		t.Error("WithSecret took a 101st name")
	} else {
		tooMany("WithSecret", err)
	}
	if n, err := base.WithSecret(ref("R0"), user, open); err != nil || len(n.Secrets) != 31 {
		t.Errorf("WithSecret answering a request: %v", err)
	}
	if _, err := base.WithSecret(ref("V0"), user, open); err != nil { // variable becomes a secret
		t.Errorf("WithSecret over a variable: %v", err)
	}
	if _, err := base.WithSecret(ref("S0"), user, open); err != nil { // rotation
		t.Errorf("WithSecret rotating: %v", err)
	}

	// CheckRequest: full refuses a new name, renews an open one, and a name
	// that is a secret is not a new request
	tooMany("CheckRequest", base.CheckRequest(open, "NEW"))
	if err := base.CheckRequest(open, "R5"); err != nil {
		t.Errorf("CheckRequest renewing: %v", err)
	}
	if err := base.CheckRequest(open[:29], "NEW"); err != nil {
		t.Errorf("CheckRequest with a free place: %v", err)
	}
}
