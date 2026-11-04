package mcpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is what clients show for the server (and the name the install
// commands give it: `claude mcp add --transport http odicea <url>`).
const ServerName = "odicea"

// Instructions reach every client in initialize: how to publish on Odicea,
// without installing anything. The odicea skill (phase 1d-3) goes deeper.
const Instructions = `Odicea publica apps internas de la empresa en su propia infraestructura, con la seguridad que configuró TI.
Odicea publishes the company's internal apps on its own infrastructure, with the security IT configured.

Flujo / flow:
1. whoami: tu organización y lo que puedes hacer. list_apps / get_app: las apps y su estado.
2. Publicar: create_app → deploy_folder → ejecuta el comando que devuelve en la carpeta del proyecto → get_deployment(wait_seconds) hasta "ready" → comparte la URL.
3. list_versions: el historial; rollback vuelve a una versión anterior.

Reglas para que una app funcione en Odicea:
- Escucha en el puerto $PORT (8080 si no está) y responde en "/".
- Nada de secretos en el código: TI los configura aparte.
- Si una tool falla, lee "next_step" en el error: dice qué hacer.`

var toolName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Registry holds every module's tools.
type Registry struct {
	tools []Tool
}

// NewRegistry checks the tools (unique snake_case names, a known risk, at
// least one permission, a handler) and keeps them in order.
func NewRegistry(tools ...[]Tool) (*Registry, error) {
	r := &Registry{}
	seen := map[string]bool{}
	for _, group := range tools {
		for _, t := range group {
			switch {
			case !toolName.MatchString(t.Name):
				return nil, fmt.Errorf("mcpx: tool %q: names are snake_case", t.Name)
			case seen[t.Name]:
				return nil, fmt.Errorf("mcpx: tool %q is registered twice", t.Name)
			case !t.Risk.Valid():
				return nil, fmt.Errorf("mcpx: tool %q: unknown risk %q", t.Name, t.Risk)
			case len(t.Permissions) == 0:
				return nil, fmt.Errorf("mcpx: tool %q declares no permissions", t.Name)
			case t.add == nil:
				return nil, fmt.Errorf("mcpx: tool %q has no handler (build it with mcpx.Typed)", t.Name)
			case t.Description == "":
				return nil, fmt.Errorf("mcpx: tool %q has no description", t.Name)
			}
			seen[t.Name] = true
			r.tools = append(r.tools, t)
		}
	}
	return r, nil
}

// Tools are the registered tools, in order.
func (r *Registry) Tools() []Tool { return slices.Clone(r.tools) }

// Options tune the endpoint.
type Options struct {
	// Version is the server version clients see (default "dev").
	Version string
	// Logger logs internal tool failures (default slog.Default()).
	Logger *slog.Logger
	// MaxBodyBytes bounds a request body (default 1 MiB, the API's JSON
	// limit).
	MaxBodyBytes int64
}

// principalExtra is the TokenInfo.Extra key carrying the Principal through
// the SDK to the tool handlers.
const principalExtra = "odicea.principal"

// VerifiedBearer is the token the authenticator puts in place of the
// agent's: the SDK carries TokenInfo only for a bearer request, and tools
// can read the request's headers.
const VerifiedBearer = "odicea-verified"

// Handler serves POST /mcp: stateless (no session; every request carries
// its token), JSON responses (long operations long-poll in their tool, never
// stream). The request must already be authenticated, its Authorization
// header replaced by "Bearer "+VerifiedBearer (the agent's token never
// reaches the tools): its context carries the Principal (httpx.MCPAuth) and
// event.Meta; without one it answers 401.
// Each request gets a server listing only the tools its principal may use.
func NewHandler(r *Registry, opts Options) http.Handler {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = 1 << 20
	}
	cache := mcp.NewSchemaCache()
	impl := &mcp.Implementation{Name: ServerName, Title: "Odicea", Version: opts.Version}
	getServer := func(req *http.Request) *mcp.Server {
		caller, ok := FromContext(req.Context())
		if !ok {
			return nil
		}
		s := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: Instructions, SchemaCache: cache, Logger: opts.Logger})
		for _, t := range r.tools {
			if caller.Can(t.Permissions...) {
				t.add(s, sdkTool(t), opts.Logger)
			}
		}
		return s
	}
	h := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       opts.Logger,
		// The API sits behind a proxy or binds every interface; the bearer
		// token (never a cookie) is what protects the endpoint.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        opts.MaxBodyBytes,
	})
	// The SDK hands TokenInfo to every handler (RequestExtra), whatever
	// context it runs the handler under; the principal and provenance travel
	// there. The token was verified by httpx.MCPAuth: this verifier only
	// carries what it found.
	carry := auth.RequireBearerToken(func(_ context.Context, _ string, req *http.Request) (*auth.TokenInfo, error) {
		caller, ok := FromContext(req.Context())
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: caller.User.String(), Scopes: caller.Permissions,
			Extra: map[string]any{principalExtra: caller, metaExtra: event.MetaFrom(req.Context())}}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})
	return carry(h)
}

const metaExtra = "odicea.meta"

// callContext is the context a tool handler runs under: the principal and
// the request's provenance (correlation ID, "mcp:<client>") from the
// request's TokenInfo, so every event the tool publishes is attributed.
func callContext(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return ctx
	}
	extra := req.Extra.TokenInfo.Extra
	if p, ok := extra[principalExtra].(Principal); ok {
		ctx = WithPrincipal(ctx, p)
	}
	if m, ok := extra[metaExtra].(event.Meta); ok {
		ctx = event.WithMeta(ctx, m)
	}
	return ctx
}

func sdkTool(t Tool) *mcp.Tool {
	readOnly := t.Risk == RiskRead
	a := &mcp.ToolAnnotations{Title: t.Title, ReadOnlyHint: readOnly, IdempotentHint: readOnly || t.Annotations.Idempotent}
	if !readOnly {
		destructive := t.Annotations.Destructive
		a.DestructiveHint = &destructive
	}
	openWorld := false
	a.OpenWorldHint = &openWorld
	return &mcp.Tool{Name: t.Name, Title: t.Title, Description: t.Description, Annotations: a}
}
