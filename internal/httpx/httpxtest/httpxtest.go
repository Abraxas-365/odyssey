// Package httpxtest runs handlers in tests the way the server does: behind
// httpx.WriteError, the request-id/headers/body middleware and a fake
// authentication whose bearer token carries the organization and permissions.
package httpxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// User is the subject of every fake token.
const User = "6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55"

// App is a router with the production error answer, headers, body limit and
// a fake auth. Mount handlers on App().Group(prefix).
func App() *httpx.Router {
	app := httpx.NewRouter()
	app.Use(httpx.RequestID, httpx.Headers, httpx.LimitBody, Auth())
	return app
}

// Auth is a fake authentication whose bearer token (see Token) carries the
// organization and permissions; the subject is always User. Pass it to
// bootstrap.NewServer to exercise the real wiring without an IAMKit.
func Auth() httpx.Middleware {
	return httpx.AuthenticateWith(func(_ context.Context, token string) (*authclient.Claims, error) {
		parts := strings.SplitN(token, "|", 3)
		c := &authclient.Claims{Purpose: "application", OrganizationID: parts[0]}
		c.Subject = User
		if len(parts) > 1 && parts[1] != "" {
			c.Permissions = strings.Split(parts[1], ",")
		}
		if len(parts) > 2 && parts[2] != "" {
			c.Subject = parts[2]
		}
		return c, nil
	})
}

// Token is a bearer token for org holding the given permissions.
func Token(org identity.OrganizationID, permissions ...string) string {
	return org.String() + "|" + strings.Join(permissions, ",")
}

// TokenAs is Token for another subject than User.
func TokenAs(user string, org identity.OrganizationID, permissions ...string) string {
	return Token(org, permissions...) + "|" + user
}

// Response is a finished request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends body (a string, []byte or a value to marshal as JSON; nil for
// none) with a JSON content type.
func Do(t testing.TB, app http.Handler, method, path, token string, body any) Response {
	t.Helper()
	return DoWithHeaders(t, app, method, path, token, body, nil)
}

// DoWithHeaders is Do with extra request headers.
func DoWithHeaders(t testing.TB, app http.Handler, method, path, token string, body any, headers map[string]string) Response {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case string:
		raw = []byte(b)
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return Serve(app, req)
}

// Serve runs req through app and returns the finished response.
func Serve(app http.Handler, req *http.Request) Response {
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return Response{Status: res.StatusCode, Header: res.Header, Body: out}
}

// JSON decodes the body into T.
func JSON[T any](t testing.TB, r Response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
	return v
}

// ErrorCode is error.code of an error response ("" when the body has none).
func ErrorCode(r Response) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

// ErrorField is error.details.field of an error response.
func ErrorField(r Response) string {
	var e struct {
		Error struct {
			Details struct {
				Field string `json:"field"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Details.Field
}

// Want fails the test unless the status matches.
func Want(t testing.TB, r Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}
