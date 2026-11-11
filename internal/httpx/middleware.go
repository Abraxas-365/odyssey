package httpx

import (
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/google/uuid"
)

// requestIDPattern is what a client-sent X-Request-ID may look like besides a
// UUID (docs/api.md §10): short and free of anything a log line could be
// forged with.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidRequestID reports whether a client-sent X-Request-ID is accepted: a
// UUID, or up to 64 characters of [A-Za-z0-9-_].
func ValidRequestID(s string) bool {
	if requestIDPattern.MatchString(s) {
		return true
	}
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// RequestID gives every response an X-Request-ID: the client's when valid,
// else a new UUID.
func RequestID(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ensureRequestID(w, r)
		return next(w, r)
	}
}

// ensureRequestID returns the response's request id, creating one when the
// request never went through RequestID.
func ensureRequestID(w http.ResponseWriter, r *http.Request) string {
	if id := w.Header().Get("X-Request-ID"); id != "" {
		return id
	}
	id := r.Header.Get("X-Request-ID")
	if !ValidRequestID(id) {
		id = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", id)
	return id
}

// RequestIDOf is the response's request id (set by RequestID).
func RequestIDOf(w http.ResponseWriter, r *http.Request) string { return ensureRequestID(w, r) }

// SecurityHeaders sets the headers of docs/api.md §10. Cache-Control is set
// only when the handler has not (file downloads keep their own).
func SecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-store")
	}
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

// Headers sets SecurityHeaders before the handler runs (a handler may still
// set its own Cache-Control).
func Headers(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		SecurityHeaders(w, r)
		return next(w, r)
	}
}

// RejectContentEncoding answers 415 to any request carrying a Content-Encoding
// other than "identity": the API accepts no compressed request bodies (a tiny
// compressed body could expand without bound in whatever inflates it).
// Install it globally, before any handler reads the body.
func RejectContentEncoding(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		for _, v := range r.Header.Values("Content-Encoding") {
			for _, enc := range strings.Split(v, ",") {
				enc = strings.ToLower(strings.TrimSpace(enc))
				if enc != "" && enc != "identity" {
					return errx.UnsupportedMediaType("Content-Encoding is not supported (send the body uncompressed)")
				}
			}
		}
		return next(w, r)
	}
}

// Recover turns a panic into a 500 (logged with its stack).
func Recover(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) (err error) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				slog.Error("panic", "request_id", ensureRequestID(w, r), "method", r.Method, "path", r.URL.Path,
					"panic", p, "stack", string(debug.Stack()))
				err = errx.Internal(errx.GenericMessage)
			}
		}()
		return next(w, r)
	}
}

// CORS lets the listed browser origins call the API: simple requests get
// Access-Control-Allow-Origin, preflights (OPTIONS with
// Access-Control-Request-Method) are answered 204 here. Origins are compared
// case-insensitively; others get no Access-Control-* header.
type CORS struct {
	Origins, Methods, Headers, Expose []string
	MaxAge                            int
}

// Middleware is the CORS check.
func (c CORS) Middleware(next Handler) Handler {
	origins := make([]string, len(c.Origins))
	for i, o := range c.Origins {
		origins[i] = strings.ToLower(strings.TrimSpace(o))
	}
	all := slices.Contains(origins, "*")
	return func(w http.ResponseWriter, r *http.Request) error {
		h := w.Header()
		origin := strings.ToLower(r.Header.Get("Origin"))
		if origin == "" {
			if !all {
				h.Add("Vary", "Origin")
			}
			return next(w, r)
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") == "" {
			h.Add("Vary", "Origin")
			return next(w, r)
		}
		allow := ""
		if all {
			allow = "*"
		} else if slices.Contains(origins, origin) {
			allow = origin
		}
		if r.Method != http.MethodOptions {
			if !all {
				h.Add("Vary", "Origin")
			}
			if allow != "" {
				h.Set("Access-Control-Allow-Origin", allow)
			}
			if len(c.Expose) > 0 {
				h.Set("Access-Control-Expose-Headers", strings.Join(c.Expose, ","))
			}
			return next(w, r)
		}
		h.Add("Vary", "Access-Control-Request-Method")
		h.Add("Vary", "Access-Control-Request-Headers")
		h.Add("Vary", "Origin")
		if allow != "" {
			h.Set("Access-Control-Allow-Origin", allow)
		}
		h.Set("Access-Control-Allow-Methods", strings.Join(c.Methods, ","))
		h.Set("Access-Control-Allow-Headers", strings.Join(c.Headers, ","))
		if c.MaxAge > 0 {
			h.Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAge))
		}
		if len(c.Expose) > 0 {
			h.Set("Access-Control-Expose-Headers", strings.Join(c.Expose, ","))
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
