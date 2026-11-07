package project

import (
	"slices"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/gate"
)

// What IT's rules see of a change of an app's services (MS-3c,
// docs/policy.md "set_services"), and what project asks IT to approve.

// OpSetServices is the "op" of the approval payload: which action of project
// to run again once approved.
const OpSetServices = "project.set_services"

// SetServicesApproval is the payload of a held set_services: the list as it
// will be stored (deterministic: offered back to redeem the approval).
type SetServicesApproval struct {
	Op    string        `json:"op"`
	Items []ServiceSpec `json:"items"`
}

// ServicesChange is the answer to SetServices: the app's services, or
// (Outcome held) the request IT must decide, with nothing changed.
type ServicesChange struct {
	gate.Outcome
	Items []Service `json:"items"`
}

// GateServices are an app's services as IT's rules see them.
func GateServices(services []Service) []gate.Service {
	out := make([]gate.Service, len(services))
	for i, s := range services {
		out[i] = gate.Service{Name: s.Name, Kind: string(s.Kind), Route: s.Route, Public: s.Public()}
	}
	return out
}

// ServicesRequest is a change of an app's services as IT's rules see it:
// the new list, the services added, removed or modified (in the new list's
// order, the removed after) and, always, a change of topology.
func ServicesRequest(p Project, next, before []Service) gate.Request {
	changed := []string{}
	byName := make(map[string]Service, len(before))
	for _, s := range before {
		byName[s.Name] = s
	}
	for _, s := range next {
		if old, ok := byName[s.Name]; !ok || !SameServices([]Service{old}, []Service{s}) {
			changed = append(changed, s.Name)
		}
		delete(byName, s.Name)
	}
	for _, s := range before {
		if _, gone := byName[s.Name]; gone {
			changed = append(changed, s.Name)
		}
	}
	return gate.Request{Action: gate.ActionSetServices, Tier: string(p.Tier), ProjectSlug: p.Slug,
		Services: GateServices(next), Changed: slices.Clip(changed), TopologyChanged: true}
}

// ServicesSummary is the held change in one plain Spanish sentence.
func ServicesSummary(p Project, next []Service) string {
	names := make([]string, len(next))
	for i, s := range next {
		names[i] = s.Name
	}
	if len(next) == 1 {
		return "Cambiar los servicios de «" + p.Name + "» (queda 1: " + names[0] + ")"
	}
	return "Cambiar los servicios de «" + p.Name + "» (quedan " + strconv.Itoa(len(next)) + ": " + joinNames(names) + ")"
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
		case i == len(names)-1:
			out += " y "
		default:
			out += ", "
		}
		out += n
	}
	return out
}
