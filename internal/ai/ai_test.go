package ai_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	proj := project.MustParseID("00000000-0000-4000-8000-0000000000aa")
	env := project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb")
	k := ai.Key{ID: ai.MustParseKeyID("00000000-0000-4000-8000-0000000000f1"), Organization: org, Project: proj, Environment: env}
	n := ai.Names{Project: "Visitas", Environment: "production"}
	for _, e := range []event.Event{
		ai.NewEnabled(org, user, k, 1, n),
		ai.NewRotated(org, user, k, 2, n),
		ai.NewDisabled(org, user, proj.String(), env.String(), n),
		ai.NewRevokeFailed(org, user, proj.String(), env.String(), "The AI gateway is not reachable.", n),
		ai.NewRevocationRetried(org, proj.String(), env.String(), 2, n),
	} {
		eventtest.Golden(t, e)
		if strings.Contains(string(e.Payload), "ik_") || strings.Contains(strings.ToLower(string(e.Payload)), "secret") {
			t.Errorf("%s payload mentions a secret: %s", e.Type, e.Payload)
		}
	}
}

func TestViewStates(t *testing.T) {
	at := time.Now()
	key := func(s ai.Status) ai.Key { return ai.Key{ID: ai.NewKeyID(), Status: s, CreatedAt: at} }
	for name, c := range map[string]struct {
		keys    []ai.Key
		status  string
		enabled bool
	}{
		"off":                       {nil, ai.StateOff, false},
		"enabling":                  {[]ai.Key{key(ai.StatusCreating)}, ai.StateEnabling, false},
		"enabled":                   {[]ai.Key{key(ai.StatusActive)}, ai.StateEnabled, true},
		"rotating":                  {[]ai.Key{key(ai.StatusRevoking), key(ai.StatusActive), key(ai.StatusCreating)}, ai.StateRotating, true},
		"cleaning":                  {[]ai.Key{key(ai.StatusActive), key(ai.StatusRevoking)}, ai.StateRotating, true},
		"disabling":                 {[]ai.Key{key(ai.StatusRevoking)}, ai.StateDisabling, false},
		"revoke failed":             {[]ai.Key{key(ai.StatusRevokeFailed)}, ai.StateRevokeFailed, false},
		"enabled over a failed one": {[]ai.Key{key(ai.StatusRevokeFailed), key(ai.StatusActive)}, ai.StateEnabled, true},
	} {
		v := ai.NewView(c.keys, "https://llm.test/", true, "")
		if v.Status != c.status || v.Enabled != c.enabled || v.BaseURL != "https://llm.test" || len(v.Variables) != 4 {
			t.Errorf("%s: %+v", name, v)
		}
		if c.enabled && (v.EnabledAt == nil || v.EnabledBy == nil) {
			t.Errorf("%s: no enabled_by/at", name)
		}
	}
}

func TestErrorsAreBusinessWithTheirCodes(t *testing.T) {
	for err, code := range map[error]string{
		ai.ErrUnavailable():        ai.CodeUnavailable,
		ai.ErrClusterUnavailable(): ai.CodeUnavailable,
		ai.ErrNoSecrets():          ai.CodeSecretsUnavailable,
		ai.ErrBusy():               ai.CodeBusy,
		ai.ErrNotEnabled():         ai.CodeNotEnabled,
	} {
		if !errx.IsType(err, errx.TypeBusiness) && !errx.IsType(err, errx.TypeConflict) {
			t.Errorf("%v: type", err)
		}
		var ee *errx.Error
		if !errors.As(err, &ee) || ee.Code != code {
			t.Errorf("%v: code want %q", err, code)
		}
	}
	if ai.MessageOf(ai.ErrUnavailable()) != "La IA no está habilitada en esta organización. Pide a TI que la conecte." {
		t.Errorf("message: %q", ai.MessageOf(ai.ErrUnavailable()))
	}
}

func TestKeyNamesAreDeterministicAndPrefixed(t *testing.T) {
	env, id := project.NewEnvironmentID(), ai.NewKeyID()
	if ai.KeyName(env, id) != ai.KeyName(env, id) || !strings.HasPrefix(ai.KeyName(env, id), ai.NamePrefix(env)) {
		t.Error("name")
	}
	if strings.HasPrefix(ai.KeyName(project.NewEnvironmentID(), id), ai.NamePrefix(env)) {
		t.Error("another environment's key matches the prefix")
	}
}

func TestRevokeFailedIsVisibleWithItsReason(t *testing.T) {
	k := ai.Key{ID: ai.NewKeyID(), Status: ai.StatusRevokeFailed, RevokeError: "The AI gateway is not reachable.", CreatedAt: time.Now()}
	v := ai.NewView([]ai.Key{k}, "https://llm.test", true, "")
	if v.Status != ai.StateRevokeFailed || v.Warning != ai.WarningRevokeFailed || v.RevokeError != k.RevokeError {
		t.Fatalf("%+v", v)
	}
	if ok := ai.NewView([]ai.Key{{ID: ai.NewKeyID(), Status: ai.StatusActive, CreatedAt: time.Now()}}, "", true, ""); ok.Warning != "" || ok.RevokeError != "" {
		t.Errorf("a healthy app carries a warning: %+v", ok)
	}
	if !ai.StatusRevokeFailed.Valid() {
		t.Error("revoke_failed is not a valid status")
	}
}

func TestFailureReasonIsOurOwnWordsAndBounded(t *testing.T) {
	if got := ai.FailureReason(errx.External("The AI gateway is not reachable.")); got != "The AI gateway is not reachable." {
		t.Errorf("%q", got)
	}
	if got := ai.FailureReason(errors.New("dial tcp 10.0.0.1:3000: secret detail")); strings.Contains(got, "10.0.0.1") || got == "" {
		t.Errorf("a raw error reached people: %q", got)
	}
	if got := ai.FailureReason(errx.External(strings.Repeat("é", 1000))); len([]rune(got)) != ai.MaxReason {
		t.Errorf("not bounded: %d", len([]rune(got)))
	}
}
