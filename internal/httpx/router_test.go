package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
)

// guard refuses every request without X-Allowed (a permission check).
func guard(next httpx.Handler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("X-Allowed") == "" {
			return errx.Forbidden("insufficient permissions")
		}
		return next(w, r)
	}
}

func ok(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, httpx.Map{"id": r.PathValue("id"), "path": r.URL.Path})
}

func router() *httpx.Router {
	app := httpx.NewRouter()
	api := app.Group("/api/v1", httpx.RequestID)
	audit := api.Group("/audit", guard)
	audit.Get("/", ok)
	audit.Get("/{id}", ok)
	api.With(guard).Get("/projects/{id}", ok)
	api.Get("/open", ok)
	return app
}

func serve(t *testing.T, app http.Handler, method, path string, allowed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if allowed {
		req.Header.Set("X-Allowed", "1")
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func TestRouterAnswers(t *testing.T) {
	app := router()
	for _, c := range []struct {
		method, path string
		allowed      bool
		status       int
		body, allow  string
	}{
		{"GET", "/api/v1/open", false, 200, `"path":"/api/v1/open"`, ""},
		{"GET", "/api/v1/open/", false, 200, `"path":"/api/v1/open"`, ""}, // trailing slash ignored
		{"HEAD", "/api/v1/open", false, 200, "", ""},                      // HEAD on every GET
		{"GET", "/api/v1/projects/p1", true, 200, `"id":"p1"`, ""},
		{"GET", "/api/v1/projects/p1", false, 403, `"FORBIDDEN"`, ""},
		{"DELETE", "/api/v1/open", false, 405, `method not allowed`, "GET, HEAD"},
		{"GET", "/api/v1/nope", false, 404, `route not found`, ""},
		{"GET", "/nope", false, 404, `route not found`, ""},
		// a group's middleware guards its unknown paths and methods too
		{"GET", "/api/v1/audit", true, 200, `"path":"/api/v1/audit"`, ""},
		{"GET", "/api/v1/audit/", true, 200, `"path":"/api/v1/audit"`, ""},
		{"GET", "/api/v1/audit/x/y", false, 403, `"FORBIDDEN"`, ""},
		{"POST", "/api/v1/audit/x", false, 403, `"FORBIDDEN"`, ""},
		{"GET", "/api/v1/audit/x/y", true, 404, `route not found`, ""},
		// With guards only its routes
		{"GET", "/api/v1/projects/p1/x", false, 404, `route not found`, ""},
		// paths that are not clean are not redirected
		{"GET", "/api/v1//open", false, 404, `route not found`, ""},
		{"GET", "/api/v1/./open", false, 404, `route not found`, ""},
		// an escaped "/" stays inside one segment, with or without a trailing slash
		{"GET", "/api/v1/projects/a%2Fb", true, 200, `"id":"a/b"`, ""},
		{"GET", "/api/v1/projects/a%2Fb/", true, 200, `"id":"a/b"`, ""},
	} {
		rec := serve(t, app, c.method, c.path, c.allowed)
		name := c.method + " " + c.path
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, c.status, rec.Body)
			continue
		}
		if !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: body %s, want %s", name, rec.Body, c.body)
		}
		if got := rec.Header().Get("Allow"); got != c.allow {
			t.Errorf("%s: Allow %q, want %q", name, got, c.allow)
		}
		if c.status >= 400 && rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: error Content-Type %q", name, rec.Header().Get("Content-Type"))
		}
	}
}

// A client that disconnects does not cancel the handler's context (a command
// is never cut halfway); its values stay.
func TestRouterContextOutlivesTheClient(t *testing.T) {
	type key struct{}
	app := httpx.NewRouter()
	var err error
	var value any
	app.Get("/x", func(w http.ResponseWriter, r *http.Request) error {
		err, value = r.Context().Err(), r.Context().Value(key{})
		return httpx.Status(w, http.StatusNoContent)
	})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "v"))
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	app.ServeHTTP(httptest.NewRecorder(), req)
	if err != nil || value != "v" {
		t.Fatalf("context err %v, value %v", err, value)
	}
}
