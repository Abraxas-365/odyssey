// Package server builds the API's HTTP handler: middleware, health, the
// public routes, MCP and the /api/v1 group.
package server

import (
	"net"
	"net/http"
	"time"

	"github.com/Abraxas-365/odyssey/internal/httpx"
)

// Routes is implemented by every domain's HTTP handler.
type Routes interface {
	Register(r *httpx.Router)
}

// PublicRoutes is implemented by the handlers that also mount
// unauthenticated routes (a provider's callbacks, each checking its own
// proof). Architecture tests confine them to sourcehttp, releasehttp, accesshttp and the server's Assistant.
type PublicRoutes interface {
	RegisterPublic(r *httpx.Router)
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

// New returns the API's router. Every domain route sits under /api/v1 behind the auth chain.
func New(health Health, public PublicConfig, opts Options, auth []httpx.Middleware, routes ...Routes) *httpx.Router {
	app := httpx.NewRouter()
	app.Use(httpx.Recover, httpx.RequestID, httpx.Headers, httpx.RejectContentEncoding, httpx.LimitBody)
	if len(opts.CORSOrigins) > 0 {
		app.Use(httpx.CORS{
			Origins: opts.CORSOrigins,
			Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			Headers: []string{"Authorization", "Content-Type", "X-Request-ID", "Mcp-Protocol-Version", "Mcp-Session-Id"},
			Expose:  []string{"Location", "X-Request-ID", "WWW-Authenticate"},
			MaxAge:  600,
		}.Middleware)
	}

	app.Get("/health", func(w http.ResponseWriter, _ *http.Request) error {
		if err := health(); err != nil {
			return httpx.JSONStatus(w, http.StatusServiceUnavailable, httpx.Map{"status": "unavailable"})
		}
		return httpx.JSON(w, httpx.Map{"status": "ok"})
	})
	app.Get("/api/config", func(w http.ResponseWriter, _ *http.Request) error {
		return httpx.JSON(w, public)
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

	api := app.Group("/api/v1", append(append([]httpx.Middleware{}, auth...), httpx.Provenance)...)
	for _, r := range routes {
		r.Register(api)
	}
	return app
}

// HTTP is the server for h: bodies other than uploads must arrive within
// ReadTimeout (httpx.LimitBody gives uploads httpx.UploadReadTimeout).
func HTTP(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// Serve serves h on ln (tests).
func Serve(ln net.Listener, h http.Handler) *http.Server {
	srv := HTTP(ln.Addr().String(), h)
	go func() { _ = srv.Serve(ln) }()
	return srv
}
