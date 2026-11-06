package release_test

import (
	"errors"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

var topology = []project.Service{
	{Name: "web", Kind: project.ServiceWeb, Root: "apps/web", Route: "/"},
	{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true},
	{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"},
}

// MS-3a: a version is each service with its own image; a worker has no port
// nor health check; one image per service or none.
func TestServicesOfOneImageEach(t *testing.T) {
	cfg := release.Config{Port: 3000, HealthPath: "/health", Replicas: 2}
	images := []string{"r/web@" + digest, "r/api@" + digest, "r/jobs@" + digest}
	got, err := release.ServicesOf(topology, images, cfg)
	if err != nil || len(got) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[1] != (release.Service{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true,
		Image: images[1], Port: 3000, HealthPath: "/health", Replicas: 2}) {
		t.Errorf("api %+v", got[1])
	}
	if got[2] != (release.Service{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs", Image: images[2], Replicas: 2}) {
		t.Errorf("worker %+v", got[2])
	}
	if _, err := release.ServicesOf(topology, images[:2], cfg); err == nil {
		t.Error("two images for three services")
	}
	if _, err := release.ServicesOf(nil, nil, cfg); err == nil {
		t.Error("a version of no services")
	}
}

func TestImageServicesNeedsOneService(t *testing.T) {
	cfg := release.Config{Port: 3000, HealthPath: "/", Replicas: 1}
	if got, err := release.ImageServices(topology[:1], "r/web@"+digest, cfg); err != nil || len(got) != 1 || got[0].Image != "r/web@"+digest {
		t.Fatalf("one service: %+v %v", got, err)
	}
	_, err := release.ImageServices(topology, "r/web@"+digest, cfg)
	var e *errx.Error
	if !errors.As(err, &e) || e.Code != release.CodeImageNeedsOneService || e.Type != errx.TypeBusiness {
		t.Fatalf("one image for three services: %v", err)
	}
}

func TestBuildsForAndBuildIDs(t *testing.T) {
	builds := release.BuildsFor(topology)
	if len(builds) != 3 || builds[1].Service() != (project.Service{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true}) {
		t.Fatalf("builds %+v", builds)
	}
	seen := map[build.ID]bool{}
	for _, b := range builds {
		seen[b.Build] = true
	}
	if len(seen) != 3 {
		t.Fatal("two services share a build")
	}
	if ids := (release.Deployment{Build: &builds[0].Build, Builds: builds}).BuildIDs(); len(ids) != 3 || ids[2] != builds[2].Build {
		t.Errorf("ids %v", ids)
	}
	old := build.NewID()
	if ids := (release.Deployment{Build: &old}).BuildIDs(); len(ids) != 1 || ids[0] != old {
		t.Errorf("a deployment from before services: %v", ids)
	}
	if ids := (release.Deployment{}).BuildIDs(); len(ids) != 0 {
		t.Errorf("an image: %v", ids)
	}
}
