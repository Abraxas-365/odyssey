package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/resourceserver"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
)

// MCPPath is where coding agents connect.
const MCPPath = "/mcp"

// MCPIntrospectionCache is how long an active introspection answer is kept:
// a burst of tool calls costs one call to IAMKit, a revocation takes effect
// within this long.
const MCPIntrospectionCache = 15 * time.Second

// MCPAuth authenticates POST /mcp: Odicea is an OAuth protected resource of
// IAMKit (RFC 9728), the coding agent its client. A token is admitted only
// when it was issued for this resource (RFC 8707 resource claim, audience
// IAM.Audience), for its environment, application and resource, and IAMKit's
// introspection says it is still live. The caller reaches the tools as a
// neutral mcpx.Principal: mcpx never sees IAMKit.
type MCPAuth struct {
	server *resourceserver.Server
	iam    config.IAM
}

// MCPMetadataName is the resource name agents show on the consent screen.
const MCPMetadataName = "Odicea"

// NewMCPAuth builds the authenticator. scopes are advertised as
// scopes_supported (the permission catalog). The issuer is fetched (JWKS,
// introspection) at IAM.URL when it is not reachable as is (in compose,
// tokens say http://localhost:8085 and the API reaches http://iamkit:8080).
func NewMCPAuth(iam config.IAM, m config.MCP, scopes []string) (*MCPAuth, error) {
	if !m.Enabled() {
		return nil, errors.New("httpx: MCP needs IAMKIT_INTROSPECTION_CLIENT_ID and its secret")
	}
	client, err := issuerClient(iam)
	if err != nil {
		return nil, err
	}
	srv, err := resourceserver.New(resourceserver.Config{
		Issuer:          iam.Issuer,
		Resource:        iam.Audience,
		EnvironmentID:   iam.Environment,
		ScopesSupported: scopes,
		ResourceName:    MCPMetadataName,
		HTTPClient:      client,
		Introspection: &resourceserver.Introspection{ClientID: m.IntrospectionClientID, ClientSecret: m.IntrospectionClientSecret,
			CacheTTL: MCPIntrospectionCache},
	})
	if err != nil {
		return nil, err
	}
	// The resource is the endpoint's URL (RFC 8707/9728): an agent that
	// checks it against the URL it was given refuses anything else.
	if u, err := url.Parse(srv.Resource()); err != nil || u.Path != MCPPath {
		return nil, fmt.Errorf("httpx: MCP needs IAMKIT_AUDIENCE to be the endpoint's URL (https://<odicea>%s), not %q", MCPPath, srv.Resource())
	}
	return &MCPAuth{server: srv, iam: iam}, nil
}

// Resource is the canonical resource URI (the tokens' audience).
func (a *MCPAuth) Resource() string { return a.server.Resource() }

// MetadataPaths are where Metadata is mounted: the RFC 9728 path for
// /mcp and the root well-known path some clients probe first.
func (a *MCPAuth) MetadataPaths() []string { return a.server.MetadataPaths() }

// Metadata serves the protected resource metadata (public, CORS *: agents
// read it to find IAMKit).
func (a *MCPAuth) Metadata() fiber.Handler { return adaptor.HTTPHandler(a.server.MetadataHandler()) }

// Protect serves next (mcpx.NewHandler) to authenticated agents: the
// Principal and the request's provenance (X-Request-ID, "mcp:<client_id>")
// are on the request context. Failures answer like the IAMKit SDK (401 with
// the WWW-Authenticate challenge that starts the agent's OAuth flow, 503
// with Retry-After when IAMKit cannot be asked); the agent's client needs
// that shape, not the REST error body.
func (a *MCPAuth) Protect(next http.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		requestID := ensureRequestID(c)
		if c.Method() != fiber.MethodPost {
			// Stateless: no GET stream, no session to DELETE.
			c.Set(fiber.HeaderAllow, fiber.MethodPost)
			return c.SendStatus(fiber.StatusMethodNotAllowed)
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		claims, err := a.server.Verify(ctx, resourceserver.Bearer(c.Get(fiber.HeaderAuthorization)))
		cancel()
		var caller mcpx.Principal
		if err == nil {
			caller, err = a.principal(claims)
		}
		if err != nil {
			if errors.Is(err, resourceserver.ErrUnavailable) {
				slog.Warn("mcp: IAMKit could not check a token", "request_id", requestID)
			}
			return adaptor.HTTPHandlerFunc(func(w http.ResponseWriter, _ *http.Request) { a.server.Deny(w, err, nil) })(c)
		}
		meta := event.Meta{CorrelationID: requestID, Client: assistant.MCPClientPrefix + caller.Client}
		ctx = event.WithMeta(mcpx.WithPrincipal(c.UserContext(), caller), meta)
		return adaptor.HTTPHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The agent's token is never passed on (tools see the request's
			// headers): mcpx needs only that a bearer is present.
			r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})(c)
	}
}

// principal checks the token's boundaries beyond the SDK's (it checks the
// audience, environment and resource claim) and translates it.
func (a *MCPAuth) principal(c *resourceserver.Claims) (mcpx.Principal, error) {
	if c.ApplicationID != a.iam.Application || c.ResourceID != a.iam.Resource || c.ClientID == "" {
		return mcpx.Principal{}, resourceserver.ErrInvalidToken
	}
	org, err := identity.ParseOrganizationID(c.OrganizationID)
	if err != nil {
		return mcpx.Principal{}, resourceserver.ErrInvalidToken
	}
	user, err := identity.ParseUserID(c.Subject)
	if err != nil {
		return mcpx.Principal{}, resourceserver.ErrInvalidToken
	}
	return mcpx.Principal{Organization: org, User: user, Client: c.ClientID, Permissions: c.Permissions}, nil
}

// issuerClient reaches the issuer: directly when IAM.URL is the issuer,
// else by sending the issuer's requests to IAM.URL (same IAMKit, another
// network name).
func issuerClient(iam config.IAM) (*http.Client, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	issuer, internal := strings.TrimRight(iam.Issuer, "/"), strings.TrimRight(iam.URL, "/")
	if internal == "" || internal == issuer {
		return client, nil
	}
	from, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	to, err := url.Parse(internal)
	if err != nil {
		return nil, err
	}
	client.Transport = rewriteHost{from: from, to: to, next: http.DefaultTransport}
	return client, nil
}

type rewriteHost struct {
	from, to *url.URL
	next     http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == r.from.Scheme && req.URL.Host == r.from.Host {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
		req.Host = r.to.Host
	}
	return r.next.RoundTrip(req)
}
