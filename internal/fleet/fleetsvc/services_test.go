package fleetsvc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// An app of several services is refused for a cluster whose agent runs one
// workload per app (it would serve half of it); one web service at "/" is
// not.
func TestApplyRefusesServicesWithoutTheCapability(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	img := "r.local/web@sha256:" + strings.Repeat("a", 64)
	shop := fleet.App{Organization: e.org, Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "shop", Revision: 1,
		Services: []fleet.AppService{
			{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 1},
			{Name: "worker", Kind: fleet.ServiceWorker, Image: img, Replicas: 1},
		}}
	connect := func(apis ...string) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{APIs: apis}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect(fleet.CapabilityRetire)
	if err := e.svc.Apply(ctx, shop); code(err) != project.CodeServicesUnavailable {
		t.Fatalf("several services without the capability: %v", err)
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, agent, shop.Environment, "shop"); ok {
		t.Fatal("a refused app was stored")
	}
	simple := fleet.App{Organization: e.org, Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "web", Revision: 1,
		Image: img, Port: 3000, HealthPath: "/", Replicas: 1,
		Services: []fleet.AppService{{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 1}}}
	if err := e.svc.Apply(ctx, simple); err != nil {
		t.Fatalf("one web service runs anywhere: %v", err)
	}
	connect(fleet.CapabilityRetire, fleet.CapabilityServices)
	if err := e.svc.Apply(ctx, shop); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := e.svc.DesiredApp(ctx, agent, shop.Environment, "shop"); !ok || err != nil || len(got.Services) != 2 || got.Image != "" {
		t.Fatalf("stored %+v %v %v", got, ok, err)
	}
}

// A setting for some services only is refused for a cluster whose agent
// would hand it to every service (a database meant for the api would reach
// the web); one for every service is not.
func TestApplyRefusesScopedSettingsWithoutTheCapability(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	img := "r.local/web@sha256:" + strings.Repeat("a", 64)
	shop := func(rev int64) fleet.App {
		return fleet.App{Organization: e.org, Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "shop", Revision: rev,
			Services: []fleet.AppService{
				{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 1},
				{Name: "api", Kind: fleet.ServiceWeb, Route: "/api", Image: img, Port: 8080, HealthPath: "/", Replicas: 1},
			}}
	}
	connect := func(apis ...string) {
		t.Helper()
		caps := fleet.Capabilities{APIs: append(apis, fleet.CapabilityExternalSecrets), SecretStores: []string{"vault"}, ResourceKinds: []string{"redis"}}
		if err := e.svc.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "v1", Capabilities: caps}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect(fleet.CapabilityServices)
	for name, scope := range map[string]func(*fleet.App){
		"service env": func(a *fleet.App) { a.Services[1].Env = map[string]string{"LOG": "1"} },
		"secret": func(a *fleet.App) {
			a.Secrets = []fleet.AppSecret{{Name: "K", Source: fleet.SecretExternal, Store: "vault", Key: "k", Services: []string{"api"}}}
		},
		"resource": func(a *fleet.App) {
			a.Resources = []fleet.ResourceRef{{Name: "main", Kind: "redis", Services: []string{"api"}}}
		},
	} {
		a := shop(1)
		scope(&a)
		if !a.ScopedSettings() {
			t.Fatalf("%s: not scoped", name)
		}
		if err := e.svc.Apply(ctx, a); code(err) != fleet.CodeServiceSettingsUnavailable {
			t.Errorf("%s without the capability: %v", name, err)
		}
	}
	if err := e.svc.Apply(ctx, shop(1)); err != nil {
		t.Fatalf("settings for every service run anywhere: %v", err)
	}
	connect(fleet.CapabilityServices, fleet.CapabilityServiceSettings)
	a := shop(2)
	a.Services[1].Env = map[string]string{"LOG": "1"}
	if err := e.svc.Apply(ctx, a); err != nil {
		t.Fatal(err)
	}
}

func TestValidateChecksScopes(t *testing.T) {
	img := "r.local/web@sha256:" + strings.Repeat("a", 64)
	base := func() fleet.App {
		return fleet.App{Organization: identity.NewOrganizationID(), Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "shop", Revision: 1,
			Services: []fleet.AppService{{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 1},
				{Name: "worker", Kind: fleet.ServiceWorker, Image: img, Replicas: 1}}}
	}
	for name, bad := range map[string]func(*fleet.App){
		"repeated": func(a *fleet.App) {
			a.Resources = []fleet.ResourceRef{{Name: "m", Kind: "redis", Services: []string{"web", "web"}}}
		},
		"bad name": func(a *fleet.App) {
			a.Resources = []fleet.ResourceRef{{Name: "m", Kind: "redis", Services: []string{"Web"}}}
		},
		"bad env": func(a *fleet.App) { a.Services[0].Env = map[string]string{"1X": "y"} },
		"secret scope": func(a *fleet.App) {
			a.Secrets = []fleet.AppSecret{{Name: "K", Source: fleet.SecretExternal, Store: "vault", Key: "k", Services: []string{""}}}
		},
	} {
		a := base()
		bad(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a service the app does not have (any longer) is allowed: it reaches nobody
	a := base()
	a.Resources = []fleet.ResourceRef{{Name: "m", Kind: "redis", Services: []string{"gone"}}}
	if err := a.Validate(); err != nil {
		t.Errorf("a gone service: %v", err)
	}
}
