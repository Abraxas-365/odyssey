package release

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Target is the environment an app runs in. The Cluster adapter decides
// which cluster that is (fleet placement, phase 1a-a3).
type Target struct {
	Organization identity.OrganizationID
	Project      project.ID
	Environment  project.EnvironmentID
}

// DesiredApp is what the cluster must run: the spec of the OdysseyApp
// custom resource the agent reconciles.
type DesiredApp struct {
	// Name is the app's name in the cluster (AppName): a DNS label.
	Name string `json:"name"`
	// Revision increases with every deployment of the environment; the
	// cluster ignores a desired app older than the last one it took, so a
	// superseded deployment can never win late.
	Revision   int64             `json:"revision"`
	Image      string            `json:"image"`
	Port       int               `json:"port"`
	HealthPath string            `json:"health_path"`
	Replicas   int               `json:"replicas"`
	Env        map[string]string `json:"env"`
	// Labels identify the app's objects (project, environment, release,
	// deployment).
	Labels map[string]string `json:"labels"`
	// Access is who may open the app: "public", "team" or "organization"
	// ("" = public, only for settings that predate 1e). Desired leaves it
	// empty; the settings of the deployment fill it (WithSettings).
	Access string `json:"access"`
	// Secrets are the app's secret variables (references; WithSettings).
	Secrets []DesiredSecret `json:"secrets"`
	// Resources are the databases, caches and volumes the app is connected
	// to (WithSettings): the cluster wires their variables into the app.
	Resources []DesiredResource `json:"resources"`
	// AI, when set (WithSettings), tells the agent to wire the app to the AI
	// gateway; its key is the managed secret AIKeySecret, listed in Secrets.
	AI *DesiredAI `json:"ai,omitempty"`
	// Services are the version's services, in order (Release.Services). The
	// top-level Image, Port, HealthPath and Replicas are the first service's
	// for an agent that runs one workload per app (without odicea.services).
	Services []Service `json:"services"`
}

// Phase is how the cluster sees an app.
type Phase string

const (
	PhaseProgressing Phase = "progressing" // rolling out, not yet answering its health check
	PhaseReady       Phase = "ready"       // every replica answers its health check
	PhaseFailed      Phase = "failed"      // it cannot start or its health check keeps failing
)

// FailureReason explains a failed phase.
type FailureReason string

const (
	FailureUnhealthy FailureReason = "unhealthy"  // it runs but its health check fails (also the default)
	FailureImagePull FailureReason = "image_pull" // the image cannot be downloaded
	FailureCrash     FailureReason = "crash"      // it keeps exiting on start
	FailureConfig    FailureReason = "config"     // the cluster cannot apply it (e.g. its host is taken)
	FailureResource  FailureReason = "resource"   // a database, cache or volume it uses was not ready in time
)

// AppStatus is what the cluster reports of an app.
type AppStatus struct {
	// ObservedRevision is the desired revision the status describes.
	ObservedRevision int64         `json:"observed_revision"`
	Phase            Phase         `json:"phase"`
	Reason           FailureReason `json:"reason"`
	Message          string        `json:"message"`
	ReadyReplicas    int           `json:"ready_replicas"`
	URL              string        `json:"url"`
}

// Desired builds what the cluster must run for deployment d of release r.
func Desired(name string, d Deployment, r Release) DesiredApp {
	env := make(map[string]string, len(r.Config.Env))
	for k, v := range r.Config.Env {
		env[k] = v
	}
	return DesiredApp{
		Name: name, Revision: d.Revision, Image: r.Image, Port: r.Config.Port, HealthPath: r.Config.HealthPath,
		Replicas: r.Config.Replicas, Env: env, Services: slices.Clone(r.Services),
		Labels: map[string]string{
			"odyssey.dev/project":     d.Project.String(),
			"odyssey.dev/environment": d.Environment.String(),
			"odyssey.dev/release":     r.ID.String(),
			"odyssey.dev/deployment":  d.ID.String(),
		},
	}
}
