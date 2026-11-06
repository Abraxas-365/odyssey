// Package account is the person using Odicea: their role, as the console
// names it, and the AI assistants connected to their account (GET /api/v1/me).
// It keeps nothing: IAMKit holds the connections (the user's consents to
// Odicea's MCP server), read through the Connections port with the user's own
// token, so the console never says "conectado" from a copy that went stale.
package account

import (
	"slices"
	"time"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// Role is the catalog role (iam/permissions.json) whose permissions are
// exactly the token's: "TI", "Builder", "Lector", or "" when they match none
// (a custom grant). The console names a role only when one matches.
func Role(held []string) string {
	for _, r := range permission.Roles {
		if len(r.Permissions) == len(held) && !slices.ContainsFunc(r.Permissions, func(p string) bool { return !slices.Contains(held, p) }) {
			return r.Name
		}
	}
	return ""
}

// Caller is the person asking, as their console token says. Token is that
// bearer token: IAMKit answers about a user's connections only to the user.
type Caller struct {
	User        identity.UserID
	Permissions []string
	Token       string
}

// Connected is a client connected to the caller's account at Odicea's MCP
// server, as IAMKit lists it.
type Connected struct {
	// ID is IAMKit's id for the client; ClientID its OAuth client id.
	ID, ClientID string
	ConsentedAt  *time.Time
	LastUsedAt   *time.Time
}

// Connection is an assistant connected to the user's account at Odicea's MCP
// server, as IAMKit says.
type Connection struct {
	// ID is IAMKit's id for the client: what Disconnect takes.
	ID string `json:"id"`
	// Key is the assistant (assistant.Keys, or assistant.Other).
	Key  string `json:"key"`
	Name string `json:"name"`
	// ConsentedAt is when the user allowed it (nil: never asked — the
	// administrator granted it).
	ConsentedAt *time.Time `json:"consented_at"`
	// LastUsedAt is the newest refresh of its sessions (nil: none live).
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Assistant is the assistant part of /me: what is connected, and what an
// assistant may do with the user's permissions.
type Assistant struct {
	Connected []Connection `json:"connected"`
	assistant.Abilities
}

// Me is GET /api/v1/me.
type Me struct {
	User         identity.UserID         `json:"user_id"`
	Organization identity.OrganizationID `json:"organization_id"`
	Role         string                  `json:"role"`
	Permissions  []string                `json:"permissions"`
	// Assistant is nil when IAMKit could not say what is connected: the
	// console then says nothing rather than "no conectado".
	Assistant *Assistant `json:"assistant"`
}
