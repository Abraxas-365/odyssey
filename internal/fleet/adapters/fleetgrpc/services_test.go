package fleetgrpc

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

func TestAppToProtoCarriesTheServices(t *testing.T) {
	img := "r.local/api@sha256:" + strings.Repeat("b", 64)
	got := appToProto(fleet.App{Name: "shop", Services: []fleet.AppService{
		{Name: "web", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 2},
		{Name: "api", Kind: fleet.ServiceWeb, Route: "/api", StripPrefix: true, Image: img, Port: 8080, HealthPath: "/healthz", Replicas: 1},
		{Name: "worker", Kind: fleet.ServiceWorker, Image: img, Replicas: 1},
	}})
	svcs := got.GetServices()
	if len(svcs) != 3 || svcs[1].GetRoute() != "/api" || !svcs[1].GetStripPrefix() || svcs[1].GetPort() != 8080 ||
		svcs[2].GetKind() != "worker" || svcs[0].GetReplicas() != 2 || got.GetImage() != "" {
		t.Errorf("services: %+v", got)
	}
}

func TestAppToProtoCarriesTheScopes(t *testing.T) {
	img := "r.local/api@sha256:" + strings.Repeat("b", 64)
	got := appToProto(fleet.App{Name: "shop",
		Services: []fleet.AppService{{Name: "api", Kind: fleet.ServiceWeb, Route: "/", Image: img, Port: 8080, HealthPath: "/", Replicas: 1,
			Env: map[string]string{"LOG": "debug"}}},
		Secrets:   []fleet.AppSecret{{Name: "K", Source: fleet.SecretManaged, Version: 1, Services: []string{"api"}}},
		Resources: []fleet.ResourceRef{{Name: "main", Kind: "postgres", Services: []string{"api"}}},
		AI:        &fleet.AIRef{BaseURL: "https://llm.test", KeySecret: "K", Services: []string{"api"}},
	})
	if got.GetServices()[0].GetEnv()["LOG"] != "debug" || got.GetSecrets()[0].GetServices()[0] != "api" ||
		got.GetResources()[0].GetServices()[0] != "api" || got.GetAi().GetServices()[0] != "api" {
		t.Errorf("scopes: %+v", got)
	}
}

func TestReportFromProtoCarriesEachService(t *testing.T) {
	r, err := reportFromProto(&fleetv1.AppReport{Apps: []*fleetv1.AppStatus{{EnvironmentId: project.NewEnvironmentID().String(), Name: "shop",
		Phase: "failed", Reason: "crash", Message: "worker: restarted", Services: []*fleetv1.ServiceStatus{
			{Name: "web", Phase: "ready", ReadyReplicas: 2},
			{Name: "worker", Phase: "failed", Reason: "crash", Message: "restarted"},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Apps[0].Services
	if len(s) != 2 || s[0].ReadyReplicas != 2 || s[1].Phase != fleet.PhaseFailed || s[1].Reason != fleet.ReasonCrash {
		t.Fatalf("services: %+v", s)
	}
	if _, err := r.Valid(); err != nil {
		t.Error(err)
	}
}
