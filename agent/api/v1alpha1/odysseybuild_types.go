package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OdysseyBuildSpec is one build the control plane asked for. It mirrors the
// fleet protocol's Build (field for field) and never changes once created.
type OdysseyBuildSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9a-f-]{36}$`
	Organization string `json:"organization"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f-]{36}$`
	Project string `json:"project"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f-]{36}$`
	Environment string      `json:"environment"`
	Source      BuildSource `json:"source"`
	// ImageRepository is where the image is pushed (registry/repository,
	// no tag); the build's id is the tag.
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._:/-]*$`
	// +kubebuilder:validation:MaxLength=200
	ImageRepository string `json:"imageRepository"`
	// CacheRef is the registry reference of the project's build cache.
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._:/-]*$`
	// +kubebuilder:validation:MaxLength=220
	CacheRef string `json:"cacheRef"`
	// TimeoutSeconds bounds the build's Job (activeDeadlineSeconds).
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=7200
	TimeoutSeconds int32 `json:"timeoutSeconds"`
	// ReuseImage is the image of an earlier build of the same code under
	// the same builder (<imageRepository>@sha256:<digest>): when it is still
	// in the registry the build tags it and builds nothing.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=300
	ReuseImage string `json:"reuseImage,omitempty"`
	// SkipScan: the reused image's scan is recent and the control plane
	// copies it, so a reused image is not scanned again.
	// +optional
	SkipScan bool `json:"skipScan,omitempty"`
}

// BuildSource identifies the uploaded archive the build fetches through
// the agent (the control plane checks it again while sending it).
type BuildSource struct {
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	SHA256 string `json:"sha256"`
	// +kubebuilder:validation:Minimum=1
	Size int64 `json:"size"`
}

// BuildPhase is where a build is.
// +kubebuilder:validation:Enum=pending;running;succeeded;failed
type BuildPhase string

const (
	BuildPending   BuildPhase = "pending"
	BuildRunning   BuildPhase = "running"
	BuildSucceeded BuildPhase = "succeeded"
	BuildFailed    BuildPhase = "failed"
)

// Final reports whether the phase never changes again.
func (p BuildPhase) Final() bool { return p == BuildSucceeded || p == BuildFailed }

// BuildReason explains a failed build.
// +kubebuilder:validation:Enum="";unsupported;source;push;timeout;failed
type BuildReason string

const (
	BuildReasonNone        BuildReason = ""
	BuildReasonUnsupported BuildReason = "unsupported" // nothing says how to build or start the app
	BuildReasonSource      BuildReason = "source"      // the source could not be fetched or is not valid
	BuildReasonPush        BuildReason = "push"        // the image could not be pushed
	BuildReasonTimeout     BuildReason = "timeout"     // it ran past timeoutSeconds
	BuildReasonFailed      BuildReason = "failed"      // the build itself failed (see the log)
)

// OdysseyBuildStatus is what the agent observed of the build's Job. Once
// the phase is final it never changes (the Job may be gone by then).
type OdysseyBuildStatus struct {
	// +optional
	Phase BuildPhase `json:"phase,omitempty"`
	// ImageDigest of the pushed image (succeeded only).
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	ImageDigest string `json:"imageDigest,omitempty"`
	// +optional
	Reason BuildReason `json:"reason,omitempty"`
	// Message is the detail for people (the end of the failing step's output).
	// +optional
	Message string `json:"message,omitempty"`
	// FinishedAt is when the agent saw the phase become final; the build is
	// deleted a while after (agent/controllers/build Config.Retention).
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Reused: the build succeeded by reusing spec.reuseImage (nothing was
	// built; imageDigest is the reused image's).
	// +optional
	Reused bool `json:"reused,omitempty"`
}

// OdysseyBuild is one build of an app from uploaded source: the agent runs
// it as a Job in its own namespace (odyssey-builds) and pushes the image to
// the cluster's registry.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=obuild,categories=odyssey
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.imageRepository`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OdysseyBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a build's spec never changes"
	Spec   OdysseyBuildSpec   `json:"spec,omitempty"`
	Status OdysseyBuildStatus `json:"status,omitempty"`
}

// OdysseyBuildList is a list of OdysseyBuild.
//
// +kubebuilder:object:root=true
type OdysseyBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdysseyBuild `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdysseyBuild{}, &OdysseyBuildList{})
}
