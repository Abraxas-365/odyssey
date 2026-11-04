// Package resourcekind is the contract of an environment's resources (2b)
// both planes keep: which kinds exist, how a name and a size are written,
// and which variables an app reads a resource by. The control plane uses it
// to validate and to show what an app gets; the agent to wire it. Keeping
// one copy is what makes the console say exactly what the pod receives.
package resourcekind

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kind is what a resource is.
type Kind string

const (
	// Postgres is a PostgreSQL database (CloudNativePG Cluster, one instance).
	Postgres Kind = "postgres"
	// Redis is a cache (a Redis Deployment with a password and a volume).
	Redis Kind = "redis"
	// Files is a volume the app mounts (a PersistentVolumeClaim).
	Files Kind = "files"
)

// Kinds are every kind, in the order the console lists them.
var Kinds = []Kind{Postgres, Redis, Files}

// Valid says whether k is a known kind.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// MaxName bounds a resource's name: it prefixes the cluster objects made for
// it (<name>-rw, <name>-app, …), which must stay DNS labels.
const MaxName = 30

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// CheckName says why name cannot name a resource (nil: it can).
func CheckName(name string) error {
	switch {
	case name == "":
		return errors.New("a resource needs a name")
	case len(name) > MaxName:
		return fmt.Errorf("a resource's name has at most %d characters", MaxName)
	case !nameRE.MatchString(name):
		return errors.New("a resource's name uses lowercase letters, digits and dashes, and starts with a letter")
	}
	return nil
}

var prefixRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// MaxPrefix bounds a variable prefix.
const MaxPrefix = 40

// CheckPrefix says why prefix cannot prefix a resource's variables ("" is
// no prefix and always fine).
func CheckPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxPrefix || !prefixRE.MatchString(prefix) || strings.HasSuffix(prefix, "_") {
		return fmt.Errorf("a prefix is an uppercase name of at most %d characters (letters, digits, _), like ANALYTICS", MaxPrefix)
	}
	if strings.HasPrefix(prefix, "ODICEA") {
		return errors.New("the ODICEA prefix is reserved for the platform")
	}
	return nil
}

// Storage sizes, in GiB: a volume is a whole number of GiB between these.
const (
	MinStorageGi     = 1
	MaxStorageGi     = 100
	DefaultStorageGi = 1
)

// Storage is a size as Kubernetes writes it ("5Gi").
func Storage(gi int) string { return strconv.Itoa(gi) + "Gi" }

// ParseStorage reads a size written by Storage.
func ParseStorage(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(s, "Gi"))
	if err != nil || !strings.HasSuffix(s, "Gi") || n < MinStorageGi || n > MaxStorageGi {
		return 0, fmt.Errorf("a size is between %dGi and %dGi", MinStorageGi, MaxStorageGi)
	}
	return n, nil
}

// Variables are the environment variables an app reads a resource of kind
// k by, with prefix (and "_") in front.
func Variables(k Kind, prefix string) []string {
	var names []string
	switch k {
	case Postgres:
		names = []string{"DATABASE_URL", "PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"}
	case Redis:
		names = []string{"REDIS_URL"}
	case Files:
		names = []string{"FILES_DIR"}
	}
	if prefix == "" {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + "_" + n
	}
	return out
}

// MountPath is where a files resource named name is mounted in the app.
func MountPath(name string) string { return "/data/" + name }

// Wait is how long the agent lets a revision wait for its resources (a first
// PostgreSQL takes minutes) before it reports the revision failed. The
// control plane's own wait for a deployment (its backstop when the agent
// says nothing) is longer than this, so the agent's plain reason always
// arrives first.
const Wait = 10 * time.Minute
