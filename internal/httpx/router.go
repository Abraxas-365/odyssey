package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Handler serves one route: it writes the response, or returns an error that
// WriteError answers with the one error body (docs/api.md §3).
type Handler func(w http.ResponseWriter, r *http.Request) error

// Middleware wraps a Handler: authentication, provenance, a permission check.
type Middleware func(next Handler) Handler

// Std serves a standard http.Handler (the MCP SDK's, IAMKit's metadata) as a
// route.
func Std(h http.Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		h.ServeHTTP(w, r)
		return nil
	}
}

// Route is a mounted method and path ({name} for a path parameter).
type Route struct{ Method, Path string }

// Router mounts routes on the standard http.ServeMux (method patterns,
// {name} parameters read with r.PathValue). A Group shares the mux under a
// path prefix with more middleware; With adds middleware to the routes it
// mounts. The router answers what the mux would (404, 405 with Allow) with
// the standard error body, behind the middleware of the deepest group whose
// prefix holds the path (a /api/v1 path that matches no route is still
// authenticated first). A trailing slash is ignored ("/projects/" is
// "/projects").
type Router struct {
	root   *root
	prefix string
	mw     []Middleware
	top    bool // the router NewRouter returned
}

type root struct {
	mux       *http.ServeMux
	global    []Middleware
	routes    []Route
	fallbacks []fallback
}

// fallback is a group with middleware: its chain runs before the 404/405 of
// a path under its prefix.
type fallback struct {
	prefix string
	mw     []Middleware
}

// NewRouter returns an empty router.
func NewRouter() *Router { return &Router{root: &root{mux: http.NewServeMux()}, top: true} }

// Use adds middleware. On the router NewRouter returned it runs for every
// request, routed or not; on a group, for the routes mounted on it afterwards
// and for the 404/405 under its prefix.
func (r *Router) Use(mw ...Middleware) {
	if r.top {
		r.root.global = append(r.root.global, mw...)
		return
	}
	r.mw = append(r.mw, mw...)
	r.root.fallbacks = append(r.root.fallbacks, fallback{prefix: r.prefix, mw: append([]Middleware{}, r.mw...)})
}

// Group returns a router for the routes under prefix, behind mw. Like a
// Fiber group, mw also runs before the 404/405 of any path under prefix (an
// unknown /api/v1/audit/x is a 403 for a caller without audit:read).
func (r *Router) Group(prefix string, mw ...Middleware) *Router {
	g := &Router{root: r.root, prefix: join(r.prefix, prefix), mw: append(append([]Middleware{}, r.mw...), mw...)}
	if len(mw) > 0 {
		r.root.fallbacks = append(r.root.fallbacks, fallback{prefix: g.prefix, mw: g.mw})
	}
	return g
}

// With returns a router for the same prefix whose routes also go through mw
// (only the routes: an unknown path is not guarded by it):
//
//	r.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/projects", h.list)
func (r *Router) With(mw ...Middleware) *Router {
	return &Router{root: r.root, prefix: r.prefix, mw: append(append([]Middleware{}, r.mw...), mw...)}
}

func (r *Router) Get(path string, h Handler)    { r.Handle(http.MethodGet, path, h) }
func (r *Router) Post(path string, h Handler)   { r.Handle(http.MethodPost, path, h) }
func (r *Router) Put(path string, h Handler)    { r.Handle(http.MethodPut, path, h) }
func (r *Router) Patch(path string, h Handler)  { r.Handle(http.MethodPatch, path, h) }
func (r *Router) Delete(path string, h Handler) { r.Handle(http.MethodDelete, path, h) }

// Options mounts an OPTIONS route (a CORS preflight answered by the route itself).
func (r *Router) Options(path string, h Handler) { r.Handle(http.MethodOptions, path, h) }

// Handle mounts h for method at the router's prefix plus path. A GET route
// also answers HEAD (without a body).
func (r *Router) Handle(method, path string, h Handler) {
	full := join(r.prefix, path)
	chained := chain(r.mw, h)
	r.root.routes = append(r.root.routes, Route{Method: method, Path: full})
	pattern := full
	if pattern == "/" {
		pattern = "/{$}" // "/" alone would match every path
	}
	r.root.mux.Handle(method+" "+pattern, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := chained(w, req); err != nil {
			WriteError(w, req, err)
		}
	}))
}

// Routes are the mounted routes, in mounting order.
func (r *Router) Routes() []Route { return append([]Route(nil), r.root.routes...) }

