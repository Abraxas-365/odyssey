package resource

import (
	"context"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// engine is how the agent prepares one kind of resource: the only place the
// controller tells kinds apart (an architecture test holds it to that), so
// a new kind is one entry here and never a half-handled case elsewhere.
type engine struct {
	// apply makes or updates what provides the resource and says how it is.
	apply func(r *Reconciler, ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error)
	// remove deletes, through del, everything apply made, and says whether
	// something is still on its way out beyond what del reported.
	remove func(r *Reconciler, ctx context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (pending bool, err error)
	// secret and keys are where an app reads a resource of the kind from
	// (Connection; none for a kind the app mounts instead).
	secret func(resource string) string
	keys   []string
}

var engines = map[v1alpha1.ResourceKind]engine{
	v1alpha1.ResourcePostgres: {
		apply:  (*Reconciler).postgres,
		remove: (*Reconciler).removePostgres,
		secret: PostgresSecret,
		keys:   []string{"uri", "host", "port", "dbname", "user", "password"},
	},
	v1alpha1.ResourceRedis: {
		apply:  (*Reconciler).redis,
		remove: (*Reconciler).removeRedis,
		secret: RedisName,
		keys:   []string{RedisKeyURL},
	},
	v1alpha1.ResourceFiles: {
		apply:  (*Reconciler).files,
		remove: (*Reconciler).removeFiles,
	},
}
