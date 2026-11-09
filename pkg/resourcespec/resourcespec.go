// Package resourcespec is what both planes agree a resource is prepared
// with, beyond its kind and size: the engine settings a recipe fixes (a
// resolved, typed spec, never the recipe itself) and the capabilities an
// agent announces so the control plane only sends what it can honour.
//
// An agent that does not announce SpecV1 ignores everything here, so it is
// only ever sent resources whose spec is empty (the basic recipes, exactly
// what it prepared before the catalog existed).
package resourcespec

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// SchemaVersion is the version of Spec the control plane writes.
const SchemaVersion = 1

// Capabilities an agent announces (besides Builtin(kind)).
const (
	// SpecV1: the agent applies a Spec of SchemaVersion 1.
	SpecV1 = "spec/v1"
	// PostgresInstances: it runs a database with replicas.
	PostgresInstances = "builtin:postgres:instances"
	// PostgresVersion: it pins the PostgreSQL major version.
	PostgresVersion = "builtin:postgres:version"
	// PostgresResources: it sets the database's CPU and memory.
	PostgresResources = "builtin:postgres:resources"
	// ConnectExisting: it writes the connection of a resource the company
	// already has (EngineExisting).
	ConnectExisting = "existing"
)

// Builtin is the capability of preparing kind with the built-in engine.
func Builtin(k resourcekind.Kind) string { return "builtin:" + string(k) }

// Engines.
const (
	// EngineBuiltin: the agent prepares the resource with its own operators.
	EngineBuiltin = "builtin"
	// EngineExisting: the resource already exists in the company; the agent
	// only writes its connection (docs/catalog.md).
	EngineExisting = "existing"
)

// Spec is how a resource is prepared beyond its kind and size. The zero
// value is the basic resource every agent prepares. At most one block is
// set: the built-in engine's settings of its kind, or Existing.
type Spec struct {
	// Postgres settings (nil: the basic database).
	Postgres *Postgres `json:"postgres,omitempty"`
	// Existing is the connection of a resource the company already has
	// (EngineExisting): the agent writes it, and nothing else.
	Existing *Existing `json:"existing,omitempty"`
}

// Existing is where each variable of a resource the company already has
// comes from: a value written in the recipe, or a reference to a secret
// store (External Secrets). A secret variable is only ever a reference,
// so a credential never passes through Odicea. A derived variable
// (DATABASE_URL) is not listed: the agent builds it from the others.
type Existing struct {
	Values []Value `json:"values"`
}

// Value is one variable of an Existing connection: Value or Ref.
type Value struct {
	Variable string `json:"variable"`
	Value    string `json:"value,omitempty"`
	Ref      *Ref   `json:"ref,omitempty"`
}

// Ref is a value read from a ClusterSecretStore.
type Ref struct {
	// Store is the ClusterSecretStore's name.
	Store string `json:"store"`
	// Key is the secret's key in the store ("rds/reportes").
	Key string `json:"key"`
	// Property is the field of that secret ("" the whole value).
	Property string `json:"property,omitempty"`
}

// Bounds of an Existing connection.
const (
	MaxValue    = 255
	MaxStore    = 253
	MaxKey      = 255
	MaxProperty = 128
)

