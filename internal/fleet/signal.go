package fleet

import (
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Signal is a change the gateways must act on at once.
type Signal struct {
	Organization identity.OrganizationID
	Cluster      ClusterID
	// Environment and Name are set when that desired app changed.
	Environment project.EnvironmentID
	Name        string
	// Resource is set (with Environment and Name) when that desired resource
	// changed instead of an app.
	Resource bool
	// Build is set when that desired build changed (dispatched or cancelled).
	Build BuildID
	// Secret is set when a managed value waits for the cluster.
	Secret SecretDeliveryID
	// Revoked: the cluster's certificate is no longer accepted.
	Revoked bool
	// Reenrolled: the cluster enrolled again with a new key; tunnels of the
	// old key end (the new agent reconnects with its own).
	Reenrolled bool
	// Policy: the cluster's egress policy changed.
	Policy bool
}
