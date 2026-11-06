package release

import (
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Service is one service as a version runs it: the app's topology when the
// version was made (name, kind, folder, route) and how that service runs
// (its image, port, health check, replicas). A version never changes, so
// deploying it again brings back exactly these services.
type Service struct {
	Name        string              `json:"name" db:"name"`
	Kind        project.ServiceKind `json:"kind" db:"kind"`
	Root        string              `json:"root" db:"root"`
	Route       string              `json:"route" db:"route"`
	StripPrefix bool                `json:"strip_prefix" db:"strip_prefix"`
	Image       string              `json:"image" db:"image"`
	// Port and HealthPath are 0 and "" for a worker (it answers nothing).
	Port       int    `json:"port" db:"port"`
	HealthPath string `json:"health_path" db:"health_path"`
	Replicas   int    `json:"replicas" db:"replicas"`
}

// ServicesOf is the snapshot of a version: each service of topology, in
// order, runs images[i] with the version's settings (a web service on its
// port and health check; a worker answers nothing, so it has neither).
func ServicesOf(topology []project.Service, images []string, c Config) ([]Service, error) {
	return snapshot(topology, images, c)
}

func snapshot(topology []project.Service, images []string, c Config) ([]Service, error) {
	if len(topology) == 0 || len(images) != len(topology) {
		return nil, errx.Internal("a version needs one image per service")
	}
	out := make([]Service, len(topology))
	for i, s := range topology {
		out[i] = Service{Name: s.Name, Kind: s.Kind, Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix,
			Image: images[i], Replicas: c.Replicas}
		if s.Kind == project.ServiceWeb {
			out[i].Port, out[i].HealthPath = c.Port, c.HealthPath
		}
	}
	return out, nil
}

// CodeImageNeedsOneService refuses publishing an image to an app of
// several services: one image cannot be all of them (each is built from
// its own folder).
const CodeImageNeedsOneService = "IMAGE_NEEDS_ONE_SERVICE"

// ImageServices is the snapshot of a version made of one published image:
// only an app of one service has one.
func ImageServices(topology []project.Service, image string, c Config) ([]Service, error) {
	if len(topology) != 1 {
		return nil, errx.Business("La app tiene varios servicios: una sola imagen no alcanza para todos. Publica su código para construir cada uno.").
			WithCode(CodeImageNeedsOneService)
	}
	return snapshot(topology, []string{image}, c)
}

// ServiceBuild is one service of a deployment from source: the service as
// the app had it when the deployment was asked for (the version is made of
// exactly these, whatever changes meanwhile) and the build of its image.
type ServiceBuild struct {
	Name        string              `json:"name" db:"name"`
	Kind        project.ServiceKind `json:"kind" db:"kind"`
	Root        string              `json:"root" db:"root"`
	Route       string              `json:"route" db:"route"`
	StripPrefix bool                `json:"strip_prefix" db:"strip_prefix"`
	Build       build.ID            `json:"build_id" db:"build_id"`
}

// Service is the topology entry the build was asked for.
func (b ServiceBuild) Service() project.Service {
	return project.Service{Name: b.Name, Kind: b.Kind, Root: b.Root, Route: b.Route, StripPrefix: b.StripPrefix}
}

// BuildsFor are the builds of a deployment from source of topology: one per
// service, each with a new build ID.
func BuildsFor(topology []project.Service) []ServiceBuild {
	out := make([]ServiceBuild, len(topology))
	for i, s := range topology {
		out[i] = ServiceBuild{Name: s.Name, Kind: s.Kind, Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix, Build: build.NewID()}
	}
	return out
}
