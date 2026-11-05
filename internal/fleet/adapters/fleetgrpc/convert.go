package fleetgrpc

import (
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

func agentFromProto(h *fleetv1.Hello) fleet.Agent {
	c := h.GetCapabilities()
	return fleet.Agent{Version: h.GetAgentVersion(), Capabilities: fleet.Capabilities{
		KubernetesVersion: c.GetKubernetesVersion(),
		APIs:              nonNil(c.GetApis()),
		IngressClasses:    nonNil(c.GetIngressClasses()),
		StorageClasses:    nonNil(c.GetStorageClasses()),
		Builder:           builderFromProto(c.GetBuilder()),
		SecretKey:         secretKeyFromProto(c.GetSecretKey()),
		SecretStores:      nonNil(c.GetSecretStores()),
		ResourceKinds:     nonNil(c.GetResourceKinds()),
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
	secrets := make([]*fleetv1.SecretRef, 0, len(a.Secrets))
	for _, s := range a.Secrets {
		secrets = append(secrets, &fleetv1.SecretRef{Name: s.Name, Source: s.Source, Version: s.Version,
			Store: s.Store, Key: s.Key, Property: s.Property})
	}
	resources := make([]*fleetv1.ResourceRef, 0, len(a.Resources))
	for _, r := range a.Resources {
		resources = append(resources, &fleetv1.ResourceRef{Name: r.Name, Kind: string(r.Kind), Prefix: r.Prefix})
	}
	var ai *fleetv1.AI
	if a.AI != nil {
		ai = &fleetv1.AI{BaseUrl: a.AI.BaseURL, KeySecret: a.AI.KeySecret}
	}
	return &fleetv1.App{
		Resources: resources, Ai: ai,
		OrganizationId: a.Organization.String(), ProjectId: a.Project.String(), EnvironmentId: a.Environment.String(),
		Name: a.Name, Revision: a.Revision, Image: a.Image, Port: int32(a.Port), HealthPath: a.HealthPath,
		Replicas: int32(a.Replicas), Env: a.Env, Labels: a.Labels, Access: &fleetv1.Access{Mode: a.Access}, Secrets: secrets,
	}
}

func secretToProto(d fleet.SecretDelivery) *fleetv1.ControlMessage {
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_Secret{Secret: &fleetv1.Secret{
		DeliveryId: d.ID.String(), OrganizationId: d.Organization.String(), ProjectId: d.Project.String(),
		EnvironmentId: d.Environment.String(), App: d.App, Name: d.Name, Version: d.Version, KeyId: d.KeyID, Sealed: d.Sealed,
	}}}
}

func policyToProto(p fleet.Policy) *fleetv1.ControlMessage {
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_ClusterPolicy{ClusterPolicy: &fleetv1.ClusterPolicy{
		Generation: p.Generation,
		Egress:     &fleetv1.EgressPolicy{Mode: string(p.Egress.Mode), Allow: p.Egress.Allow},
	}}}
}

func policyAckFromProto(a *fleetv1.PolicyAck) fleet.PolicyAck {
	return fleet.PolicyAck{Generation: a.GetGeneration(), Error: a.GetError(), Warnings: a.GetWarnings()}
}

func secretAckFromProto(a *fleetv1.SecretAck) (fleet.SecretAck, error) {
	id, err := fleet.ParseSecretDeliveryID(a.GetDeliveryId())
	if err != nil {
		return fleet.SecretAck{}, err
	}
	return fleet.SecretAck{Delivery: id, Error: a.GetError()}, nil
}

func secretKeyFromProto(k *fleetv1.SecretKey) *fleet.SecretKey {
	if k == nil {
		return nil
	}
	return &fleet.SecretKey{KeyID: k.GetKeyId(), Public: k.GetPublicKey(), Suite: k.GetSuite()}
}

func builderFromProto(b *fleetv1.Builder) *fleet.Builder {
	if b == nil {
		return nil
	}
	return &fleet.Builder{Registry: b.GetRegistry(), Platforms: nonNil(b.GetPlatforms())}
}

func buildToProto(b fleet.DesiredBuild) *fleetv1.ControlMessage {
	if b.Cancelled || b.Final {
		return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_CancelBuild{CancelBuild: &fleetv1.CancelBuild{BuildId: b.ID.String()}}}
	}
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_Build{Build: &fleetv1.Build{
		BuildId: b.ID.String(), OrganizationId: b.Organization.String(), ProjectId: b.Project.String(),
		EnvironmentId: b.Environment.String(), Source: &fleetv1.SourceRef{Sha256: b.SourceSHA256, Size: b.SourceSize},
		ImageRepository: b.ImageRepository, CacheRef: b.CacheRef, TimeoutSeconds: int32(b.Timeout.Seconds()),
	}}}
}

// buildReportFromProto skips unreadable ids (validation drops the rest).
func buildReportFromProto(r *fleetv1.BuildReport) []fleet.BuildStatus {
	out := make([]fleet.BuildStatus, 0, len(r.GetBuilds()))
	for _, b := range r.GetBuilds() {
		id, err := fleet.ParseBuildID(b.GetBuildId())
		if err != nil {
			continue
		}
		out = append(out, fleet.BuildStatus{Build: id, Phase: fleet.BuildPhase(b.GetPhase()), Digest: b.GetImageDigest(),
			Reason: b.GetReason(), Message: b.GetMessage()})
	}
	return out
}

func buildLogFromProto(l *fleetv1.BuildLog) (fleet.BuildLog, error) {
	id, err := fleet.ParseBuildID(l.GetBuildId())
	if err != nil {
		return fleet.BuildLog{}, err
	}
	out := fleet.BuildLog{Build: id, Lines: make([]fleet.LogLine, 0, len(l.GetLines()))}
	for _, x := range l.GetLines() {
		out.Lines = append(out.Lines, fleet.LogLine{Seq: x.GetSeq(), Stream: x.GetStream(), Text: x.GetText()})
	}
	return out, nil
}

func resourceToProto(d fleet.DesiredResource) *fleetv1.ControlMessage {
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_Resource{Resource: &fleetv1.Resource{
		OrganizationId: d.Organization.String(), ProjectId: d.Project.String(), EnvironmentId: d.Environment.String(),
		Name: d.Name, Kind: string(d.Kind), Generation: d.Generation, Storage: resourcekind.Storage(d.StorageGi), Deleted: d.Deleted,
	}}}
}

// resourceReportFromProto rejects the whole report when a status is
// unreadable, like reportFromProto.
func resourceReportFromProto(r *fleetv1.ResourceReport) (fleet.ResourceReport, error) {
	out := fleet.ResourceReport{Full: r.GetFull(), Resources: make([]fleet.ResourceStatus, 0, len(r.GetResources()))}
	for _, s := range r.GetResources() {
		env, err := project.ParseEnvironmentID(s.GetEnvironmentId())
		if err != nil {
			return fleet.ResourceReport{}, err
		}
		out.Resources = append(out.Resources, fleet.ResourceStatus{Environment: env, Name: s.GetName(),
			ObservedGeneration: s.GetObservedGeneration(), Phase: fleet.ResourcePhase(s.GetPhase()), Message: s.GetMessage()})
	}
	return out, nil
}
