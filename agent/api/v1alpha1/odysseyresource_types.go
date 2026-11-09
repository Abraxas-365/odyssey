package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceKind is what an OdysseyResource provisions (the same names as
// pkg/resourcekind: the control plane and the agent share them).
// +kubebuilder:validation:Enum=postgres;redis;files;object-storage
type ResourceKind string

const (
	ResourcePostgres      ResourceKind = "postgres"       // a CloudNativePG Cluster (spec.postgres), or the company's own
	ResourceRedis         ResourceKind = "redis"          // a Redis Deployment with a password and a volume, or the company's own
	ResourceFiles         ResourceKind = "files"          // a PersistentVolumeClaim the app mounts
	ResourceObjectStorage ResourceKind = "object-storage" // only ever the company's own (spec.existing)
)

// OdysseyResourceSpec is the desired state of one resource of an environment.
// The CR is named after the resource and lives in the environment's namespace.
// +kubebuilder:validation:XValidation:rule="!(has(self.postgres) && has(self.existing))",message="a resource is made by Odicea or is the company's, not both"
// +kubebuilder:validation:XValidation:rule="has(self.existing) != has(self.storage)",message="a resource Odicea makes has a storage size; one the company already has has none"
// +kubebuilder:validation:XValidation:rule="has(self.existing) == has(oldSelf.existing)",message="a resource cannot change between made by Odicea and the company's: delete it and create another"
type OdysseyResourceSpec struct {
	Kind ResourceKind `json:"kind"`
	// Generation grows with every change of the desired state. Whoever
	// applies the spec never lowers it, so an older message cannot win late;
	// status.observedGeneration says which generation the status describes.
	// +kubebuilder:validation:Minimum=1
	Generation int64 `json:"generation"`
	// Storage is the volume's size ("5Gi"). It may grow, never shrink: a
	// smaller value is ignored. Empty only for a resource the company
	// already has (Existing), which has no volume.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*Gi$`
	// +optional
	Storage string `json:"storage,omitempty"`
	// Deleted asks for the resource and its data to be removed. The agent
	// reports phase deleted and then removes this object.
	// +optional
	Deleted bool `json:"deleted,omitempty"`
	// Recipe is the catalog recipe@version that resolved it
	// ("postgres-produccion@1"), for people reading the cluster; the agent
	// acts on the settings below only.
	// +kubebuilder:validation:MaxLength=80
	// +optional
	Recipe string `json:"recipe,omitempty"`
	// Postgres are the database's settings (kind postgres; none: one
	// instance with the engine's defaults).
	// +optional
	Postgres *PostgresSettings `json:"postgres,omitempty"`
	// Existing is the connection of a resource the company already has:
	// the agent only writes its connection Secret (through the External
	// Secrets Operator) and never makes, changes or deletes the resource.
	// +optional
	Existing *ExistingConnection `json:"existing,omitempty"`
}

// ExistingConnection is where each variable of the kind's contract comes
// from (pkg/resourcespec.Existing: the agent validates it again).
type ExistingConnection struct {
	// +kubebuilder:validation:MaxItems=16
	Values []ExistingValue `json:"values"`
}

// ExistingValue is one variable: a value written in the recipe, or a
// reference to a secret store (a credential is only ever a reference).
type ExistingValue struct {
	// +kubebuilder:validation:MaxLength=64
	Variable string `json:"variable"`
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	Ref *ExistingRef `json:"ref,omitempty"`
}

// ExistingRef locates a value in a ClusterSecretStore.
type ExistingRef struct {
	// +kubebuilder:validation:MaxLength=253
	Store string `json:"store"`
	// +kubebuilder:validation:MaxLength=255
	Key string `json:"key"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Property string `json:"property,omitempty"`
}

// PostgresSettings are how a CloudNativePG database runs. A zero field is
// the engine's default.
type PostgresSettings struct {
	// Instances: 1 (no replica) to 5. They follow every change.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=5
	// +optional
	Instances int32 `json:"instances,omitempty"`
	// Major is the PostgreSQL major version. It is fixed when the database
	// is made: a different one later fails the resource (a major upgrade is
	// a migration, not a setting).
	// +kubebuilder:validation:Enum=0;16;17;18
	// +optional
	Major int32 `json:"major,omitempty"`
	// CPU and Memory are Kubernetes quantities, each set as request and
	// limit of every instance.
	// +kubebuilder:validation:MaxLength=16
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +kubebuilder:validation:MaxLength=16
	// +optional
	Memory string `json:"memory,omitempty"`
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
// +kubebuilder:printcolumn:name="Recipe",type=string,JSONPath=`.spec.recipe`,priority=1
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
	// Services are the app's services wired to it; empty: every one (a files
	// volume, ReadWriteOnce: only the first).
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:Pattern=`^[a-z][a-z0-9]{0,19}$`
	Services []string `json:"services,omitempty"`
}

func init() {
	SchemeBuilder.Register(&OdysseyResource{}, &OdysseyResourceList{})
}
