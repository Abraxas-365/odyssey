package access

import "context"

// The sign-in keeps no state (a flow lives sealed in the browser's cookie), so
// this domain has no Repository port.

// Signer signs tickets and publishes the keys that verify them.
type Signer interface {
	Sign(Ticket) (string, error)
	// Keys are the current key first, then the previous ones.
	Keys() []Key
}

// Login is the OIDC client of the access sign-in (an IAMKit confidential client).
type Login interface {
	AuthorizeURL(LoginRequest) string
	// Exchange trades the code for the signed-in user, checking that the ID
	// token carries the sign-in's nonce. A refused code or a foreign ID token
	// is a Validation error with CodeFlowInvalid.
	Exchange(ctx context.Context, code, redirect, verifier, nonce string) (Identity, error)
}

// Commands are the sign-in steps, implemented by accesssvc.Service.
type Commands interface {
	Authorize(ctx context.Context, in Authorize) (Redirect, error)
	Callback(ctx context.Context, in Callback) (Outcome, error)
}

// Queries are the read operations.
type Queries interface {
	// Keys are the public keys tickets verify with (the current one first).
	Keys() []Key
}
