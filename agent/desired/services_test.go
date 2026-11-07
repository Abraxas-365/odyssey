package desired

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func withServices(rev int64) *fleetv1.App {
	a := app(rev)
	a.Image, a.Port, a.HealthPath, a.Replicas = "", 0, "", 0
	a.Services = []*fleetv1.Service{
		{Name: "web", Kind: "web", Route: "/", Image: "r.test/web@sha256:" + strings.Repeat("a", 64), Port: 3000, HealthPath: "/", Replicas: 2},
		{Name: "api", Kind: "web", Route: "/api", StripPrefix: true, Image: "r.test/api@sha256:" + strings.Repeat("b", 64), Port: 8080, HealthPath: "/healthz", Replicas: 1},
		{Name: "worker", Kind: "worker", Image: "r.test/w@sha256:" + strings.Repeat("c", 64), Replicas: 1},
	}
	return a
}

func TestApplyMapsServices(t *testing.T) {
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(context.Background(), withServices(1)); err != nil {
		t.Fatal(err)
	}
	got := get(t, c).Spec.Services
	want := []v1alpha1.ServiceSpec{
		{Name: "web", Kind: v1alpha1.ServiceWeb, Route: "/", Image: "r.test/web@sha256:" + strings.Repeat("a", 64), Port: 3000, HealthPath: "/", Replicas: 2},
		{Name: "api", Kind: v1alpha1.ServiceWeb, Route: "/api", StripPrefix: true, Image: "r.test/api@sha256:" + strings.Repeat("b", 64), Port: 8080, HealthPath: "/healthz", Replicas: 1},
		{Name: "worker", Kind: v1alpha1.ServiceWorker, Image: "r.test/w@sha256:" + strings.Repeat("c", 64), Replicas: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("services: %+v", got)
	}

	// an app without services keeps the top-level fields only
	s, c = newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(context.Background(), app(1)); err != nil {
		t.Fatal(err)
	}
	if a := get(t, c); a.Spec.Services != nil || a.Spec.Image == "" {
		t.Errorf("legacy: %+v", a.Spec)
	}
}

func TestApplyRefusesBadServices(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	for name, mutate := range map[string]func(*fleetv1.App){
		"name with a hyphen": func(a *fleetv1.App) { a.Services[1].Name = "my-api" },
		"name too long":      func(a *fleetv1.App) { a.Services[1].Name = strings.Repeat("a", 21) },
		"repeated name":      func(a *fleetv1.App) { a.Services[1].Name = "web" },
		"unknown kind":       func(a *fleetv1.App) { a.Services[2].Kind = "cron" },
		"two at one route":   func(a *fleetv1.App) { a.Services[1].Route = "/" },
		"too many": func(a *fleetv1.App) {
			for i := range MaxServices {
				a.Services = append(a.Services, &fleetv1.Service{Name: "w" + string(rune('a'+i)), Kind: "worker"})
			}
		},
	} {
		a := withServices(1)
		mutate(a)
		if err := s.Apply(context.Background(), a); !IsRejected(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// two internal services have no route: that is no clash
	a := withServices(1)
	a.Services[2] = &fleetv1.Service{Name: "auth", Kind: "web", Image: a.Services[1].Image, Port: 9000, HealthPath: "/", Replicas: 1}
	a.Services = append(a.Services, &fleetv1.Service{Name: "queue", Kind: "worker", Image: a.Services[1].Image, Replicas: 1})
	if err := s.Apply(context.Background(), a); err != nil {
		t.Errorf("internal services: %v", err)
	}
}

func TestStatusesCarryEachService(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(ctx, withServices(2)); err != nil {
		t.Fatal(err)
	}
	a := get(t, c)
	a.Status = v1alpha1.OdysseyAppStatus{ObservedRevision: 2, Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonCrash, Message: "worker: restarted",
		Services: []v1alpha1.ServiceStatus{
			{Name: "web", Phase: v1alpha1.PhaseReady, ReadyReplicas: 2},
			{Name: "worker", Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonCrash, Message: strings.Repeat("x", 900) + "\x1b"},
		}}
	if err := c.Status().Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx, env, "portal")
	if err != nil {
		t.Fatal(err)
	}
	svcs := st.GetServices()
	if len(svcs) != 2 || svcs[0].GetName() != "web" || svcs[0].GetPhase() != "ready" || svcs[0].GetReadyReplicas() != 2 ||
		svcs[1].GetReason() != "crash" || len(svcs[1].GetMessage()) != maxServiceMessage {
		t.Errorf("services: %+v", svcs)
	}
}

func TestServicesCapabilityIsAlwaysReported(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	apis := s.Capabilities(context.Background()).GetApis()
	if !slices.Contains(apis, CapabilityServices) {
		t.Error("the agent runs services: it must say so")
	}
	if !slices.Contains(apis, CapabilityServiceSettings) {
		t.Error("the agent scopes settings by service: it must say so")
	}
}

// The services a setting names reach the CRD sorted (the same set makes the
// same spec), and a service's own env with it.
func TestApplyMapsSettingScopes(t *testing.T) {
	s, c := newSyncer(t, interceptor.Funcs{})
	a := withServices(1)
	a.Services[1].Env = map[string]string{"LOG_LEVEL": "debug"}
	a.Secrets = []*fleetv1.SecretRef{{Name: "STRIPE_KEY", Source: "managed", Version: 1, Services: []string{"worker", "api"}}}
	a.Resources = []*fleetv1.ResourceRef{{Name: "main", Kind: "postgres", Services: []string{"api"}}}
	a.Ai = &fleetv1.AI{BaseUrl: "https://llm.test", KeySecret: "STRIPE_KEY", Services: []string{"api"}}
	if err := s.Apply(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	got := get(t, c).Spec
	if !slices.Equal(got.Secrets[0].Services, []string{"api", "worker"}) || !slices.Equal(got.Resources[0].Services, []string{"api"}) ||
		got.AI == nil || !slices.Equal(got.AI.Services, []string{"api"}) {
		t.Errorf("scopes: %+v %+v %+v", got.Secrets, got.Resources, got.AI)
	}
	if got.Services[1].Env["LOG_LEVEL"] != "debug" || got.Services[0].Env != nil {
		t.Errorf("service env: %+v", got.Services)
	}
}

func TestApplyRefusesBadScopes(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	for name, mutate := range map[string]func(*fleetv1.App){
		"secret names a bad service": func(a *fleetv1.App) {
			a.Secrets = []*fleetv1.SecretRef{{Name: "K", Source: "managed", Version: 1, Services: []string{"my-api"}}}
		},
		"resource repeats a service": func(a *fleetv1.App) {
			a.Resources = []*fleetv1.ResourceRef{{Name: "main", Kind: "postgres", Services: []string{"api", "api"}}}
		},
		"ai names too many": func(a *fleetv1.App) {
			a.Ai = &fleetv1.AI{BaseUrl: "https://llm.test", KeySecret: "K", Services: strings.Fields("a b c d e f g h i")}
		},
		"service env with a bad name": func(a *fleetv1.App) { a.Services[0].Env = map[string]string{"1BAD": "x"} },
	} {
		a := withServices(1)
		mutate(a)
		if err := s.Apply(context.Background(), a); !IsRejected(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
