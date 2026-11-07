package release

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// What IT's rules see of the services an action brings (MS-3c, docs/policy.md
// "services"): the same words for a deploy, a rollback and set_services, and
// for an app of one service exactly what an app of several would show.

// GateServices are services as IT's rules see them.
func GateServices(services []Service) []gate.Service {
	out := make([]gate.Service, len(services))
	for i, s := range services {
		out[i] = gate.Service{Name: s.Name, Kind: string(s.Kind), Route: s.Route,
			Public: s.Kind == project.ServiceWeb && s.Route != ""}
	}
	return out
}

// Compared is a version against the one an environment serves.
type Compared struct {
	// Changed names the services whose running image changes, that appear
	// or that go away (in the version's order, the removed ones after).
	Changed []string
	// TopologyChanged says a service appears, goes away or changes how it
	// is laid out (kind, folder, route, prefix).
	TopologyChanged bool
}

// Compare is next against serving (nil: the environment serves nothing,
// so every service is new and the topology changes).
func Compare(next []Service, serving *Release) Compared {
	out := Compared{Changed: []string{}}
	var before []Service
	if serving != nil {
		before = serving.Services
	}
	if len(before) == 0 {
		for _, s := range next {
			out.Changed = append(out.Changed, s.Name)
		}
		out.TopologyChanged = true
		return out
	}
	byName := make(map[string]Service, len(before))
	for _, s := range before {
		byName[s.Name] = s
	}
	for _, s := range next {
		old, ok := byName[s.Name]
		switch {
		case !ok:
			out.TopologyChanged = true
			out.Changed = append(out.Changed, s.Name)
		case !sameLayout(old, s):
			out.TopologyChanged = true
			out.Changed = append(out.Changed, s.Name)
		case old.Image != s.Image:
			out.Changed = append(out.Changed, s.Name)
		}
		delete(byName, s.Name)
	}
	for _, s := range before { // gone, in the serving version's order
		if _, gone := byName[s.Name]; gone {
			out.TopologyChanged = true
			out.Changed = append(out.Changed, s.Name)
		}
	}
	return out
}

// CompareLayout is the first judgment of a deployment from source: what it
// will run is known only as a layout (its images come with the build), so
// Changed names only the services that appear, go away or are laid out
// differently; those whose code changed join at the second judgment.
func CompareLayout(next []project.Service, serving *Release) Compared {
	layout := make([]Service, len(next))
	for i, s := range next {
		layout[i] = Service{Name: s.Name, Kind: s.Kind, Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix}
	}
	if serving == nil || len(serving.Services) == 0 {
		return Compare(layout, serving)
	}
	// the serving version's images, where the layout is the same: an image
	// not built yet is not a change yet
	byName := make(map[string]Service, len(serving.Services))
	for _, s := range serving.Services {
		byName[s.Name] = s
	}
	for i, s := range layout {
		if old, ok := byName[s.Name]; ok && sameLayout(old, s) {
			layout[i].Image = old.Image
		}
	}
	return Compare(layout, serving)
}

func sameLayout(a, b Service) bool {
	return a.Kind == b.Kind && a.Root == b.Root && a.Route == b.Route && a.StripPrefix == b.StripPrefix
}

// Judged fills request with what a version brings against what serves.
func (c Compared) Judged(request gate.Request, next []gate.Service) gate.Request {
	request.Services = next
	request.Changed = slices.Clone(c.Changed)
	request.TopologyChanged = c.TopologyChanged
	return request
}
