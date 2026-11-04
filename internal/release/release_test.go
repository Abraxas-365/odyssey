package release_test

import (
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
	} {
		eventtest.Golden(t, e)
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
