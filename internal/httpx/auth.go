package httpx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/iamkit/sdk/authclient/fiberauth"
	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/gofiber/fiber/v2"
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
func Authenticate(iam Introspector, b config.IAM) fiber.Handler {
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
func AuthenticateWith(verify func(ctx context.Context, token string) (*authclient.Claims, error)) fiber.Handler {
	return typed(fiberauth.Authenticate(verify))
}

// RequirePermissions rejects requests whose token lacks any permission.
// Pass permission.X constants, never string literals.
func RequirePermissions(permissions ...string) fiber.Handler {
	return typed(fiberauth.RequirePermissions(permissions...))
}

// typed runs an SDK middleware and, when it answered with its own error body
// instead of passing the request on, replaces that response with an errx
// error. A handler never writes a 401/403 itself (it returns an error), so a
// nil return with such a status on the response means the SDK wrote it.
func typed(next fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := next(c); err != nil {
			return err
		}
		status := c.Response().StatusCode()
		if status != fiber.StatusUnauthorized && status != fiber.StatusForbidden {
			return nil
		}
		var sdk struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(c.Response().Body(), &sdk)
		c.Response().ResetBody()
		c.Response().Header.Del(fiber.HeaderCacheControl)
		if status == fiber.StatusUnauthorized {
			return errx.Unauthorized(or(sdk.Error.Message, "missing or invalid bearer token"))
		}
		return errx.Forbidden(or(sdk.Error.Message, "insufficient permissions"))
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
func Organization(c *fiber.Ctx) (identity.OrganizationID, error) {
	claims := fiberauth.Claims(c)
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
func Can(c *fiber.Ctx, permission string) bool {
	claims := fiberauth.Claims(c)
	return claims != nil && claims.HasPermission(permission)
}

// User returns the authenticated IAMKit user (the token subject).
func User(c *fiber.Ctx) (identity.UserID, error) {
	claims := fiberauth.Claims(c)
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
func Permissions(c *fiber.Ctx) []string {
	if claims := fiberauth.Claims(c); claims != nil {
		return slices.Clone(claims.Permissions)
	}
	return nil
}

// BearerToken is the request's bearer token, already authenticated (only
// for calling IAMKit on the user's behalf: it never leaves for anyone else).
func BearerToken(c *fiber.Ctx) string {
	scheme, token, ok := strings.Cut(c.Get(fiber.HeaderAuthorization), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// Provenance puts the request's event provenance in the user context (after
// Authenticate): every event a handler publishes correlates to the
// X-Request-ID and names the OAuth client used (console, an MCP agent).
func Provenance(c *fiber.Ctx) error {
	meta := event.Meta{CorrelationID: ensureRequestID(c)}
	ctx := c.UserContext()
	if claims := fiberauth.Claims(c); claims != nil {
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
	c.SetUserContext(event.WithMeta(ctx, meta))
	return c.Next()
}

// PublicContext is the user context of a route that has no authenticated
// caller (a webhook, an upload ticket): events published under it correlate
// to the X-Request-ID and name no OAuth client.
func PublicContext(c *fiber.Ctx) context.Context {
	return event.WithMeta(c.UserContext(), event.Meta{CorrelationID: ensureRequestID(c)})
}
