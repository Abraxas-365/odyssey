package releasesvc

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// MS-4: settings of some services only, as the service checks and judges
// them before anything is written.

type scopeProjects struct {
	usesProjects
	services []project.Service
}

func (f *scopeProjects) Services(context.Context, identity.OrganizationID, project.ID) ([]project.Service, error) {
	return f.services, nil
}

type scopeRepo struct {
	usesRepo
	appended []release.Settings
	scoped   []release.SecretRequest
	open     []release.SecretRequest
}

func (r *scopeRepo) LockState(_ context.Context, s release.EnvironmentState) (release.EnvironmentState, error) {
	return s, nil
}

func (r *scopeRepo) AppendSettings(_ context.Context, s release.Settings) (release.Settings, error) {
	s.Revision = r.settings.Revision + 1
	r.appended = append(r.appended, s)
	r.settings = s
	return s, nil
}

func (r *scopeRepo) ListSecretRequests(context.Context, identity.OrganizationID, project.EnvironmentID) ([]release.SecretRequest, error) {
	return r.open, nil
}

func (r *scopeRepo) ScopeSecretRequest(_ context.Context, q release.SecretRequest) error {
	r.scoped = append(r.scoped, q)
	return nil
}

type scopeCluster struct {
	release.Cluster
	scoped bool
}

func (c *scopeCluster) Secrets(context.Context, release.Target) (release.ClusterSecrets, error) {
	return release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}, Stores: []string{}, ScopedSettings: c.scoped}, nil
}

type scopeResources struct {
	release.Resources
	kind resourcekind.Kind
}

func (r scopeResources) Find(_ context.Context, _ identity.OrganizationID, _ project.EnvironmentID, name string) (release.ResourceInfo, error) {
	return release.ResourceInfo{Name: name, Kind: r.kind}, nil
}

// recordRules answers allow and keeps what IT's rules were asked.
type recordRules struct{ requests []gate.Request }

func (j *recordRules) Check(_ context.Context, _ identity.OrganizationID, r gate.Request) (gate.Verdict, error) {
	j.requests = append(j.requests, r)
	return gate.Verdict{Effect: gate.Allow}, nil
}

type scopeRig struct {
	svc     *Service
	repo    *scopeRepo
	cluster *scopeCluster
	rules   *recordRules
	events  *eventtest.Recorder
	p       project.Project
	e       project.Environment
}

func newScopeRig(scoped bool, kind resourcekind.Kind) *scopeRig {
	org := identity.NewOrganizationID()
	p := project.Project{ID: project.NewID(), Organization: org, Name: "Tienda", Slug: "tienda", Tier: project.TierProduction}
	e := project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}
	services := []project.Service{
		{Name: "web", Kind: project.ServiceWeb, Route: "/"},
		{Name: "api", Kind: project.ServiceWeb, Route: "/api"},
		{Name: "jobs", Kind: project.ServiceWorker},
	}
	r := &scopeRig{repo: &scopeRepo{usesRepo: usesRepo{settings: release.DefaultSettings(org, e.ID)}},
		cluster: &scopeCluster{scoped: scoped}, rules: &recordRules{}, events: &eventtest.Recorder{}, p: p, e: e}
	r.svc = &Service{repo: r.repo, projects: &scopeProjects{usesProjects: usesProjects{p: p, e: e}, services: services},
		cluster: r.cluster, tx: directTx{}, policy: r.rules, events: r.events, resources: scopeResources{kind: kind}, now: time.Now}
	return r
}

func errCode(err error) string {
	var e *errx.Error
	if errx.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestSetEnvOfAServiceNeedsTheCapability(t *testing.T) {
	ctx := context.Background()
	r := newScopeRig(false, resourcekind.Postgres)
	in := release.SetEnv{Set: map[string]string{"LOG": "debug"}, Services: release.Scope{"api"}}
	if _, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, in); errCode(err) != release.CodeServiceSettingsUnavailable {
		t.Fatalf("an old agent: %v", err)
	}
	if len(r.repo.appended) != 0 {
		t.Fatal("refused, yet written")
	}
	// The whole app needs nothing; removing a service's variable neither.
	if _, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, release.SetEnv{Set: map[string]string{"LOG": "info"}}); err != nil {
		t.Fatalf("whole app: %v", err)
	}
	if _, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, release.SetEnv{Unset: []string{"LOG"}, Services: release.Scope{"api"}}); err != nil {
		t.Fatalf("a removal: %v", err)
	}

	r = newScopeRig(true, resourcekind.Postgres)
	in.Services = release.Scope{"admin"}
	if _, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, in); errx.IsType(err, errx.TypeValidation) == false {
		t.Fatalf("a service the app does not have: %v", err)
	}
	if _, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, release.SetEnv{Set: map[string]string{"LOG": "info"}}); err != nil {
		t.Fatal(err)
	}
	in.Services = release.Scope{"api"}
	out, err := r.svc.SetEnv(ctx, r.p.Organization, r.p.ID, r.e.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Settings.ServiceEnv["api"]["LOG"] != "debug" || len(out.Warnings) != 1 {
		t.Fatalf("change %+v", out)
	}
	evs := r.events.Events()
	last := evs[len(evs)-1]
	var payload release.EnvChanged
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if last.Type != release.EventEnvChanged || !slices.Equal(payload.Services, release.Scope{"api"}) || last.Summary != "Cambió las variables (solo api) de Tienda en production" {
		t.Fatalf("event %s %q %s", last.Type, last.Summary, last.Payload)
	}
}

