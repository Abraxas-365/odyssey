package resource

import (
	"context"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// kind is what the controller knows of one kind of resource: the only place
// it tells kinds apart (an architecture test holds it to that), so a new
// kind is one entry here and never a half-handled case elsewhere.
//
// Every engine that prepares a kind writes the same connection: the Secret
// secret(name) with keys, one per variable of the kind's contract
// (resourcekind.Variables, same order). The app controller reads it by
// Connection and never knows which engine made it.
type kind struct {
	// secret and keys are the connection (none for a kind the app mounts
	// instead). A key may be missing from the Secret when its variable is
	// optional and the resource does not give it.
	secret func(resource string) string
	keys   []string
	// derived are how the existing engine builds each derived variable of
	// the contract from the others (by variable name).
	derived map[string][]part
	// builtin is the agent's own engine for the kind (nil: none; a resource
	// of the kind is only ever one the company already has).
	builtin *engine
}

// engine prepares a resource.
type engine struct {
	// apply makes or updates what provides the resource and says how it is.
	apply func(r *Reconciler, ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error)
	// remove deletes, through del, everything apply made, and says whether
	// something is still on its way out beyond what del reported.
	remove func(r *Reconciler, ctx context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (pending bool, err error)
}

var kinds = map[v1alpha1.ResourceKind]kind{
	v1alpha1.ResourcePostgres: {
		secret: PostgresSecret,
		keys:   []string{"uri", "host", "port", "dbname", "user", "password", "sslmode"},
		// postgresql://user:password@host:port/database[?sslmode=mode]: the
		// user, password, database and mode percent-encoded, an IPv6 host
		// in brackets.
		derived: map[string][]part{"DATABASE_URL": {
			{text: "postgresql://"}, {variable: "PGUSER", escape: true}, {text: ":"}, {variable: "PGPASSWORD", escape: true},
			{text: "@"}, {variable: "PGHOST", host: true}, {text: ":"}, {variable: "PGPORT", escape: true},
			{text: "/"}, {variable: "PGDATABASE", escape: true},
			{text: "?sslmode=", when: "PGSSLMODE"}, {variable: "PGSSLMODE", escape: true, when: "PGSSLMODE"},
		}},
		builtin: &engine{apply: (*Reconciler).postgres, remove: (*Reconciler).removePostgres},
	},
	v1alpha1.ResourceRedis: {
		secret:  RedisName,
		keys:    []string{RedisKeyURL},
		builtin: &engine{apply: (*Reconciler).redis, remove: (*Reconciler).removeRedis},
	},
	v1alpha1.ResourceFiles: {
		builtin: &engine{apply: (*Reconciler).files, remove: (*Reconciler).removeFiles},
	},
	v1alpha1.ResourceObjectStorage: {
		secret: ObjectStorageSecret,
		keys:   []string{"bucket", "endpoint", "region", "access-key-id", "secret-access-key"},
	},
}

// existingEngine writes the connection of a resource the company already
// has, whatever its kind (existing.go).
var existingEngine = engine{apply: (*Reconciler).existing, remove: (*Reconciler).removeExisting}

// engineOf is the engine that prepares o: the existing one for a resource
// the company already has, otherwise its kind's own (false: none).
func engineOf(o *v1alpha1.OdysseyResource) (engine, bool) {
	if o.Spec.Existing != nil {
		return existingEngine, true
	}
	k, ok := kinds[o.Spec.Kind]
	if !ok || k.builtin == nil {
		return engine{}, false
	}
	return *k.builtin, true
}
