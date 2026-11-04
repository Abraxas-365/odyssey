// Package access is the sign-in gate of Odicea apps ("Quién puede abrirla"):
// `odyssey-agent access`, a small HTTP service (same binary and image as the
// agent, its own Deployment) that Traefik's forwardAuth asks before it lets a
// request reach an app whose access mode is "team" or "organization". The
// agent (agent/controllers/app) wires each protected app's Ingress to it.
//
// It imports neither the control plane nor the fleet protocol: the two meet
// only in the ticket contract below (docs/access.md has the same text).
//
// # Ticket contract (what the control plane signs)
//
// An unauthenticated browser is sent to
//
//	<ISSUER>/access/authorize?env=<environment id>&return=<url-encoded absolute URL the user asked for>
//
// where <ISSUER> is the control plane's public API base URL
// (ODYSSEY_ACCESS_ISSUER, e.g. http://localhost:8095). After the user signs in
// and the control plane decides they may open the app, it redirects the
// browser to
//
//	<scheme>://<app host>/_odicea/callback?ticket=<JWT>&return=<relative path>
//
// (return is a path starting with "/" and not "//"; anything else is
// ignored and "/" is used). The gate trades the ticket for a cookie.
//
// The ticket is a JWT signed with EdDSA (Ed25519):
//
//	header: {"alg":"EdDSA","typ":"JWT","kid":"<key id>"}
//	claims:
//	  iss     = ISSUER, exactly as the gate is configured
//	  aud     = the app's host as the browser sent it: host[:port], a plain string
//	  sub     = the user's id          email, name = the user's (may be empty)
//	  org     = the user's organization id
//	  app_org = the organization id of the app
//	  env     = the environment id     mode = "team" or "organization"
//	  iat, exp = Unix seconds; exp - iat <= 300 (tickets live seconds, not minutes)
//	  jti     = unique id; the gate accepts each jti once
//	  typ     = "odicea-access-ticket"
//
// The gate rejects a ticket whose signature, iss, aud, exp, typ, env, mode or
// app_org does not match what it expects for that app, a replayed jti, and in
// mode "team" a user whose org differs from app_org.
//
// Keys are published at <ISSUER>/.well-known/odicea-access-keys (or
// ODYSSEY_ACCESS_JWKS_URL when the gate reaches the control plane by another
// address than the browser does), a standard JWK set:
//
//	{"keys":[{"kty":"OKP","crv":"Ed25519","x":"<base64url>","kid":"…","use":"sig","alg":"EdDSA"}]}
//
// # What the gate sets
//
// Cookie __odicea_access: host-only, Path=/, HttpOnly, SameSite=Lax, Secure
// when the request came over https, 8 hours, HMAC-SHA256 signed and bound to
// {host, env, mode, app_org, user}. On success /verify answers 200 with
// X-Odicea-User (user id), X-Odicea-Email, X-Odicea-Name and X-Odicea-Org
// (the user's organization); Traefik copies them to the app after a strip
// middleware emptied whatever the client sent.
package access

// CookieName is the cookie that holds a signed-in session.
const CookieName = "__odicea_access"

// CallbackPath is where the control plane sends the browser back to, on the
// app's own host.
const CallbackPath = "/_odicea/callback"

// TicketType is the ticket's typ claim.
const TicketType = "odicea-access-ticket"

// Modes the gate enforces.
const (
	ModeTeam         = "team"
	ModeOrganization = "organization"
)

// Headers the gate sets on a verified request.
const (
	HeaderUser  = "X-Odicea-User"
	HeaderEmail = "X-Odicea-Email"
	HeaderName  = "X-Odicea-Name"
	HeaderOrg   = "X-Odicea-Org"
)
