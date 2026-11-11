// Package mcpx serves Odicea's MCP endpoint (POST /mcp) to coding agents
// (Claude Code, Codex, VS Code, Antigravity): one stateless server over the
// official Go SDK, whose tools each domain declares in its <domain>mcp
// adapter on top of the same Commands/Queries as the REST API.
//
// mcpx knows neither IAMKit nor an HTTP framework: httpx.MCPAuth authenticates the
// request and puts the caller's Principal on its context; Handler lists and
// runs only the tools that Principal may use. Only this package imports the
// SDK (architecture rule), so tools never see it.
package mcpx

import (
	"context"
	"reflect"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Risk is how much a tool can change. read and write run at once; confirm
// (ask the user in the client) and approval (a pending request someone
// approves) are wired in phase 3, but every tool declares its risk now.
type Risk string

const (
	RiskRead     Risk = "read"
	RiskWrite    Risk = "write"
	RiskConfirm  Risk = "confirm"
	RiskApproval Risk = "approval"
)

// Valid reports whether r is a known risk.
func (r Risk) Valid() bool {
	return r == RiskRead || r == RiskWrite || r == RiskConfirm || r == RiskApproval
}

// Annotations are the MCP hints of a tool beyond its risk (read-only follows
// from RiskRead). Clients use them to ask the user before acting.
type Annotations struct {
	// Destructive: the tool may undo or replace what is live (rollback,
	// disconnect).
	Destructive bool
	// Idempotent: calling it again with the same input changes nothing more.
	// Read tools always are.
	Idempotent bool
}

// Tool is one MCP tool. Declare it as a mcpx.Tool literal (Permissions as
// permission.X constants, Risk as a mcpx.Risk* constant; architecture rule)
// and give it its handler with Typed.
type Tool struct {
	// Name is snake_case, unique in the registry: what agents call.
	Name string
	// Title is the short human name clients show.
	Title string
	// Description tells the agent when to use the tool and what comes back.
	Description string
	// Toolset groups tools for the console and docs ("apps", "versions",
	// "github").
	Toolset string
	// Permissions are all required to list or call the tool (at least one).
	Permissions []string
	Risk        Risk
	Annotations Annotations

	// add registers the typed handler on a per-request server (set by Typed).
	add adder
	// out is the handler's output type (set by Typed), for contract tests.
	out reflect.Type
}

// OutputType is the Go type of the tool's result (nil before Typed).
func (t Tool) OutputType() reflect.Type { return t.out }

// Principal is who calls: the user, their organization, the MCP client they
// call through and the permissions this authorization grants (the scopes
// the user consented ∩ what they hold now).
type Principal struct {
	Organization identity.OrganizationID
	User         identity.UserID
	// Client is the OAuth client_id the agent authenticated as: its Client
	// ID Metadata Document URL (https://claude.ai/...) or a registered id.
	Client      string
	Permissions []string
}

// Can reports whether the principal holds every permission.
func (p Principal) Can(permissions ...string) bool {
	for _, want := range permissions {
		if !slices.Contains(p.Permissions, want) {
			return false
		}
	}
	return true
}

type principalKey struct{}

// WithPrincipal puts the authenticated caller on ctx (httpx.MCPAuth does it
// for every request), and as the acting user (internal/actor) for IT's rules:
// what the agent may do is the consented scopes, not the user's full set.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = actor.With(ctx, actor.Actor{User: p.User, Permissions: p.Permissions})
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext is the caller WithPrincipal stored.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