// Stores are the ClusterSecretStores e reads from, sorted, once each.
func (e *Existing) Stores() []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, v := range e.Values {
		if v.Ref != nil {
			out = append(out, v.Ref.Store)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// SecretStore is the capability of reading from the ClusterSecretStore
// named store. The agent does not announce it: the control plane derives
// it from the stores the cluster reports.
func SecretStore(store string) string { return "secretstore:" + store }

// ExamplePostgres is a valid Existing connection of a database (tests and
// docs): host, port and database written, user and password from store.
func ExamplePostgres(store string) Spec {
	return Spec{Existing: &Existing{Values: []Value{
		{Variable: "PGHOST", Value: "reportes.abc123.us-east-1.rds.amazonaws.com"},
		{Variable: "PGPORT", Value: "5432"},
		{Variable: "PGDATABASE", Value: "reportes"},
		{Variable: "PGUSER", Ref: &Ref{Store: store, Key: "rds/reportes", Property: "username"}},
		{Variable: "PGPASSWORD", Ref: &Ref{Store: store, Key: "rds/reportes", Property: "password"}},
	}}}
}

// Postgres are the settings of a built-in PostgreSQL database. A zero field
// is the engine's own default (and needs no capability).
type Postgres struct {
	// Instances is 1 (no replica) to MaxInstances.
	Instances int `json:"instances,omitempty"`
	// Major is the PostgreSQL major version, one of Majors.
	Major int `json:"major,omitempty"`
	// CPU and Memory are Kubernetes quantities ("500m", "1Gi"), each set
	// as both request and limit.
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// MaxInstances bounds a database's instances.
const MaxInstances = 5

// Majors are the PostgreSQL major versions an agent can pin.
var Majors = []int{16, 17, 18}

// IsZero says the spec asks for nothing beyond the basic resource.
func (s Spec) IsZero() bool {
	return s.Existing == nil && (s.Postgres == nil || *s.Postgres == Postgres{})
}

// Engine is the engine s is prepared by: EngineExisting with an Existing
// block, otherwise the kind's built-in one.
func (s Spec) Engine() string {
	if s.Existing != nil {
		return EngineExisting
	}
	return EngineBuiltin
}

// Requires are the capabilities an agent needs to honour s for kind,
// sorted. On the built-in engine: the kind's engine plus what the settings
// ask for. On the existing engine: writing existing connections, plus every
// secret store it reads (SecretStore); never the kind's built-in engine (a
// cluster that cannot make a database can still connect to the company's).
func (s Spec) Requires(k resourcekind.Kind) []string {
	if s.Existing != nil {
		out := []string{ConnectExisting}
		for _, store := range s.Existing.Stores() {
			out = append(out, SecretStore(store))
		}
		slices.Sort(out)
		return out
	}
	out := []string{Builtin(k)}
	if p := s.Postgres; p != nil && !s.IsZero() {
		out = append(out, SpecV1)
		if p.Instances != 0 {
			out = append(out, PostgresInstances)
		}
		if p.Major != 0 {
			out = append(out, PostgresVersion)
		}
		if p.CPU != "" || p.Memory != "" {
			out = append(out, PostgresResources)
		}
	}
	slices.Sort(out)
	return out
}

// Missing are the capabilities of required that announced lacks.
func Missing(required, announced []string) []string {
	var out []string
	for _, r := range required {
		if !slices.Contains(announced, r) {
			out = append(out, r)
		}
	}
	return out
}

// Validate checks s for kind before either plane acts on it.
func (s Spec) Validate(k resourcekind.Kind) error {
	if s.Existing != nil {
		if s.Postgres != nil {
			return fmt.Errorf("a resource the company already has takes no engine settings")
		}
		return s.Existing.validate(k)
	}
	p := s.Postgres
	if p == nil {
		return nil
	}
	if resourcekind.Of(k).Settings != "postgres" {
		return fmt.Errorf("a %s resource takes no postgres settings", k)
	}
	switch {
	case p.Instances < 0 || p.Instances > MaxInstances:
		return fmt.Errorf("a database has 1 to %d instances", MaxInstances)
	case p.Major != 0 && !slices.Contains(Majors, p.Major):
		return fmt.Errorf("postgres %d is not one of %v", p.Major, Majors)
	case !quantity(p.CPU) || !quantity(p.Memory):
		return fmt.Errorf("cpu and memory are Kubernetes quantities like 500m or 1Gi")
	}
	return nil
}

var (
	storeRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	keyRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+=-]*$`)
	propertyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hostRE     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
)

// validate: every variable of k's contract once (a derived one never, a
// required one always), a secret only by reference, a written value in its
// format, a reference with safe names.
func (e *Existing) validate(k resourcekind.Kind) error {
	c, known := resourcekind.Lookup(k)
	if !known || !c.Existing {
		return fmt.Errorf("a %s resource cannot be one the company already has", k)
	}
	seen := map[string]bool{}
	for _, v := range e.Values {
		cv, ok := c.Variable(v.Variable)
		switch {
		case !ok:
			return fmt.Errorf("%s has no variable %q", k, v.Variable)
		case cv.Derived:
			return fmt.Errorf("%s is built from the other variables; do not set it", v.Variable)
		case seen[v.Variable]:
			return fmt.Errorf("%s is set twice", v.Variable)
		case (v.Ref == nil) == (v.Value == ""):
			return fmt.Errorf("%s needs either a value or a reference to a secret store", v.Variable)
		case cv.Secret && v.Ref == nil:
			return fmt.Errorf("%s is a credential: read it from a secret store, never write it here", v.Variable)
		case len(cv.Choices) > 0 && v.Ref != nil:
			// a store could hold anything: only a written value is checked
			return fmt.Errorf("%s is one of %s: write it here, it is not read from a secret store", v.Variable, strings.Join(cv.Choices, ", "))
		}
		seen[v.Variable] = true
		if v.Ref != nil {
			if err := v.Ref.validate(v.Variable); err != nil {
				return err
			}
			continue
		}
		if err := checkValue(cv, v.Value); err != nil {
			return err
		}
	}
	for _, cv := range c.Variables {
		if !cv.Derived && !cv.Optional && !seen[cv.Name] {
			return fmt.Errorf("%s is missing", cv.Name)
		}
	}
	for _, cv := range c.Variables {
		if cv.Together == "" || seen[cv.Name] {
			continue
		}
		for _, other := range c.Variables {
			if other.Together == cv.Together && seen[other.Name] {
				return fmt.Errorf("%s goes with %s: set both or neither", cv.Name, other.Name)
			}
		}
	}
	return nil
}

func (r Ref) validate(variable string) error {
	switch {
	case r.Store == "" || len(r.Store) > MaxStore || !storeRE.MatchString(r.Store):
		return fmt.Errorf("%s: the store is the name of a ClusterSecretStore", variable)
	case r.Key == "" || len(r.Key) > MaxKey || !keyRE.MatchString(r.Key):
		return fmt.Errorf("%s: the key has up to %d letters, digits and . _ / : @ + = -", variable, MaxKey)
	case r.Property != "" && (len(r.Property) > MaxProperty || !propertyRE.MatchString(r.Property)):
		return fmt.Errorf("%s: the property has up to %d letters, digits and . _ -", variable, MaxProperty)
	}
	return nil
}

// checkValue: printable, bounded, and in the variable's format.
func checkValue(v resourcekind.Variable, s string) error {
	if len(s) > MaxValue || strings.TrimSpace(s) != s || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return fmt.Errorf("%s: a value has up to %d printable characters, without spaces around it", v.Name, MaxValue)
	}
	if len(v.Choices) > 0 && !slices.Contains(v.Choices, s) {
		return fmt.Errorf("%s is one of %s", v.Name, strings.Join(v.Choices, ", "))
	}
	switch v.Format {
	case resourcekind.FormatHost:
		if !host(s) {
			return fmt.Errorf("%s: %q is not a host name or IP address", v.Name, s)
		}
	case resourcekind.FormatPort:
		if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != s {
			return fmt.Errorf("%s: a port is a number from 1 to 65535", v.Name)
		}
	case resourcekind.FormatURL:
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !host(u.Hostname()) || u.User != nil ||
			u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("%s: an address like https://s3.example.com", v.Name)
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s: a port is a number from 1 to 65535", v.Name)
			}
		}
	}
	return nil
}

// host is a DNS name or an IP address the connection may point to. Private
// addresses are fine (a company's database usually lives on one); never the
// machine itself (loopback), the cloud's metadata service (link-local,
// metadata.google.internal), a name of one label (the pod's search domains
// would make it a Service of the cluster) or one under *.svc,
// *.cluster.local or *.localhost. A name whose last label is a number
// (2130706433, 0x7f000001) some resolvers read as an address: refused too.
// What a namespace-qualified name may still reach inside the cluster is
// the app namespace's network policy's call.
func host(s string) bool {
	if ip := net.ParseIP(s); ip != nil {
		return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
			!ip.IsMulticast() && !ip.IsUnspecified()
	}
	if len(s) > 253 || !hostRE.MatchString(s) {
		return false
	}
	l := strings.ToLower(s)
	labels := strings.Split(l, ".")
	if len(labels) < 2 || numeric(labels[len(labels)-1]) || l == "metadata.google.internal" {
		return false
	}
	for _, inside := range []string{".svc", ".cluster.local", ".localhost"} {
		if strings.HasSuffix(l, inside) || strings.Contains(l, inside+".") {
			return false
		}
	}
	return true
}

// numeric: all digits, or a 0x hex number.
func numeric(label string) bool {
	if rest, ok := strings.CutPrefix(label, "0x"); ok && rest != "" {
		label = rest
		return strings.Trim(label, "0123456789abcdef") == ""
	}
	return strings.Trim(label, "0123456789") == ""
}

// quantity is a cheap check of a Kubernetes quantity (the agent parses it
// for real): digits, an optional fraction, an optional unit.
func quantity(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 16 {
		return false
	}
	i, dots, digits := 0, 0, 0
	for ; i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.'); i++ {
		if s[i] == '.' {
			dots++
		} else {
			digits++
		}
	}
	if digits == 0 || dots > 1 {
		return false
	}
	return slices.Contains([]string{"", "m", "Ki", "Mi", "Gi", "Ti", "k", "M", "G", "T"}, s[i:])
}
