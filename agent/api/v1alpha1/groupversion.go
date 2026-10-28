// Package v1alpha1 holds Odyssey's custom resources: the desired state the
// control plane sends to a cluster and the agent reconciles.
//
// +kubebuilder:object:generate=true
// +groupName=odyssey.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the API group and version of Odyssey's resources.
var GroupVersion = schema.GroupVersion{Group: "odyssey.dev", Version: "v1alpha1"}

var (
	// SchemeBuilder registers the types with a runtime.Scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	// AddToScheme adds the types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
