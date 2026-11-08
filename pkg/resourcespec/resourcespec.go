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
	"slices"

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
)

// Builtin is the capability of preparing kind with the built-in engine.
func Builtin(k resourcekind.Kind) string { return "builtin:" + string(k) }

// Engines.
const EngineBuiltin = "builtin"

// Spec is how a resource is prepared beyond its kind and size. The zero
// value is the basic resource every agent prepares.
type Spec struct {
	// Postgres settings (nil: the basic database).
	Postgres *Postgres `json:"postgres,omitempty"`
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
func (s Spec) IsZero() bool { return s.Postgres == nil || *s.Postgres == Postgres{} }

// Requires are the capabilities an agent needs to honour s for kind (the
// built-in engine of the kind, plus what the spec asks for), sorted.
func (s Spec) Requires(k resourcekind.Kind) []string {
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
