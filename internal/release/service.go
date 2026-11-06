package release

import (
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

// ServicesOf is the snapshot of a version made of one image: every service
// of the app's topology runs it with the version's settings. Until each
// service is built on its own (MS-3) only a Simple topology has a version:
// any other is refused with CodeServicesUnavailable.
func ServicesOf(topology []project.Service, image string, c Config) ([]Service, error) {
	if !project.Simple(topology) {
		return nil, errx.Business(project.ServicesUnavailableMessage).WithCode(project.CodeServicesUnavailable)
	}
	s := topology[0]
	return []Service{{Name: s.Name, Kind: s.Kind, Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix,
		Image: image, Port: c.Port, HealthPath: c.HealthPath, Replicas: c.Replicas}}, nil
}
