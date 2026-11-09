// Package resourcekind is the contract of an environment's resources (2b)
// both planes keep: which kinds exist, how a name and a size are written,
// and which variables an app reads a resource by. The control plane uses it
// to validate and to show what an app gets; the agent to wire it. Keeping
// one copy is what makes the console say exactly what the pod receives.
//
// A kind is a contract (Contract), registered here and nowhere else: what
// the app receives (variables, a mount), which of those are credentials and
// whether the resource holds data. How a contract is fulfilled (a recipe on
// an engine) is the catalog's business; the app and its assistant only ever
// name kinds. Code that needs something about a kind reads it from the
// registry (Lookup) instead of switching on the kind.
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

// Kind is what a resource is: the name of a contract.
type Kind string

const (
	// Postgres is a PostgreSQL database.
	Postgres Kind = "postgres"
	// Redis is a cache.
	Redis Kind = "redis"
	// Files is a volume the app mounts.
	Files Kind = "files"
	// ObjectStorage is an S3-compatible bucket. No built-in engine prepares
	// it: only a bucket the company already has fulfils it.
	ObjectStorage Kind = "object-storage"
)

// Variable is one environment variable a contract gives the app.
type Variable struct {
	// Name, without the connection's prefix.
	Name string
	// Secret: a credential (its value lives only in the cluster). A resource
	// the company already has gives it only as a reference to a secret store,
	// never as a value written in Odicea.
	Secret bool
	// Optional: a resource may leave it out (the app then does not get it):
	// a bucket read with the pod's own identity has no keys.
	Optional bool
	// Derived: the agent builds it from the other variables (a connection
	// URL from its pieces, escaped); nobody writes it.
	Derived bool
	// Format is what a value written for it must look like (FormatHost…;
	// "" any short text).
	Format string
	// Choices are the only values it takes (none: any value of its format).
	Choices []string
	// Together names a set of optional variables a resource gives all of
	// or none of (a key and its secret): "" for none.
	Together string
	// Meaning in plain Spanish, for the catalog.
	Meaning string
}

// Formats of a variable's value.
const (
	// FormatHost: a DNS name or IP address outside the cluster.
	FormatHost = "host"
	// FormatPort: a TCP port.
	FormatPort = "port"
	// FormatURL: an http(s) URL whose host is a FormatHost.
	FormatURL = "url"
)

// Contract is what a kind promises the app, whatever recipe fulfils it.
type Contract struct {
	Kind Kind
	// Noun and Article word the kind in Spanish ("la" "base de datos").
	Noun    string
	Article string
	// Variables, in the order the agent wires them.
	Variables []Variable
	// Mounted: the resource is a volume mounted at MountPath(name), whose
	// path is the first variable. A mounted resource is written by one
	// service of an app (its volume is ReadWriteOnce).
	Mounted bool
	// HoldsData: deleting it or fulfilling it with another recipe loses data.
	HoldsData bool
	// Builtin: the agent's built-in engine prepares it (a cluster lists it in
	// its resource kinds when it can).
	Builtin bool
	// Existing: a resource the company already has can fulfil it (the
	// existing engine writes its connection). A mounted kind cannot.
	Existing bool
	// Unavailable is what to tell someone whose cluster cannot provision it
	// with its built-in engine.
	Unavailable string
	// Settings names the built-in engine's settings block the kind takes
	// (pkg/resourcespec: "postgres"); "" takes none.
	Settings string
}

// The is the kind with its article: "la base de datos".
func (c Contract) The() string { return c.Article + " " + c.Noun }

