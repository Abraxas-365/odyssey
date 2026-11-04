// Package server builds the Fiber app: middleware, health and route groups.
package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// Routes is implemented by every domain's HTTP handler.
type Routes interface {
	Register(r fiber.Router)
}

// PublicRoutes is implemented by the handlers that also mount
// unauthenticated routes (a provider's callbacks, each checking its own
// proof). Architecture tests confine them to the source domain.
type PublicRoutes interface {
	RegisterPublic(r fiber.Router)
}

// Health reports readiness of a dependency (database).
type Health func() error

// PublicConfig is what the dashboard needs before it can sign in. It is
// served unauthenticated at GET /api/config and holds no secrets.
type PublicConfig struct {
	IAMURL      string `json:"iam_url"`   // IAMKit's browser-facing base URL
	ClientID    string `json:"client_id"` // public OAuth client (code + PKCE); "" when not configured
	Environment string `json:"environment_id"`
}

// Options are the deployment settings of the HTTP server.
type Options struct {
	// CORSOrigins are the browser origins allowed to call the API. Empty (the
	// default) leaves CORS closed: no Access-Control-* header is ever sent.
	CORSOrigins []string
	// Public mount their unauthenticated routes on the root, outside /api/v1.
	Public []PublicRoutes
	// MCP, when set, serves coding agents at POST /mcp and its protected
	// resource metadata. Nil leaves both unmounted.
	MCP *MCP
}

// MCP is the agents' endpoint: Auth authenticates (its own OAuth resource,
// not the console's chain) and Handler serves the tools (mcpx.NewHandler).
type MCP struct {
	Auth    *httpx.MCPAuth
	Handler http.Handler
}

// New returns the app. Every domain route sits under /api/v1 behind the auth chain.
func New(health Health, public PublicConfig, opts Options, auth []fiber.Handler, routes ...Routes) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "odyssey-api",
		ErrorHandler: httpx.ErrorHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		// Bodies are streamed so uploads never sit in memory; httpx.LimitBody
		// buffers every other body (≤ 1 MiB) before any handler runs.
		BodyLimit:                    httpx.MaxJSONBody,
		StreamRequestBody:            true,
		DisablePreParseMultipartForm: true,
		DisableStartupMessage:        true,
	})
	app.Server().HeaderReceived = httpx.HeaderReceived
	app.Use(recover.New(recover.Config{EnableStackTrace: true}), httpx.RequestID, httpx.Headers, httpx.RejectContentEncoding, httpx.LimitBody)
	if len(opts.CORSOrigins) > 0 {
		app.Use(cors.New(cors.Config{
			AllowOrigins:  strings.Join(opts.CORSOrigins, ","),
			AllowMethods:  "GET,POST,PUT,PATCH,DELETE,OPTIONS",
			AllowHeaders:  "Authorization,Content-Type,X-Request-ID,Mcp-Protocol-Version,Mcp-Session-Id",
			ExposeHeaders: "Location,X-Request-ID,WWW-Authenticate",
			MaxAge:        600,
		}))
	}

	app.Get("/health", func(c *fiber.Ctx) error {
		if err := health(); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unavailable"})
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})
	app.Get("/api/config", func(c *fiber.Ctx) error {
		return c.JSON(public)
	})
	for _, p := range opts.Public {
		p.RegisterPublic(app)
	}
	if m := opts.MCP; m != nil {
		for _, path := range m.Auth.MetadataPaths() {
			app.Get(path, m.Auth.Metadata())
			app.Options(path, m.Auth.Metadata())
		}
		// Stateless: POST only. MCP clients probe GET (stream) and DELETE
		// (session): both must get 405, which tells them there is neither.
		app.Post(httpx.MCPPath, m.Auth.Protect(m.Handler))
		app.Get(httpx.MCPPath, m.Auth.Protect(m.Handler))
		app.Delete(httpx.MCPPath, m.Auth.Protect(m.Handler))
	}

	api := app.Group("/api/v1", append(append([]fiber.Handler{}, auth...), httpx.Provenance)...)
	for _, r := range routes {
		r.Register(api)
	}
	return app
}
