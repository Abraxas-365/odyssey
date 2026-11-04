package httpx_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

func TestHeaderReceivedGivesRawUploadsTheLongTimeout(t *testing.T) {
	for ct, long := range map[string]bool{
		"application/gzip":                  true,
		"application/octet-stream":          true,
		"multipart/form-data; boundary=abc": true,
		"application/json":                  false,
		"text/plain":                        false,
		"":                                  false,
	} {
		var h fasthttp.RequestHeader
		h.SetContentType(ct)
		got := httpx.HeaderReceived(&h).ReadTimeout == httpx.UploadReadTimeout
		if got != long {
			t.Errorf("Content-Type %q: long timeout %v, want %v", ct, got, long)
		}
	}
}

func TestStreamBodyAndDecodeJSON(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Put("/raw", func(c *fiber.Ctx) error {
		body, err := httpx.StreamBody(c, 10)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		return c.SendString(string(b))
	})
	app.Put("/json", func(c *fiber.Ctx) error {
		var v map[string]any
		if err := httpx.DecodeJSON(c, &v); err != nil {
			return err
		}
		return c.SendStatus(http.StatusNoContent)
	})
	do := func(path, ct, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
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
