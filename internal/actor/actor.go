// Package actor carries who is acting (the authenticated user and what the
// token lets them do) on the context, transport-neutral: the REST
// middleware and the MCP gateway put it there, and a domain's policy
// adapter reads it, so IT's rules can ask "does the actor hold X" without any
// domain importing IAMKit or a transport (docs/policy.md). A webhook carries
// none (a system Actor naming the sender, with no permissions); a workflow or
// an upload ticket carries the Actor of the request that started it, as it was
// then, and puts it back on the context of the deferred step.
package actor

import (
	"context"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Actor is the authenticated caller. It is also what an asynchronous path
// (a workflow, an upload ticket) carries from the request that started it, so
// the rules that judge the deferred work still see who asked: the permissions
// are the snapshot taken at request time.
type Actor struct {
	User identity.UserID `json:"user,omitempty"`
	// System names a non-human actor when User is zero, e.g. "github:ana" for
	// a push to a connected repository. It holds no permissions.
	System      string   `json:"system,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// ID is who the actor is for a rule: the user, else the system actor's name.
func (a Actor) ID() string {
	if !a.User.IsZero() {
		return a.User.String()
	}
	return a.System
}

// Can reports whether the actor holds the permission.
func (a Actor) Can(permission string) bool { return slices.Contains(a.Permissions, permission) }

type key struct{}

// With puts the actor on ctx.
func With(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, key{}, a)
}

// From is the actor With stored; the zero Actor when none (a system action).
func From(ctx context.Context) Actor {
	a, _ := ctx.Value(key{}).(Actor)
	return a
}
