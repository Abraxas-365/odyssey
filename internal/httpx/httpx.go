// Package httpx holds the net/http helpers of the API: the router, request
// parsing (JSON bodies, pagination, filters), IAMKit authentication, the
// headers every response carries and WriteError, which turns every error
// into the one error body of docs/api.md §3.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Map is a JSON object written inline.
type Map = map[string]any

// WriteError answers err with {"error": {...}} (docs/api.md §3): *errx.Error
// as typed, anything else as a generic 500. 5xx bodies never carry the
// cause; it goes to the log together with the request id. An error returned
// after the response started can only be logged.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *errx.Error
	if !errors.As(err, &e) {
		e = errx.Internal(errx.GenericMessage)
	}
	id := ensureRequestID(w, r)
	if started(w) {
		slog.Error("request failed after the response started", "request_id", id, "method", r.Method, "path", r.URL.Path, "err", err)
		return
	}
	if e.HTTPStatus >= 500 {
		slog.Error("request failed", "request_id", id, "method", r.Method, "path", r.URL.Path,
			"status", e.HTTPStatus, "code", e.Code, "err", err)
	}
	h := w.Header()
	h.Del("Content-Disposition")
	h.Del("ETag")
	SecurityHeaders(w, r)
	if e.HTTPStatus == http.StatusUnauthorized {
		h.Set("WWW-Authenticate", "Bearer")
	}
	_ = JSONStatus(w, e.HTTPStatus, Map{"error": e.ToResponse(id)})
}

// JSON answers 200 with v as JSON.
func JSON(w http.ResponseWriter, v any) error { return JSONStatus(w, http.StatusOK, v) }

// JSONStatus answers status with v as JSON.
func JSONStatus(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return writeErr(err)
}

// Status answers status without a body.
func Status(w http.ResponseWriter, status int) error {
	w.WriteHeader(status)
	return nil
}

// Send answers 200 with body (Content-Type set by the caller).
func Send(w http.ResponseWriter, body []byte) error {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body)
	return writeErr(err)
}

// HTML answers 200 with an HTML page.
func HTML(w http.ResponseWriter, status int, page string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write([]byte(page))
	return writeErr(err)
}

// Redirect answers status (302) with Location.
func Redirect(w http.ResponseWriter, location string, status int) error {
	w.Header().Set("Location", location)
	w.WriteHeader(status)
	return nil
}

// writeErr: the client went away while the body was written; nothing to
// answer anymore (WriteError only logs once the response started).
func writeErr(err error) error {
	if err != nil {
		return errors.New("httpx: writing the response: " + err.Error())
	}
	return nil
}

// Secure reports a request that reached the API over TLS (directly or
// through a proxy that says so).
func Secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
