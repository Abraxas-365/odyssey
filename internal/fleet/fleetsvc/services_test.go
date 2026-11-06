package fleetsvc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
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
