package accesshttp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accesshttp"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
)

type fake struct {
	authorize func(access.Authorize) (access.Redirect, error)
	callback  func(access.Callback) (access.Outcome, error)
	seen      access.Callback
}

func (f *fake) Authorize(_ context.Context, in access.Authorize) (access.Redirect, error) {
	return f.authorize(in)
}
func (f *fake) Callback(_ context.Context, in access.Callback) (access.Outcome, error) {
	f.seen = in
	return f.callback(in)
}
func (f *fake) Keys() []access.Key {
	return []access.Key{{ID: "kid1", Public: []byte("0123456789abcdef0123456789abcdef")}}
}

func app(h *accesshttp.Handler) *fiber.App {
	a := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	h.RegisterPublic(a)
	return a
}

func get(t *testing.T, a *fiber.App, path string, cookie string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", access.FlowCookie+"="+cookie)
	}
	res, err := a.Test(req, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestDisabledAnswers404(t *testing.T) {
	a := app(accesshttp.New(nil, nil, false))
	for _, p := range []string{"/.well-known/odicea-access-keys", "/access/authorize?env=e&mode=team&return=x", "/access/callback?code=c&state=s"} {
		res, body := get(t, a, p, "")
		if res.StatusCode != 404 || !strings.Contains(body, access.CodeNotConfigured) {
			t.Errorf("%s: %d %s", p, res.StatusCode, body)
		}
	}
}

func TestKeysAreAJWKS(t *testing.T) {
	f := &fake{}
	res, body := get(t, app(accesshttp.New(f, f, false)), "/.well-known/odicea-access-keys", "")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", res.StatusCode, res.Header)
	}
	var out struct{ Keys []map[string]string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Keys) != 1 {
		t.Fatalf("%v %s", err, body)
	}
	k := out.Keys[0]
	if k["kty"] != "OKP" || k["crv"] != "Ed25519" || k["kid"] != "kid1" || k["use"] != "sig" || k["alg"] != "EdDSA" ||
		k["x"] != base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) {
		t.Fatalf("key %v", k)
	}
}

func TestAuthorizeRedirectsWithTheFlowCookie(t *testing.T) {
	var got access.Authorize
	f := &fake{authorize: func(in access.Authorize) (access.Redirect, error) {
		got = in
		return access.Redirect{Location: "https://iam.example/oauth/authorize?x=1", Flow: "sealed"}, nil
	}}
	res, _ := get(t, app(accesshttp.New(f, f, true)), "/access/authorize?env=e1&mode=team&return=https%3A%2F%2Fapp.example%2Fp", "")
	if res.StatusCode != 302 || res.Header.Get("Location") != "https://iam.example/oauth/authorize?x=1" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	if got.Environment != "e1" || got.Mode != "team" || got.Return != "https://app.example/p" {
		t.Fatalf("question %+v", got)
	}
	c := res.Header.Get("Set-Cookie")
	for _, want := range []string{access.FlowCookie + "=sealed", "path=/access", "httponly", "samesite=lax", "secure", "max-age=600"} {
		if !strings.Contains(strings.ToLower(c), want) {
			t.Errorf("cookie %q lacks %s", c, want)
		}
	}
	// plain http API: no Secure
	res, _ = get(t, app(accesshttp.New(f, f, false)), "/access/authorize?env=e1&mode=team&return=x", "")
	if strings.Contains(strings.ToLower(res.Header.Get("Set-Cookie")), "secure") {
		t.Errorf("Secure on http: %s", res.Header.Get("Set-Cookie"))
	}
}

