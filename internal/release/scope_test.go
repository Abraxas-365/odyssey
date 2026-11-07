package release_test

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// MS-4: settings of some services only.

var appServices = []project.Service{
	{Name: "web", Kind: project.ServiceWeb, Route: "/"},
	{Name: "api", Kind: project.ServiceWeb, Route: "/api"},
	{Name: "jobs", Kind: project.ServiceWorker},
}

func scope(names ...string) release.Scope { return release.Scope(names) }

func codeOf(err error) string {
	var e *errx.Error
	if !errx.As(err, &e) {
		return ""
	}
	return e.Code
}

func fieldOf(err error) any {
	var e *errx.Error
	if !errx.As(err, &e) {
		return nil
	}
	return e.Details["field"]
}

func TestScopeAlgebra(t *testing.T) {
	if got := scope("web", " api", "web").Normalize(); !slices.Equal(got, scope("api", "web")) {
		t.Errorf("normalize: %v", got)
	}
	if scope().Normalize() != nil {
		t.Error("the whole app normalizes to nil")
	}
	cases := []struct {
		name           string
		a, b           release.Scope
		overlaps, covs bool
	}{
		{"whole/whole", nil, nil, true, true},
		{"whole/some", nil, scope("api"), true, true},
		{"some/whole", scope("api"), nil, true, false},
		{"disjoint", scope("api"), scope("web"), false, false},
		{"shared", scope("api", "web"), scope("web"), true, true},
		{"partial", scope("web"), scope("api", "web"), true, false},
	}
	for _, c := range cases {
		if got := c.a.Overlaps(c.b); got != c.overlaps {
			t.Errorf("%s: overlaps %v", c.name, got)
		}
		if got := c.a.Covers(c.b); got != c.covs {
			t.Errorf("%s: covers %v", c.name, got)
		}
	}
	if !scope().Reaches("anything") || scope("api").Reaches("web") || !scope("api").Reaches("api") {
		t.Error("reaches")
	}
	if !scope("web", "api").Equal(scope("api", "web")) || scope("api").Equal(nil) {
		t.Error("equal is set equality")
	}
}

