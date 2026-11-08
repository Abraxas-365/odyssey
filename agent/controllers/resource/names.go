// Package resource is the agent's controller for OdysseyResource: it turns
// each resource of an environment into the objects that provide it — a
// CloudNativePG Cluster (postgres), a password-protected Redis (redis) or a
// PersistentVolumeClaim (files) — reports how it is going in the resource's
// status, and removes it all, data included, when the control plane says so.
//
// The names of what it makes are part of the contract with the app
// controller (agent/controllers/app), which wires an app to a resource by
// them: only this file says them.
package resource

import (
	"slices"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

const (
	// LabelManagedBy marks what the agent owns (its cache sees only these).
	// It is the same label and value as agent/controllers/app uses.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "odyssey-agent"
	// LabelResource names the OdysseyResource an object belongs to.
	LabelResource = "odyssey.dev/resource"
	// Finalizer holds an OdysseyResource until its data is gone and the
	// control plane has been told (phase deleted).
	Finalizer = "odyssey.dev/resource"
)

// RedisImage is the official Redis image, pinned by digest (the index of
// the 7.4-alpine tag; multi-arch).
const RedisImage = "redis:7.4-alpine@sha256:8da19c7399dca900c6822a1cf30f9d2285585e2c4b457ff4095b0f4500054d3f"

// PostgresImages are the CloudNativePG PostgreSQL images a database of a
// pinned major version runs (resourcespec.Majors; a test holds them to it),
// each the latest minor when pinned, by the digest of its multi-arch index.
// A new digest here reaches new databases only: a running one keeps its
// image (CloudNativePG's own rolling update is IT's call).
var PostgresImages = map[int32]string{
	16: "ghcr.io/cloudnative-pg/postgresql:16.14-standard-bookworm@sha256:b459345655ea1a0438a219f599cbae509c3132dc8faad6d729d6957e70a618bb",
	17: "ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm@sha256:f94c0eea2a7e9d40880adebd27a912d8473d5805a99c4dbc6eb6244ad9770620",
	18: "ghcr.io/cloudnative-pg/postgresql:18.4-standard-bookworm@sha256:702a5fd1e2de98afc72965149e90a633082df964087697b9cf89a81f21e343a7",
}

// RedisName is the name of a redis resource's Secret, PersistentVolumeClaim,
// Deployment and Service.
func RedisName(resource string) string { return resource + "-redis" }

// FilesClaim is the PersistentVolumeClaim of a files resource.
func FilesClaim(resource string) string { return resource + "-files" }

// PostgresSecret is the Secret CloudNativePG makes for the application
// user of the Cluster named after a postgres resource.
func PostgresSecret(resource string) string { return resource + "-app" }

// Keys of the redis Secret.
const (
	RedisKeyPassword = "password"
	RedisKeyURL      = "url"
)

// Connection says which Secret an app reads a resource of kind k from, and
// which key of it feeds each of resourcekind.Variables(k, …) (same order).
// A files resource has no Secret (nil keys): the app mounts its claim.
func Connection(k resourcekind.Kind, resource string) (secret string, keys []string) {
	e, ok := engines[v1alpha1.ResourceKind(k)]
	if !ok || e.secret == nil {
		return "", nil
	}
	return e.secret(resource), slices.Clone(e.keys)
}