// registry is every contract, in the order the console lists them.
var registry = []Contract{
	{
		Kind: Postgres, Noun: "base de datos", Article: "la", HoldsData: true, Builtin: true, Existing: true,
		Variables: []Variable{
			{Name: "DATABASE_URL", Secret: true, Derived: true, Meaning: "la dirección completa de conexión, con usuario y contraseña"},
			{Name: "PGHOST", Format: FormatHost, Meaning: "el servidor"},
			{Name: "PGPORT", Format: FormatPort, Meaning: "el puerto"},
			{Name: "PGDATABASE", Meaning: "el nombre de la base"},
			{Name: "PGUSER", Meaning: "el usuario"},
			{Name: "PGPASSWORD", Secret: true, Meaning: "la contraseña"},
			{Name: "PGSSLMODE", Optional: true, Choices: []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"},
				Meaning: "cómo se cifra la conexión (vacía: lo que decida el cliente)"},
		},
		Unavailable: "Este clúster no tiene bases de datos administradas. Pide a TI que instale CloudNativePG.",
		Settings:    "postgres",
	},
	{
		Kind: Redis, Noun: "caché", Article: "la", Builtin: true, Existing: true,
		Variables: []Variable{
			{Name: "REDIS_URL", Secret: true, Meaning: "la dirección de conexión, con contraseña"},
		},
		Unavailable: "Este clúster no puede crear cachés. Pide a TI que actualice su agente y configure una clase de almacenamiento.",
	},
	{
		Kind: Files, Noun: "espacio de archivos", Article: "el", Mounted: true, HoldsData: true, Builtin: true,
		Variables: []Variable{
			{Name: "FILES_DIR", Meaning: "la carpeta donde la app guarda sus archivos"},
		},
		Unavailable: "Este clúster no puede crear espacios de archivos. Pide a TI que configure una clase de almacenamiento por defecto.",
	},
	{
		Kind: ObjectStorage, Noun: "almacenamiento de objetos", Article: "el", HoldsData: true, Existing: true,
		Variables: []Variable{
			{Name: "S3_BUCKET", Meaning: "el nombre del bucket"},
			{Name: "S3_ENDPOINT", Optional: true, Format: FormatURL, Meaning: "la dirección del servicio (vacía: la de AWS)"},
			{Name: "S3_REGION", Meaning: "la región"},
			{Name: "AWS_ACCESS_KEY_ID", Secret: true, Optional: true, Together: "aws-key", Meaning: "la llave de acceso (vacía: la identidad de la app)"},
			{Name: "AWS_SECRET_ACCESS_KEY", Secret: true, Optional: true, Together: "aws-key", Meaning: "el secreto de la llave"},
		},
		Unavailable: "Odicea no crea almacenamiento de objetos: TI puede ofrecer uno que la empresa ya tenga.",
	},
}

// Kinds are every kind, in the order the console lists them. Whether one
// can be asked for depends on the organization's recipes (the catalog) and
// on the cluster (its engines), never on this list.
var Kinds = func() []Kind {
	out := make([]Kind, len(registry))
	for i, c := range registry {
		out[i] = c.Kind
	}
	return out
}()

// Contracts are every contract, in order.
func Contracts() []Contract { return slices.Clone(registry) }

// Lookup is k's contract (false: no such kind is declared).
func Lookup(k Kind) (Contract, bool) {
	i := slices.IndexFunc(registry, func(c Contract) bool { return c.Kind == k })
	if i < 0 {
		return Contract{}, false
	}
	return registry[i], true
}

// Of is k's contract; an unknown kind gets a generic one (nouns only), so
// wording code never has to handle a missing kind.
func Of(k Kind) Contract {
	if c, ok := Lookup(k); ok {
		return c
	}
	return Contract{Kind: k, Noun: "recurso", Article: "el", Unavailable: "Este clúster no puede crear este tipo de recurso. Pide a TI que lo revise."}
}

// Variable is the contract's variable named name (false: it has none).
func (c Contract) Variable(name string) (Variable, bool) {
	i := slices.IndexFunc(c.Variables, func(v Variable) bool { return v.Name == name })
	if i < 0 {
		return Variable{}, false
	}
	return c.Variables[i], true
}

// Valid says whether k is a declared kind.
func (k Kind) Valid() bool {
	_, ok := Lookup(k)
	return ok
}

// KindsLabel lists the kinds for an error message: "postgres, redis, files
// or object-storage".
func KindsLabel() string {
	names := make([]string, len(Kinds))
	for i, k := range Kinds {
		names[i] = string(k)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// MaxName bounds a resource's name: it prefixes the cluster objects made for
// it (<name>-rw, <name>-app, …), which must stay DNS labels.
const MaxName = 30

// CodeUnavailable is the error code of a kind the environment's cluster
// cannot provision at all (its Contract.Unavailable is the message). The
// fleet refuses with it when it is asked to run one; the catalog, before
// anything is created.
const CodeUnavailable = "RESOURCES_UNAVAILABLE"

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
// These are the wire bounds both planes accept; a recipe's offering narrows
// them.
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
// k by, with prefix (and "_") in front, in the contract's order.
func Variables(k Kind, prefix string) []string {
	c, _ := Lookup(k)
	out := make([]string, len(c.Variables))
	for i, v := range c.Variables {
		out[i] = v.Name
		if prefix != "" {
			out[i] = prefix + "_" + v.Name
		}
	}
	return out
}

// MountPath is where a mounted resource named name is mounted in the app.
func MountPath(name string) string { return "/data/" + name }

// Wait is how long the agent lets a revision wait for its resources (a first
// PostgreSQL takes minutes) before it reports the revision failed. The
// control plane's own wait for a deployment (its backstop when the agent
// says nothing) is longer than this, so the agent's plain reason always
// arrives first.
const Wait = 10 * time.Minute