func TestScopeCheck(t *testing.T) {
	if err := scope().Check("services", appServices); err != nil {
		t.Errorf("whole app: %v", err)
	}
	if err := scope("api", "jobs").Check("services", appServices); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, s := range map[string]release.Scope{
		"unknown":   scope("admin"),
		"bad name":  scope("API"),
		"too many":  scope("a", "b", "c", "d", "e", "f", "g", "h", "i"),
		"empty str": scope(""),
	} {
		if err := s.Check("services", appServices); fieldOf(err) != "services" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A removal may name a service the app no longer has: only names count.
	if err := scope("admin").CheckNames("services"); err != nil {
		t.Errorf("check names: %v", err)
	}
}

func TestScopeLabelAndGate(t *testing.T) {
	if scope().Label() != "" || scope("api").Label() != " (solo api)" || scope("api", "jobs", "web").Label() != " (solo api, jobs y web)" {
		t.Errorf("labels: %q %q", scope("api").Label(), scope("api", "jobs", "web").Label())
	}
	got := scope("api").Gate(appServices)
	if len(got) != 1 || got[0].Name != "api" || !got[0].Public {
		t.Errorf("gate of api: %+v", got)
	}
	if got := scope().Gate(appServices); len(got) != 3 {
		t.Errorf("the whole app reaches every service: %+v", got)
	}
	if got := scope("gone").Gate(appServices); len(got) != 0 {
		t.Errorf("a service the app lost reaches nobody: %+v", got)
	}
}

func TestKept(t *testing.T) {
	current := scope("web")
	if got := release.Kept(nil, current); !slices.Equal(got, current) {
		t.Errorf("nil keeps: %v", got)
	}
	empty := scope()
	if got := release.Kept(&empty, current); got != nil {
		t.Errorf("empty is the whole app: %v", got)
	}
	api := scope("api")
	if got := release.Kept(&api, current); !slices.Equal(got, api) {
		t.Errorf("given: %v", got)
	}
}

func TestFilesVolumeReachesOneService(t *testing.T) {
	cases := []struct {
		name     string
		kind     resourcekind.Kind
		scope    release.Scope
		services []project.Service
		ok       bool
	}{
		{"files, one service app, whole", resourcekind.Files, nil, appServices[:1], true},
		{"files, several, whole", resourcekind.Files, nil, appServices, false},
		{"files, several, two", resourcekind.Files, scope("api", "web"), appServices, false},
		{"files, several, one", resourcekind.Files, scope("api"), appServices, true},
		{"postgres, several, whole", resourcekind.Postgres, nil, appServices, true},
	}
	for _, c := range cases {
		err := release.CheckResourceScope(c.kind, c.scope, c.services)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && fieldOf(err) != "services" {
			t.Errorf("%s: field %v", c.name, fieldOf(err))
		}
	}
}

var nobody identity.UserID

func base() release.Settings {
	return release.DefaultSettings(identity.OrganizationID{}, project.EnvironmentID{})
}

func TestWithEnvLayers(t *testing.T) {
	s := base()
	ch, err := s.WithEnv(release.SetEnv{Set: map[string]string{"LOG": "info"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s = ch.Next
	ch, err = s.WithEnv(release.SetEnv{Set: map[string]string{"LOG": "debug"}, Services: scope("api")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Next.Env["LOG"] != "info" || ch.Next.ServiceEnv["api"]["LOG"] != "debug" {
		t.Errorf("layers: %+v %+v", ch.Next.Env, ch.Next.ServiceEnv)
	}
	if len(ch.Warnings) != 1 || ch.Warnings[0] != "El servicio api usa su propio valor de LOG, no el de la app." {
		t.Errorf("warnings: %v", ch.Warnings)
	}
	if !ch.Next.Scoped() {
		t.Error("a service's own variable is scoped")
	}
	s = ch.Next
	// The whole app's changes: the service keeps its own, and is told.
	ch, err = s.WithEnv(release.SetEnv{Set: map[string]string{"LOG": "warn"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Warnings) != 1 || ch.Warnings[0] != "El servicio api conserva su propio valor de LOG." {
		t.Errorf("warnings: %v", ch.Warnings)
	}
	s = ch.Next
	// A service's unset leaves the whole app's.
	ch, err = s.WithEnv(release.SetEnv{Unset: []string{"LOG"}, Services: scope("api")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Next.Env["LOG"] != "warn" || len(ch.Next.ServiceEnv) != 0 || ch.Next.Scoped() {
		t.Errorf("service unset: %+v %+v", ch.Next.Env, ch.Next.ServiceEnv)
	}
	// The whole app's unset removes it from every layer.
	ch, err = s.WithEnv(release.SetEnv{Unset: []string{"LOG"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Next.Env) != 0 || len(ch.Next.ServiceEnv) != 0 || !slices.Equal(ch.Unset, []string{"LOG"}) {
		t.Errorf("whole unset: %+v %+v", ch.Next.Env, ch.Next.ServiceEnv)
	}
	// The input's settings were not touched (a deep copy).
	if s.ServiceEnv["api"]["LOG"] != "debug" {
		t.Error("WithEnv changed its receiver")
	}
}

func TestScopedSettingsCollide(t *testing.T) {
	s := base()
	var err error
	if s, err = s.WithSecret(release.SecretRef{Name: "TOKEN", Source: release.SecretManaged, Version: 1, KeyID: "k", Services: scope("api")}, nobody, nil); err != nil {
		t.Fatal(err)
	}
	// A variable of another service is no collision: the scopes do not meet.
	if _, err := s.WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "x"}, Services: scope("web")}, nil); err != nil {
		t.Errorf("disjoint: %v", err)
	}
	// The same service, or the whole app, meets the secret.
	if _, err := s.WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "x"}, Services: scope("api")}, nil); fieldOf(err) != "set" {
		t.Errorf("same service: %v", err)
	}
	if _, err := s.WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "x"}}, nil); fieldOf(err) != "set" {
		t.Errorf("whole app: %v", err)
	}
	// A second secret of the name for another service is the same name twice.
	if _, err := s.WithSecret(release.SecretRef{Name: "TOKEN", Source: release.SecretManaged, Version: 2, KeyID: "k", Services: scope("api", "web")}, nobody, nil); err != nil {
		t.Errorf("a secret replaces its own name: %v", err)
	}
	// A resource whose variable the secret already gives api.
	pg := release.ResourceUse{Name: "db", Kind: resourcekind.Postgres, Services: scope("api")}
	n, _, err := base().WithResource(pg, nobody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.WithEnv(release.SetEnv{Set: map[string]string{"DATABASE_URL": "x"}, Services: scope("web")}, nil); err != nil {
		t.Errorf("the database reaches only api: %v", err)
	}
	if _, err := n.WithEnv(release.SetEnv{Set: map[string]string{"DATABASE_URL": "x"}, Services: scope("api")}, nil); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("api has DATABASE_URL: %v", err)
	}
	other := release.ResourceUse{Name: "db2", Kind: resourcekind.Postgres}
	if _, _, err := n.WithResource(other, nobody, nil); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("a whole-app database meets api's: %v", err)
	}
	other.Services = scope("web")
	if _, _, err := n.WithResource(other, nobody, nil); err != nil {
		t.Errorf("one database per service: %v", err)
	}
}

func TestSecretOfSomeServicesOverAWholeAppVariable(t *testing.T) {
	ch, err := base().WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "plain"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := release.SecretRef{Name: "TOKEN", Source: release.SecretManaged, Version: 1, KeyID: "k", Services: scope("api")}
	if _, err := ch.Next.WithSecret(ref, nobody, nil); fieldOf(err) != "services" {
		t.Errorf("the other services would lose it: %v", err)
	}
	ref.Services = nil
	n, err := ch.Next.WithSecret(ref, nobody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, plain := n.Env["TOKEN"]; plain {
		t.Error("the variable becomes the secret")
	}
	// A service's own variable becomes the secret where it reaches; the
	// others keep theirs.
	ch, err = base().WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "plain"}, Services: scope("api", "web")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref.Services = scope("api")
	n, err = ch.Next.WithSecret(ref, nobody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.ServiceEnv["api"]; ok || n.ServiceEnv["web"]["TOKEN"] != "plain" {
		t.Errorf("service env: %+v", n.ServiceEnv)
	}
}

func TestAIOfSomeServices(t *testing.T) {
	ch, err := base().WithEnv(release.SetEnv{Set: map[string]string{"OPENAI_API_KEY": "mine"}, Services: scope("web")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ai := release.AISettings{Enabled: true, KeyVersion: 1, KeyID: "k", BaseURL: "http://gw", Services: scope("api")}
	n, changed, err := ch.Next.WithAI(ai, nobody, nil)
	if err != nil || !changed {
		t.Fatalf("api's AI does not meet web's variable: %v", err)
	}
	if !n.Scoped() {
		t.Error("an AI of some services is scoped")
	}
	ai.Services = nil
	if _, _, err := ch.Next.WithAI(ai, nobody, nil); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("the whole app's AI meets web's variable: %v", err)
	}
	// While on, the AI variables are taken where it reaches.
	if _, err := n.WithEnv(release.SetEnv{Set: map[string]string{"OPENAI_BASE_URL": "x"}, Services: scope("api")}, nil); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("api's AI holds OPENAI_BASE_URL: %v", err)
	}
	if _, err := n.WithEnv(release.SetEnv{Set: map[string]string{"OPENAI_BASE_URL": "x"}, Services: scope("jobs")}, nil); err != nil {
		t.Errorf("jobs has no AI: %v", err)
	}
	// Moving the AI is a change; the same services in another order is not.
	same := n.AI
	same.Services = scope("api")
	if _, changed, _ := n.WithAI(same, nobody, nil); changed {
		t.Error("the same AI is no change")
	}
	enable := release.EnableAI{Version: 2, KeyID: "k", BaseURL: "http://gw", Sealed: []byte{1}}
	if got := enable.Settings(n.AI); !slices.Equal(got.Services, scope("api")) {
		t.Errorf("a new key keeps the services in force: %v", got.Services)
	}
	if got := enable.Settings(release.AISettings{}); got.Services != nil {
		t.Errorf("turning it on reaches the whole app: %v", got.Services)
	}
}

func TestRequestsCarryTheirScope(t *testing.T) {
	open := []release.SecretRequest{{Name: "TOKEN", Services: scope("api")}}
	s := base()
	// A variable of another service does not meet api's request.
	if _, err := s.WithEnv(release.SetEnv{Set: map[string]string{"TOKEN": "x"}, Services: scope("web")}, open); err != nil {
		t.Errorf("disjoint: %v", err)
	}
	// A database whose variable api is waiting for.
	pg := release.ResourceUse{Name: "db", Kind: resourcekind.Postgres, Prefix: "", Services: scope("api")}
	open = []release.SecretRequest{{Name: "DATABASE_URL", Services: scope("api")}}
	if _, _, err := s.WithResource(pg, nobody, open); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("api asked for DATABASE_URL: %v", err)
	}
	pg.Services = scope("web")
	if _, _, err := s.WithResource(pg, nobody, open); err != nil {
		t.Errorf("web asked for nothing: %v", err)
	}
	n, _, err := s.WithResource(pg, nobody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.CheckRequest(nil, "DATABASE_URL", scope("api")); err != nil {
		t.Errorf("api has no database: %v", err)
	}
	if err := n.CheckRequest(nil, "DATABASE_URL", nil); codeOf(err) != release.CodeVariableTaken {
		t.Errorf("the whole app meets web's database: %v", err)
	}
	// The view of an open request says who will read it.
	v := release.ViewSecrets(n, []release.SecretRequest{{Name: "TOKEN", Services: scope("api")}}, release.ClusterSecrets{}, nil)
	if len(v.Items) != 1 || !slices.Equal(v.Items[0].Services, scope("api")) {
		t.Errorf("view: %+v", v.Items)
	}
}

func TestEachServiceLayerTakesAPlace(t *testing.T) {
	ch, err := base().WithEnv(release.SetEnv{Set: map[string]string{"A": "1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, err = ch.Next.WithEnv(release.SetEnv{Set: map[string]string{"A": "2"}, Services: scope("api", "web")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ch.Next.NameCount(nil); got != 3 {
		t.Errorf("one shared value and two own: %d", got)
	}
}

func TestDesiredCarriesTheScope(t *testing.T) {
	s := base()
	s.Env = map[string]string{"LOG": "info", "TOKEN": "plain"}
	s.ServiceEnv = map[string]map[string]string{"api": {"LOG": "debug"}, "gone": {"X": "1"}}
	s.Secrets = []release.SecretRef{{Name: "TOKEN", Source: release.SecretManaged, Version: 1, KeyID: "k", Services: scope("api")}}
	s.Resources = []release.ResourceUse{{Name: "db", Kind: resourcekind.Postgres, Services: scope("api")}}
	s.AI = release.AISettings{Enabled: true, KeyVersion: 3, KeyID: "k", BaseURL: "http://gw", Services: scope("web")}
	d := release.DesiredApp{Env: map[string]string{"DATABASE_URL": "from-release", "OPENAI_API_KEY": "r"},
		Services: []release.Service{{Name: "web"}, {Name: "api"}}}
	got := d.WithSettings(s)
	if !got.Scoped() {
		t.Error("scoped")
	}
	// A scoped connection or AI keeps the release's variable for the others.
	if got.Env["DATABASE_URL"] != "from-release" || got.Env["OPENAI_API_KEY"] != "r" {
		t.Errorf("env: %+v", got.Env)
	}
	// A scoped secret leaves the whole app's variable for the others.
	if got.Env["TOKEN"] != "plain" {
		t.Errorf("env TOKEN: %+v", got.Env)
	}
	if len(got.ServiceEnv) != 1 || got.ServiceEnv["api"]["LOG"] != "debug" {
		t.Errorf("only the version's services get their own: %+v", got.ServiceEnv)
	}
	if !slices.Equal(got.Secrets[0].Services, []string{"api"}) || !slices.Equal(got.Resources[0].Services, []string{"api"}) {
		t.Errorf("secrets %+v resources %+v", got.Secrets, got.Resources)
	}
	if got.AI == nil || !slices.Equal(got.AI.Services, []string{"web"}) {
		t.Errorf("ai: %+v", got.AI)
	}
	key := got.Secrets[len(got.Secrets)-1]
	if key.Name != release.AIKeySecret || !slices.Equal(key.Services, []string{"web"}) {
		t.Errorf("the AI key goes where the AI does: %+v", key)
	}
	// The whole app's settings are not scoped.
	if (release.DesiredApp{}).WithSettings(base()).Scoped() {
		t.Error("defaults are not scoped")
	}
}
