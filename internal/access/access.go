// Package access is the sign-in of apps protected with an Odicea login
// (docs/access.md): the gate of an app's cluster sends the browser here, the
// user signs in with IAMKit, and this control plane answers with a short
// signed ticket the gate verifies with the public keys published here. The
// routes are unauthenticated by nature; the proof is the OAuth flow.
package access

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

const (
	// TicketType is the `typ` claim of a ticket.
	TicketType = "odicea-access-ticket"
	// TicketTTL is how long a ticket lives (the gate allows up to 300 s).
	TicketTTL = 60 * time.Second
	// FlowTTL is how long a sign-in may take between /access/authorize and the callback.
	FlowTTL = 10 * time.Minute
	// FlowCookie holds the sealed state of a sign-in in progress.
	FlowCookie = "__odicea_flow"
	// CallbackPath is where the gate of the app takes the ticket.
	CallbackPath = "/_odicea/callback"
)

// Error codes.
const (
	// CodeNotConfigured: this control plane has no signing key or IAMKit client.
	CodeNotConfigured = "ACCESS_NOT_CONFIGURED"
	// CodeFlowInvalid: the sign-in expired, was tampered with or its code was refused.
	CodeFlowInvalid = "ACCESS_FLOW_INVALID"
	// CodeBadReturn: `return` is not an address of the environment's app.
	CodeBadReturn = "ACCESS_BAD_RETURN"
	// CodeBadMode: the app is public, or not served with the mode the gate
	// asked for (its access changed meanwhile).
	CodeBadMode = "ACCESS_BAD_MODE"
)

// Key is a public key tickets are verified with.
type Key struct {
	ID     string
	Public ed25519.PublicKey
}

// KeyID names a key: base64url(sha256(public key))[:16].
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

// Ticket is what the gate verifies (docs/access.md "Ticket contract").
type Ticket struct {
	Issuer string
	// Audience is the app's host as the browser sent it, host[:port].
	Audience string
	Subject  string
	Email    string
	Name     string
	// Organization is the user's; AppOrganization the app's.
	Organization    string
	AppOrganization string
	Environment     string
	Mode            string
	ID              string
	IssuedAt        time.Time
	Expires         time.Time
}

// Identity is the signed-in user as IAMKit reports them.
type Identity struct {
	Subject      string
	Email        string
	Name         string
	Organization string
	// Environment is the IAMKit environment the user belongs to.
	Environment string
}

// LoginRequest starts an authorization-code + PKCE sign-in.
type LoginRequest struct {
	Redirect  string
	State     string
	Challenge string
	// Nonce binds the ID token to this sign-in (IAMKit requires one).
	Nonce string
	// Organization hints which organization to sign in to ("" for none).
	Organization string
}

// Authorize is the gate's question: sign this user in for this app.
type Authorize struct {
	Environment string
	Mode        string
	// Return is the absolute URL the user wanted to open.
	Return string
}

// Redirect sends the browser somewhere, remembering the flow in a cookie.
type Redirect struct {
	Location string
	// Flow is the cookie value (sealed); empty when there is none to set.
	Flow string
}

// Callback is IAMKit sending the browser back.
type Callback struct {
	Flow  string
	State string
	Code  string
}

// Outcome is the decision: Location when allowed, Denied otherwise.
type Outcome struct {
	Location string
	Denied   bool
}
