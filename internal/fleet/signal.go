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
	// Revoked: the cluster's certificate is no longer accepted.
	Revoked bool
	// Reenrolled: the cluster enrolled again with a new key; tunnels of the
	// old key end (the new agent reconnects with its own).
	Reenrolled bool
}
