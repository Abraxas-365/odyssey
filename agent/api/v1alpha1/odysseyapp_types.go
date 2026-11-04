package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OdysseyAppSpec is what the cluster must run for one app in one environment.
// It mirrors the control plane's release.DesiredApp (field for field).
type OdysseyAppSpec struct {
	// Revision increases with every deployment of the environment. Whoever
	// applies the spec never lowers it, so a superseded deployment cannot win
	// late; status.observedRevision says which revision the status describes.
	// +kubebuilder:validation:Minimum=1
	Revision int64 `json:"revision"`
	// Image is pinned by digest: registry/repository@sha256:<64 hex>.
	// +kubebuilder:validation:Pattern=`^[^\s@]+@sha256:[a-f0-9]{64}$`
	Image string `json:"image"`
	// Port the app listens on (PORT is set to it unless env says otherwise).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// HealthPath must answer 2xx/3xx once the app is ready to serve.
	// +kubebuilder:validation:Pattern=`^/`
	HealthPath string `json:"healthPath"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	Replicas int32 `json:"replicas"`
	// Env holds non-secret configuration.
	// +optional
	Env map[string]string `json:"env,omitempty"`
	// Labels identify the app's objects (project, environment, release,
	// deployment); they are copied to everything the agent creates.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Access says who may open the app. Absent means public (an object
	// written before access existed); the agent never exposes a protected
	// app in a cluster that cannot protect it.
	// +optional
	Access *AccessSpec `json:"access,omitempty"`
}

// AccessMode is who may open an app.
// +kubebuilder:validation:Enum=public;team;organization
type AccessMode string

const (
	AccessPublic       AccessMode = "public"       // anyone with the link
	AccessTeam         AccessMode = "team"         // users of the app's organization
	AccessOrganization AccessMode = "organization" // any user of the identity environment
)

// AccessSpec is the app's exposure policy (the sign-in gate, agent/access).
type AccessSpec struct {
	Mode AccessMode `json:"mode"`
	// Organization is the app's organization id: what the team mode
	// compares the user's organization with.
	// +optional
	Organization string `json:"organization,omitempty"`
}

// Phase is how the cluster sees the app at status.observedRevision.
// +kubebuilder:validation:Enum=progressing;ready;failed
type Phase string

const (
	PhaseProgressing Phase = "progressing"
	PhaseReady       Phase = "ready"
	PhaseFailed      Phase = "failed"
)

// Reason explains a failed phase.
// +kubebuilder:validation:Enum="";image_pull;crash;unhealthy;config
type Reason string

const (
	ReasonNone      Reason = ""
	ReasonImagePull Reason = "image_pull" // the image cannot be pulled
	ReasonCrash     Reason = "crash"      // the app keeps exiting
	ReasonUnhealthy Reason = "unhealthy"  // it runs but its health check fails
	ReasonConfig    Reason = "config"     // the spec cannot be applied (e.g. its host is taken)
)

// OdysseyAppStatus is what the agent observed.
type OdysseyAppStatus struct {
	// ObservedRevision is the spec revision this status describes.
	// +optional
	ObservedRevision int64 `json:"observedRevision,omitempty"`
	// ObservedGeneration is the metadata.generation of the spec this status
	// describes. A spec change at the same revision (e.g. the access mode)
	// bumps the generation: the status is stale until the agent re-observes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Reason Reason `json:"reason,omitempty"`
	// Message is the cluster's detail for people (in English, as Kubernetes says it).
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// URL is where the app answers.
	// +optional
	URL string `json:"url,omitempty"`
}

// OdysseyApp is one app in one environment: the agent turns it into a
// Deployment, a Service and an Ingress in the same namespace.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=oapp,categories=odyssey
// +kubebuilder:printcolumn:name="Revision",type=integer,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Observed",type=integer,JSONPath=`.status.observedRevision`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OdysseyApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdysseyAppSpec   `json:"spec,omitempty"`
	Status OdysseyAppStatus `json:"status,omitempty"`
}

// OdysseyAppList is a list of OdysseyApp.
//
// +kubebuilder:object:root=true
type OdysseyAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdysseyApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdysseyApp{}, &OdysseyAppList{})
}
