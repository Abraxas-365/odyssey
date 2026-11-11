package httpx_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/httpx"
)

func TestStreamBodyAndDecodeJSON(t *testing.T) {
	app := httpx.NewRouter()
	app.Use(httpx.LimitBody)
	app.Put("/raw", func(w http.ResponseWriter, r *http.Request) error {
		body, err := httpx.StreamBody(w, r, 10)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		return httpx.Send(w, b)
	})
	app.Put("/json", func(w http.ResponseWriter, r *http.Request) error {
		var v map[string]any
		if err := httpx.DecodeJSON(r, &v); err != nil {
			return err
		}
		return httpx.Status(w, http.StatusNoContent)
	})
	do := func(path, ct, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := do("/raw", "application/gzip", "0123456789"); code != 200 || body != "0123456789" {
		t.Fatalf("exactly at the limit: %d %q", code, body)
	}
	if code, _ := do("/raw", "application/octet-stream", "01234567890"); code != 413 {
		t.Fatalf("over the limit: %d", code)
	}
	if code, _ := do("/raw", "application/json", "{}"); code != 415 {
		t.Fatalf("json to a raw route: %d", code)
	}
	for _, ct := range []string{"application/gzip", "application/octet-stream"} {
		if code, _ := do("/json", ct, "{}"); code != 415 {
			t.Fatalf("%s to a json route: %d", ct, code)
		}
	}
	if code, _ := do("/json", "application/json", "{}"); code != 204 {
		t.Fatalf("json: %d", code)
	}
}
