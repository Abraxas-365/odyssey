package fleetgrpc

import (
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

func agentFromProto(h *fleetv1.Hello) fleet.Agent {
	c := h.GetCapabilities()
	return fleet.Agent{Version: h.GetAgentVersion(), Capabilities: fleet.Capabilities{
		KubernetesVersion: c.GetKubernetesVersion(),
		APIs:              nonNil(c.GetApis()),
		IngressClasses:    nonNil(c.GetIngressClasses()),
		StorageClasses:    nonNil(c.GetStorageClasses()),
	}}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// reportFromProto rejects the whole report when an app is unreadable (the
// agent is buggy or hostile; its next full report will be tried again).
func reportFromProto(r *fleetv1.AppReport) (fleet.Report, error) {
	out := fleet.Report{Full: r.GetFull(), Apps: make([]fleet.AppStatus, 0, len(r.GetApps()))}
	for _, a := range r.GetApps() {
		env, err := project.ParseEnvironmentID(a.GetEnvironmentId())
		if err != nil {
			return fleet.Report{}, err
		}
		out.Apps = append(out.Apps, fleet.AppStatus{
			Environment: env, Name: a.GetName(), ObservedRevision: a.GetObservedRevision(),
			Phase: fleet.Phase(a.GetPhase()), Reason: fleet.Reason(a.GetReason()), Message: a.GetMessage(),
			ReadyReplicas: int(a.GetReadyReplicas()), URL: a.GetUrl(),
		})
	}
	return out, nil
}

func appToProto(a fleet.App) *fleetv1.App {
	return &fleetv1.App{
		OrganizationId: a.Organization.String(), ProjectId: a.Project.String(), EnvironmentId: a.Environment.String(),
		Name: a.Name, Revision: a.Revision, Image: a.Image, Port: int32(a.Port), HealthPath: a.HealthPath,
		Replicas: int32(a.Replicas), Env: a.Env, Labels: a.Labels,
	}
}
