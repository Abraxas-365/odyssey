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
	switch k {
	case resourcekind.Postgres:
		return PostgresSecret(resource), []string{"uri", "host", "port", "dbname", "user", "password"}
	case resourcekind.Redis:
		return RedisName(resource), []string{RedisKeyURL}
	}
	return "", nil
}
