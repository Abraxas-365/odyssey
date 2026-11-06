package account

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Queries are the reads, implemented by accountsvc.Service.
type Queries interface {
	// Me is the caller in organization: role, permissions and assistant
	// (nil Assistant when IAMKit cannot be asked).
	Me(ctx context.Context, organization identity.OrganizationID, caller Caller) (Me, error)
}

// Commands are the writes, implemented by accountsvc.Service.
type Commands interface {
	// Disconnect ends the caller's connection of one assistant (a
	// Connection.ID) in organization. Only an assistant connected to
	// Odicea's MCP server can be disconnected here (NotFound otherwise).
	Disconnect(ctx context.Context, organization identity.OrganizationID, caller Caller, connection string) error
}

// Connections reach IAMKit with the caller's own token (the IAMKit adapter,
// accountiam). Account keeps nothing of its own: it has no Repository.
type Connections interface {
	// List is what is connected to the caller's account at Odicea's MCP
	// server in organization.
	List(ctx context.Context, organization identity.OrganizationID, token string) ([]Connected, error)
	Disconnect(ctx context.Context, organization identity.OrganizationID, token, id string) error
}