func TestConnectResourceJudgesTheServicesItReaches(t *testing.T) {
	ctx := context.Background()
	r := newScopeRig(true, resourcekind.Postgres)
	api := release.Scope{"api"}
	if _, err := r.svc.ConnectResource(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ConnectResource{Name: "db", Services: &api}); err != nil {
		t.Fatal(err)
	}
	if got := r.rules.requests[0].Services; len(got) != 1 || got[0].Name != "api" || !got[0].Public {
		t.Fatalf("judged services %+v", got)
	}
	if use, _ := r.repo.settings.Resource("db"); !slices.Equal(use.Services, api) {
		t.Fatalf("connected %+v", use)
	}
	// Connecting again without services keeps them (and judges them).
	if _, err := r.svc.ConnectResource(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ConnectResource{Name: "db", Prefix: "MAIN"}); err != nil {
		t.Fatal(err)
	}
	if use, _ := r.repo.settings.Resource("db"); !slices.Equal(use.Services, api) || use.Prefix != "MAIN" {
		t.Fatalf("kept %+v", use)
	}
	if got := r.rules.requests[1].Services; len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("judged again %+v", got)
	}
	// The whole app is every service.
	whole := release.Scope{}
	if _, err := r.svc.ConnectResource(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ConnectResource{Name: "db", Services: &whole}); err != nil {
		t.Fatal(err)
	}
	if got := r.rules.requests[2].Services; len(got) != 3 {
		t.Fatalf("whole app judged %+v", got)
	}
}

func TestAFilesVolumeGoesToOneService(t *testing.T) {
	ctx := context.Background()
	r := newScopeRig(true, resourcekind.Files)
	in := release.ConnectResource{Name: "uploads"}
	if _, err := r.svc.ConnectResource(ctx, r.p.Organization, r.p.ID, r.e.ID, in); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("the whole app of three services: %v", err)
	}
	if len(r.rules.requests) != 0 {
		t.Fatal("judged an invalid connection")
	}
	one := release.Scope{"jobs"}
	in.Services = &one
	if _, err := r.svc.ConnectResource(ctx, r.p.Organization, r.p.ID, r.e.ID, in); err != nil {
		t.Fatal(err)
	}
	if got := r.rules.requests[0].Services; len(got) != 1 || got[0].Name != "jobs" || got[0].Public {
		t.Fatalf("judged %+v", got)
	}
	// Creating with connect asks the same.
	_, err := r.svc.AuthorizeConnect(ctx, r.p.Organization, r.p.ID, r.e.ID, release.AuthorizeConnect{Name: "more", Kind: resourcekind.Files})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("authorize the whole app: %v", err)
	}
}

func TestScopeSecretOfARequest(t *testing.T) {
	ctx := context.Background()
	r := newScopeRig(true, resourcekind.Postgres)
	r.repo.open = []release.SecretRequest{{Name: "TOKEN", Organization: r.p.Organization, Environment: r.e.ID}}
	out, err := r.svc.ScopeSecret(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ScopeSecret{Name: "TOKEN", Services: release.Scope{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Deployment != nil || len(r.repo.appended) != 0 || len(r.repo.scoped) != 1 || !slices.Equal(r.repo.scoped[0].Services, release.Scope{"api"}) {
		t.Fatalf("a request changes only itself: %+v %+v", out, r.repo.scoped)
	}
	if _, err := r.svc.ScopeSecret(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ScopeSecret{Name: "NOPE", Services: release.Scope{"api"}}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("no such secret: %v", err)
	}
	// A secret in force is a new revision.
	r.repo.settings.Secrets = []release.SecretRef{{Name: "KEY", Source: release.SecretManaged, Version: 1, KeyID: "k"}}
	if _, err := r.svc.ScopeSecret(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ScopeSecret{Name: "KEY", Services: release.Scope{"web"}}); err != nil {
		t.Fatal(err)
	}
	if ref, _ := r.repo.settings.Secret("KEY"); !slices.Equal(ref.Services, release.Scope{"web"}) || len(r.repo.appended) != 1 {
		t.Fatalf("scoped %+v", ref)
	}
	// A vault secret needs its permission.
	r.repo.settings.Secrets = append(r.repo.settings.Secrets, release.SecretRef{Name: "VAULT", Source: release.SecretExternal, Store: "s", Key: "k"})
	if _, err := r.svc.ScopeSecret(ctx, r.p.Organization, r.p.ID, r.e.ID, release.ScopeSecret{Name: "VAULT", Services: release.Scope{"web"}}); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("a vault secret: %v", err)
	}
}
