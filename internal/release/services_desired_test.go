package release_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// D5: a version of one web service at "/" keeps the top-level fields (an
// agent that runs one workload per app runs it as before); any other leaves
// them empty, so such an agent fails visibly instead of serving half.
func TestDesiredLeavesTheTopLevelToSimpleVersions(t *testing.T) {
	img := "r.local/web@" + digest
	cfg := release.Config{Port: 3000, HealthPath: "/", Replicas: 2}
	d := release.Deployment{Revision: 4}
	simple := release.Release{Image: img, Config: cfg, Services: []release.Service{
		{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: img, Port: 3000, HealthPath: "/", Replicas: 2}}}
	if got := release.Desired("shop-prod", d, simple); got.Image != img || got.Port != 3000 || got.Replicas != 2 || len(got.Services) != 1 {
		t.Errorf("simple: %+v", got)
	}
	if got := release.Desired("shop-prod", d, release.Release{Image: img, Config: cfg}); got.Image != img {
		t.Errorf("a version from before services: %+v", got)
	}
	several := simple
	several.Services = append(several.Services, release.Service{Name: "worker", Kind: project.ServiceWorker, Image: img, Replicas: 1})
	if got := release.Desired("shop-prod", d, several); got.Image != "" || got.Port != 0 || got.HealthPath != "" || got.Replicas != 0 || len(got.Services) != 2 {
		t.Errorf("several: %+v", got)
	}
	stripped := simple
	stripped.Services = []release.Service{{Name: "api", Kind: project.ServiceWeb, Route: "/api", StripPrefix: true, Image: img, Port: 3000, HealthPath: "/", Replicas: 1}}
	if release.OneWebService(stripped.Services) {
		t.Error("a service at /api is not the whole address")
	}
}
