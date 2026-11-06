package fleet_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
)

func TestAppServicesAreValidated(t *testing.T) {
	img := "r/web@sha256:" + strings.Repeat("a", 64)
	several := func() fleet.App {
		return fleet.App{Organization: org, Project: proj, Environment: env, Cluster: cluster, Name: "shop", Revision: 1,
			Services: []fleet.AppService{
				{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 2},
				{Name: "api", Kind: fleet.ServiceWeb, Route: "/api", StripPrefix: true, Image: img, Port: 8080, HealthPath: "/healthz", Replicas: 1},
				{Name: "worker", Kind: fleet.ServiceWorker, Image: img, Replicas: 1},
			}}
	}
	if err := several().Validate(); err != nil {
		t.Fatalf("valid app: %v", err)
	}
	if several().Simple() {
		t.Error("three services are not simple")
	}
	for name, mutate := range map[string]func(*fleet.App){
		"top-level image with several": func(a *fleet.App) { a.Image = img },
		"top-level replicas":           func(a *fleet.App) { a.Replicas = 1 },
		"repeated name":                func(a *fleet.App) { a.Services[1].Name = "web" },
		"bad name":                     func(a *fleet.App) { a.Services[1].Name = "my-api" },
		"image by tag":                 func(a *fleet.App) { a.Services[0].Image = "r/web:latest" },
		"repeated route":               func(a *fleet.App) { a.Services[1].Route = "/" },
		"worker with a route":          func(a *fleet.App) { a.Services[2].Route = "/w" },
		"worker with a port":           func(a *fleet.App) { a.Services[2].Port = 80 },
		"web without a port":           func(a *fleet.App) { a.Services[1].Port = 0 },
		"strip at /":                   func(a *fleet.App) { a.Services[0].StripPrefix = true },
		"unknown kind":                 func(a *fleet.App) { a.Services[2].Kind = "cron" },
		"no replicas":                  func(a *fleet.App) { a.Services[0].Replicas = 0 },
		"too many": func(a *fleet.App) {
			for i := range fleet.MaxAppServices {
				a.Services = append(a.Services, fleet.AppService{Name: "w" + string(rune('a'+i)), Kind: fleet.ServiceWorker, Image: img, Replicas: 1})
			}
		},
	} {
		a := several()
		mutate(&a)
		if a.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// one web service at "/" keeps the top-level fields (older agents run them)
	one := fleet.App{Organization: org, Project: proj, Environment: env, Cluster: cluster, Name: "web", Revision: 1, Image: img, Port: 3000,
		HealthPath: "/", Replicas: 1, Services: []fleet.AppService{{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 1}}}
	if !one.Simple() || one.Validate() != nil {
		t.Errorf("one service: simple=%v %v", one.Simple(), one.Validate())
	}
	one.Image = ""
	if one.Validate() == nil {
		t.Error("a simple app needs its top-level image")
	}
}

func TestServiceStatusesAreBounded(t *testing.T) {
	ok := fleet.AppStatus{Environment: env, Name: "shop", Phase: fleet.PhaseReady, Services: []fleet.ServiceStatus{{Name: "web", Phase: fleet.PhaseReady, ReadyReplicas: 1}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]fleet.ServiceStatus{
		"bad name":     {Name: "Web", Phase: fleet.PhaseReady},
		"deleted":      {Name: "web", Phase: fleet.PhaseDeleted},
		"bad reason":   {Name: "web", Phase: fleet.PhaseFailed, Reason: "boom"},
		"long message": {Name: "web", Phase: fleet.PhaseFailed, Message: strings.Repeat("x", fleet.MaxServiceMessage+1)},
		"control":      {Name: "web", Phase: fleet.PhaseFailed, Message: "a\x1bb"},
		"negative":     {Name: "web", Phase: fleet.PhaseReady, ReadyReplicas: -1},
	} {
		bad := ok
		bad.Services = []fleet.ServiceStatus{s}
		if bad.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	twice := ok
	twice.Services = append(twice.Services, twice.Services[0])
	if twice.Validate() == nil {
		t.Error("a repeated service was accepted")
	}
}