// Every refusal on the two public routes is the same Spanish page: an unknown
// environment, a foreign return URL, a public app and an expired flow cannot
// be told apart from outside; the code is only in the logs.
func TestRefusalsAreOneGenericPage(t *testing.T) {
	refusals := map[string]error{
		"bad return":  errx.Validation("return is not an address of this app").WithCode(access.CodeBadReturn),
		"unknown env": errx.NotFound("environment not found"),
		"bad env id":  errx.InvalidField("env", "env is not an environment id"),
		"mode":        errx.Validation("not protected").WithCode(access.CodeBadMode),
		"flow":        errx.Validation("expired").WithCode(access.CodeFlowInvalid),
		"another 4xx": errx.Conflict("x"),
	}
	var first string
	for name, refusal := range refusals {
		f := &fake{
			authorize: func(access.Authorize) (access.Redirect, error) { return access.Redirect{}, refusal },
			callback:  func(access.Callback) (access.Outcome, error) { return access.Outcome{}, refusal },
		}
		for _, path := range []string{"/access/authorize?env=e&mode=team&return=https://evil", "/access/callback?code=c&state=s"} {
			res, body := get(t, app(accesshttp.New(f, f, false)), path, "flow")
			if res.StatusCode != 400 || !strings.Contains(body, "No pudimos abrir esta app. Vuelve a intentarlo desde su enlace.") ||
				res.Header.Get("Location") != "" || strings.Contains(body, "ACCESS_") || strings.Contains(body, "environment") {
				t.Errorf("%s %s: %d %s %v", name, path, res.StatusCode, body, res.Header)
			}
			if !strings.HasPrefix(path, "/access/authorize") && res.Header.Get("Set-Cookie") == "" {
				t.Errorf("%s: the flow cookie was not cleared", name)
			}
			if first == "" {
				first = body
			} else if body != first {
				t.Errorf("%s %s: a different page: %s", name, path, body)
			}
			if strings.HasPrefix(path, "/access/authorize") && strings.Contains(res.Header.Get("Set-Cookie"), access.FlowCookie+"=sealed") {
				t.Errorf("%s: a refused question set a flow", name)
			}
		}
	}
	// a failure of ours is the same page with its own 5xx
	f := &fake{authorize: func(access.Authorize) (access.Redirect, error) { return access.Redirect{}, errx.Internal("boom") }}
	res, body := get(t, app(accesshttp.New(f, f, false)), "/access/authorize?env=e&mode=team&return=x", "")
	if res.StatusCode != 500 || body != first {
		t.Errorf("internal: %d %s", res.StatusCode, body)
	}
}

func TestCallbackOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		out    access.Outcome
		err    error
		status int
		body   string
	}{
		{"allowed", access.Outcome{Location: "https://app.example/_odicea/callback?ticket=t&return=%2F"}, nil, 302, ""},
		{"denied", access.Outcome{Denied: true}, nil, 403, "No tienes acceso a esta app"},
		{"flow invalid", access.Outcome{}, errx.Validation("x").WithCode(access.CodeFlowInvalid), 400, "Vuelve a intentarlo desde su enlace"},
		{"iamkit down", access.Outcome{}, errx.External("iamkit"), 502, ""},
	}
	for _, c := range cases {
		f := &fake{callback: func(access.Callback) (access.Outcome, error) { return c.out, c.err }}
		res, body := get(t, app(accesshttp.New(f, f, false)), "/access/callback?code=c1&state=s1", "flow1")
		if res.StatusCode != c.status || !strings.Contains(body, c.body) {
			t.Errorf("%s: %d %s", c.name, res.StatusCode, body)
		}
		if f.seen.Code != "c1" || f.seen.State != "s1" || f.seen.Flow != "flow1" {
			t.Errorf("%s: callback saw %+v", c.name, f.seen)
		}
		if c.status == 302 && res.Header.Get("Location") != c.out.Location {
			t.Errorf("%s: location %s", c.name, res.Header.Get("Location"))
		}
		if cc := res.Header.Get("Set-Cookie"); !strings.Contains(cc, access.FlowCookie+"=;") && !strings.Contains(cc, "Max-Age=0") && !strings.Contains(cc, "expires") {
			t.Errorf("%s: the flow cookie was not cleared: %q", c.name, cc)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: cache %q", c.name, res.Header.Get("Cache-Control"))
		}
		if c.status == 403 && strings.Contains(body, "ticket") {
			t.Errorf("%s: a denial carries a ticket", c.name)
		}
	}
}
