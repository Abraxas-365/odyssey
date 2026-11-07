package release

import "github.com/Abraxas-365/odyssey/internal/project"

// ServiceRef names one service of a version for the read models that only
// list them (the inventory): what it is and where the address reaches it.
type ServiceRef struct {
	Name  string              `json:"name"`
	Kind  project.ServiceKind `json:"kind"`
	Route string              `json:"route"`
}

// ServiceRefs are the refs of services, in their order (never nil).
func ServiceRefs(services []Service) []ServiceRef {
	out := make([]ServiceRef, len(services))
	for i, s := range services {
		out[i] = ServiceRef{Name: s.Name, Kind: s.Kind, Route: s.Route}
	}
	return out
}

// ServiceState is how one service of the version an environment serves
// stands in its cluster right now (the console's per-service view).
type ServiceState struct {
	Name  string              `json:"name"`
	Kind  project.ServiceKind `json:"kind"`
	Route string              `json:"route"`
	// Phase is "" while the cluster has not reported on the app yet.
	Phase   Phase         `json:"phase"`
	Reason  FailureReason `json:"reason"`
	Message string        `json:"message"`
	// ReadyReplicas of Replicas (the version's setting for the service).
	ReadyReplicas int `json:"ready_replicas"`
	Replicas      int `json:"replicas"`
}

// ServiceStates joins the services of the serving version, in order, with
// what the cluster reports of the app. A status that describes no revision
// is unknown: every phase is "". A cluster that reports per service is
// believed for each service (one it does not mention is unknown); one that
// does not (empty status.Services) gives every service the aggregate phase,
// reason and message, and its ready replicas only when the app is that one
// service (they cannot be told apart otherwise).
func ServiceStates(services []Service, status AppStatus) []ServiceState {
	out := make([]ServiceState, len(services))
	byName := make(map[string]ServiceStatus, len(status.Services))
	for _, s := range status.Services {
		byName[s.Name] = s
	}
	for i, s := range services {
		st := ServiceState{Name: s.Name, Kind: s.Kind, Route: s.Route, Replicas: s.Replicas}
		switch {
		case status.ObservedRevision == 0:
		case len(status.Services) > 0:
			if own, ok := byName[s.Name]; ok {
				st.Phase, st.Reason, st.Message, st.ReadyReplicas = own.Phase, own.Reason, own.Message, own.ReadyReplicas
			}
		default:
			st.Phase, st.Reason, st.Message = status.Phase, status.Reason, status.Message
			if len(services) == 1 {
				st.ReadyReplicas = status.ReadyReplicas
			}
		}
		out[i] = st
	}
	return out
}
