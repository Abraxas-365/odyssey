package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceKind is what an OdysseyResource provisions (the same names as
// pkg/resourcekind: the control plane and the agent share them).
// +kubebuilder:validation:Enum=postgres;redis;files
type ResourceKind string

const (
	ResourcePostgres ResourceKind = "postgres" // a CloudNativePG Cluster, one instance
	ResourceRedis    ResourceKind = "redis"    // a Redis Deployment with a password and a volume
	ResourceFiles    ResourceKind = "files"    // a PersistentVolumeClaim the app mounts
)

// OdysseyResourceSpec is the desired state of one resource of an environment.
// The CR is named after the resource and lives in the environment's namespace.
type OdysseyResourceSpec struct {
	Kind ResourceKind `json:"kind"`
	// Generation grows with every change of the desired state. Whoever
	// applies the spec never lowers it, so an older message cannot win late;
	// status.observedGeneration says which generation the status describes.
	// +kubebuilder:validation:Minimum=1
	Generation int64 `json:"generation"`
	// Storage is the volume's size ("5Gi"). It may grow, never shrink: a
	// smaller value is ignored.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*Gi$`
	Storage string `json:"storage"`
	// Deleted asks for the resource and its data to be removed. The agent
	// reports phase deleted and then removes this object.
	// +optional
	Deleted bool `json:"deleted,omitempty"`
}

// ResourcePhase is how the cluster sees the resource.
// +kubebuilder:validation:Enum=provisioning;ready;failed;deleted
type ResourcePhase string

const (
	ResourceProvisioning ResourcePhase = "provisioning"
	ResourceReady        ResourcePhase = "ready"
	ResourceFailed       ResourcePhase = "failed"
	// ResourceDeleted: everything the resource made is gone (the object is
	// removed right after the control plane has been told).
	ResourceDeleted ResourcePhase = "deleted"
)

// OdysseyResourceStatus is what the agent observed.
type OdysseyResourceStatus struct {
	// ObservedGeneration is the spec.generation this status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase ResourcePhase `json:"phase,omitempty"`
	// Message says why it is not ready (in English, short).
	// +optional
	Message string `json:"message,omitempty"`
}

// OdysseyResource is one resource (database, cache, files volume) of one
// environment: the agent turns it into the objects that provide it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ores,categories=odyssey
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Generation",type=integer,JSONPath=`.spec.generation`
// +kubebuilder:printcolumn:name="Observed",type=integer,JSONPath=`.status.observedGeneration`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=`.spec.storage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OdysseyResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdysseyResourceSpec   `json:"spec,omitempty"`
	Status OdysseyResourceStatus `json:"status,omitempty"`
}

// OdysseyResourceList is a list of OdysseyResource.
//
// +kubebuilder:object:root=true
type OdysseyResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdysseyResource `json:"items"`
}

// ResourceRefSpec is a resource of the environment an app uses.
type ResourceRefSpec struct {
	// Name of the OdysseyResource in the app's namespace.
	// +kubebuilder:validation:MaxLength=30
	Name string       `json:"name"`
	Kind ResourceKind `json:"kind"`
	// Prefix goes before the variables the app reads the resource by.
	// +optional
	// +kubebuilder:validation:MaxLength=40
	Prefix string `json:"prefix,omitempty"`
}

func init() {
	SchemeBuilder.Register(&OdysseyResource{}, &OdysseyResourceList{})
}
