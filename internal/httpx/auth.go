package httpx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/iamkit/sdk/authclient/httpauth"
	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Introspector is the IAMKit call used to validate access tokens online
// (live revocation). *authclient.Client satisfies it.
type Introspector interface {
	Introspect(ctx context.Context, token, issuer, audience, environment, application, resource string) (*authclient.Claims, error)
}

// Authenticate validates the bearer token against IAMKit using the trusted
// boundaries from configuration. Fails closed if IAMKit is unreachable.
//
// The SDK middleware writes its own error JSON; its responses are replaced by
// the one error shape of docs/api.md §3 (401 UNAUTHORIZED).
func Authenticate(iam Introspector, b config.IAM) Middleware {
	return AuthenticateWith(func(ctx context.Context, token string) (*authclient.Claims, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		claims, err := iam.Introspect(ctx, token, b.Issuer, b.Audience, b.Environment, b.Application, b.Resource)
		if err != nil {
			return nil, err
		}
		if resourceBound(token) || !consoleClient(claims.OAuthClientID, b) {
			return nil, errNotConsole
		}
		return claims, nil
	})
}

// errNotConsole refuses a valid token that was not issued to the console.
var errNotConsole = errors.New("token was not issued to the console")

// consoleClient: the REST API takes only tokens of the console's OAuth client
// (or of a direct sign-in, with none). Every other client of the resource is
// a coding agent's, consented for the MCP tools only: a registered (template)
// agent client that leaves out RFC 8707's resource gets a token without the
// claim, so the claim alone cannot tell them apart.
func consoleClient(client string, b config.IAM) bool {
	return client == "" || (b.WebClientID != "" && client == b.WebClientID)
}

// resourceBound reports whether a token carries the RFC 8707 resource
// claim: a coding agent's token for /mcp (same audience as the REST API).
// The agent's grant was consented for the MCP tools and checked by them;
// the REST API takes only the console's tokens, so a leaked agent token
// reaches nothing beyond its tools. The token was already verified, its
// payload is only read.
func resourceBound(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return true // verified but unreadable: refuse
	}
	var payload struct {
		Resource json.RawMessage `json:"resource"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return true
	}
	return len(payload.Resource) > 0 && string(payload.Resource) != "null"
}

// AuthenticateWith authenticates bearer tokens with verify, answering
// failures with the standard error body (docs/api.md §3). Authenticate uses
// it with IAMKit; tests use it with a fake verifier.
func AuthenticateWith(verify func(ctx context.Context, token string) (*authclient.Claims, error)) Middleware {
	return typed(func(next http.Handler) http.Handler { return httpauth.Authenticate(verify, next) })
}

// RequirePermissions rejects requests whose token lacks any permission.
// Pass permission.X constants, never string literals.
func RequirePermissions(permissions ...string) Middleware {
	return typed(httpauth.RequirePermissionsMiddleware(permissions...))
}

// typed runs an IAMKit SDK middleware (authclient/httpauth). When it passes
// the request on, the route runs with the SDK's request (its claims on the
// context); when it answers with its own error body, that answer is dropped
// and replaced by an errx error (the one error shape).
func typed(sdk func(http.Handler) http.Handler) Middleware {
	return func(next Handler) Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			var err error
			passed := false
			refusal := &capture{header: http.Header{}}
			sdk(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				passed = true
				err = next(w, r)
			})).ServeHTTP(refusal, r)
			if passed {
				return err
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(refusal.body, &body)
			if refusal.status == http.StatusForbidden {
				return errx.Forbidden(or(body.Error.Message, "insufficient permissions"))
			}
			return errx.Unauthorized(or(body.Error.Message, "missing or invalid bearer token"))
		}
	}
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Organization returns the tenant of the authenticated user. Every tenant
// query must be constrained by this value — never by a request parameter.
func Organization(r *http.Request) (identity.OrganizationID, error) {
	claims := httpauth.Claims(r)
	if claims == nil {
		return identity.OrganizationID{}, errx.Unauthorized("missing or invalid bearer token")
	}
	if claims.Purpose != "application" || claims.OrganizationID == "" {
		return identity.OrganizationID{}, errx.Forbidden("token is not bound to an organization")
	}
	org, err := identity.ParseOrganizationID(claims.OrganizationID)
	if err != nil {
		return identity.OrganizationID{}, errx.Forbidden("token organization is invalid")
	}
	return org, nil
}

// Can reports whether the authenticated token holds the permission (for
// a rule that depends on the request, which RequirePermissions cannot say).
func Can(r *http.Request, permission string) bool {
	claims := httpauth.Claims(r)
	return claims != nil && claims.HasPermission(permission)
}

// User returns the authenticated IAMKit user (the token subject).
func User(r *http.Request) (identity.UserID, error) {
	claims := httpauth.Claims(r)
	if claims == nil || claims.Subject == "" {
		return identity.UserID{}, errx.Unauthorized("missing or invalid bearer token")
	}
	user, err := identity.ParseUserID(claims.Subject)
	if err != nil {
		return identity.UserID{}, errx.Forbidden("token subject is invalid")
	}
	return user, nil
}

// Permissions are the authenticated token's permissions (nil without one).
func Permissions(r *http.Request) []string {
	if claims := httpauth.Claims(r); claims != nil {
		return slices.Clone(claims.Permissions)
	}
	return nil
}

// BearerToken is the request's bearer token, already authenticated (only
// for calling IAMKit on the user's behalf: it never leaves for anyone else).
func BearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// Provenance puts the request's event provenance in the user context (after
// Authenticate): every event a handler publishes correlates to the
// X-Request-ID and names the OAuth client used (console, an MCP agent).
func Provenance(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		return next(w, r.WithContext(provenance(w, r)))
	}
}

func provenance(w http.ResponseWriter, r *http.Request) context.Context {
	meta := event.Meta{CorrelationID: ensureRequestID(w, r)}
	ctx := r.Context()
	if claims := httpauth.Claims(r); claims != nil {
		// the console's OAuth client (Authenticate admits no other), or
		// ClientAPI for a token that names none: never empty, which would
		// read as "not recorded"
		meta.Client = claims.OAuthClientID
		if meta.Client == "" {
			meta.Client = event.ClientAPI
		}
		// who acts, for IT's rules (internal/actor): the token's permissions.
		// A token without a valid user subject carries no actor.
		if user, err := identity.ParseUserID(claims.Subject); err == nil {
			ctx = actor.With(ctx, actor.Actor{User: user, Permissions: claims.Permissions})
		}
	}
	return event.WithMeta(ctx, meta)
}

// PublicContext is the user context of a route that has no authenticated
// caller (a webhook, an upload ticket): events published under it correlate
// to the X-Request-ID and name no OAuth client.
func PublicContext(w http.ResponseWriter, r *http.Request) context.Context {
	return event.WithMeta(r.Context(), event.Meta{CorrelationID: ensureRequestID(w, r)})
}