// ServeHTTP runs the root middleware, then the route (or the 404/405).
//
// The request context keeps its values but not net/http's cancellation: a
// client that disconnects (GitHub timing out a webhook, a closed browser
// tab) must not abort a command halfway, as it never did under Fiber.
// Server shutdown waits for the request instead (http.Server.Shutdown).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	res := &response{ResponseWriter: w}
	req = req.WithContext(context.WithoutCancel(req.Context()))
	if p := req.URL.Path; len(p) > 1 && strings.HasSuffix(p, "/") {
		req = req.Clone(req.Context())
		req.URL.Path = strings.TrimRight(p, "/")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		// keep the escaped form in step (an escaped "/" stays one segment)
		if raw := req.URL.RawPath; raw != "" {
			req.URL.RawPath = strings.TrimRight(raw, "/")
			if req.URL.RawPath == "" {
				req.URL.RawPath = "/"
			}
		}
	}
	if err := chain(r.root.global, r.root.dispatch)(res, req); err != nil {
		WriteError(res, req, err)
	}
}

func (rt *root) dispatch(w http.ResponseWriter, r *http.Request) error {
	// A path that is not clean ("//", "/./") would get the mux's redirect;
	// Fiber answered 404, and so does the router (behind the group's chain).
	if path.Clean(r.URL.Path) != r.URL.Path {
		return rt.refuse(w, r, http.StatusNotFound, "")
	}
	h, pattern := rt.mux.Handler(r)
	if pattern != "" {
		rt.mux.ServeHTTP(w, r) // sets the path values
		return nil
	}
	// The mux's own answer, only to learn its status (404, or 405 + Allow).
	probe := &capture{header: http.Header{}}
	h.ServeHTTP(probe, r)
	status, allow := probe.status, probe.header.Get("Allow")
	if status != http.StatusMethodNotAllowed {
		status, allow = http.StatusNotFound, ""
	}
	return rt.refuse(w, r, status, allow)
}

// refuse answers a 404/405 with the error body, behind the middleware of the
// deepest group whose prefix holds the path.
func (rt *root) refuse(w http.ResponseWriter, r *http.Request, status int, allow string) error {
	final := func(w http.ResponseWriter, _ *http.Request) error {
		if allow != "" {
			w.Header().Set("Allow", allow)
		}
		return errx.FromStatus(status, statusMessage(status))
	}
	if fb := rt.fallbackFor(r.URL.Path); fb != nil {
		return chain(fb.mw, final)(w, r)
	}
	return final(w, r)
}

func (rt *root) fallbackFor(path string) *fallback {
	var best *fallback
	for i := range rt.fallbacks {
		fb := &rt.fallbacks[i]
		if (path == fb.prefix || strings.HasPrefix(path, fb.prefix+"/")) && (best == nil || len(fb.prefix) >= len(best.prefix)) {
			best = fb
		}
	}
	return best
}

// statusMessage is the client text for a status the router answers itself.
func statusMessage(status int) string {
	switch status {
	case http.StatusNotFound:
		return "route not found"
	case http.StatusMethodNotAllowed:
		return "method not allowed"
	}
	return http.StatusText(status)
}

func chain(mw []Middleware, h Handler) Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// join is prefix+path without a trailing slash ("/" for the root).
func join(prefix, path string) string {
	full := strings.TrimRight(prefix, "/")
	if path != "" && path != "/" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		full += path
	}
	full = strings.TrimRight(full, "/")
	if full == "" {
		return "/"
	}
	return full
}

// response records whether the response has started, so an error returned
// after a handler wrote is logged instead of corrupting the body.
type response struct {
	http.ResponseWriter
	wrote bool
}

func (r *response) WriteHeader(status int) {
	if !r.wrote && status >= 200 {
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *response) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush lets streaming handlers (the MCP SDK) flush through the wrapper.
func (r *response) Flush() {
	r.wrote = true
	if err := http.NewResponseController(r.ResponseWriter).Flush(); err != nil {
		slog.Debug("flush", "err", err)
	}
}

// Unwrap gives http.ResponseController the underlying writer (deadlines).
func (r *response) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// started reports whether w (or the response it wraps) has been written.
func started(w http.ResponseWriter) bool {
	for {
		switch x := w.(type) {
		case *response:
			if x.wrote {
				return true
			}
			w = x.ResponseWriter
		case interface{ Unwrap() http.ResponseWriter }:
			w = x.Unwrap()
		default:
			return false
		}
	}
}

// capture is a ResponseWriter that only keeps what was written: the mux's
// 404/405, or an IAMKit SDK middleware's refusal.
type capture struct {
	header http.Header
	status int
	body   []byte
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body = append(c.body, b...)
	return len(b), nil
}
