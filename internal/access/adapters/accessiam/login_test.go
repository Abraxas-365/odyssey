package accessiam_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessiam"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

func TestAuthorizeURL(t *testing.T) {
	l := accessiam.New("http://iam:8080", "https://iam.example/", "client1", "secret")
	raw := l.AuthorizeURL(access.LoginRequest{Redirect: "https://api.example/access/callback", State: "st", Challenge: "ch", Nonce: "no", Organization: "org1"})
	u, err := url.Parse(raw)
	if err != nil || u.Host != "iam.example" || u.Path != "/oauth/authorize" {
		t.Fatal(raw, err)
	}
	q := u.Query()
	// IAMKit refuses an authorization request without state and nonce.
	for k, want := range map[string]string{"client_id": "client1", "redirect_uri": "https://api.example/access/callback", "response_type": "code",
		"state": "st", "nonce": "no", "code_challenge": "ch", "code_challenge_method": "S256", "organization_id": "org1"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("client_secret") != "" {
		t.Error("the secret went to the browser")
	}
	if l.AuthorizeURL(access.LoginRequest{}) == "" || url.Values(mustQuery(t, l.AuthorizeURL(access.LoginRequest{State: "s"}))).Has("organization_id") {
		t.Error("no organization hint expected")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// idToken builds an (unsigned-looking) ID token with these claims; the client
// reads it from the token endpoint and checks the claims, not a signature.
func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(body) + "." + enc([]byte("sig"))
}

// iam is a fake IAMKit whose token endpoint answers idTok.
func iam(t *testing.T, idTok *string, form *url.Values, user, basic *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			*form = r.PostForm
			id, secret, _ := r.BasicAuth()
			*basic = id + ":" + secret
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "token_type": "Bearer", "expires_in": 300, "id_token": *idTok})
		case "/oauth/userinfo":
			*user = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sub":"u1","name":"Ana","email":"a@b.c","environment_id":"env1","organization_id":"org1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExchangeReadsTheUser(t *testing.T) {
	var form url.Values
	var user, basic string
	tok := idToken(t, map[string]any{"sub": "u1", "aud": "client1", "nonce": "n1"})
	srv := iam(t, &tok, &form, &user, &basic)
	l := accessiam.New(srv.URL, srv.URL, "client1", "secret")
	who, err := l.Exchange(context.Background(), "code1", "https://api.example/access/callback", "ver1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if who != (access.Identity{Subject: "u1", Name: "Ana", Email: "a@b.c", Organization: "org1", Environment: "env1"}) {
		t.Fatalf("identity %+v", who)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code1" || form.Get("code_verifier") != "ver1" ||
		form.Get("redirect_uri") != "https://api.example/access/callback" || user != "Bearer at1" || basic != "client1:secret" {
		t.Fatalf("form %v user %s basic %s", form, user, basic)
	}
	// aud may be a list naming this client; with several, azp names it too.
	tok = idToken(t, map[string]any{"sub": "u1", "aud": []string{"other", "client1"}, "azp": "client1", "nonce": "n1"})
	if _, err := l.Exchange(context.Background(), "code1", "r", "v", "n1"); err != nil {
		t.Fatalf("audience list: %v", err)
	}
	tok = idToken(t, map[string]any{"sub": "u1", "aud": []string{"client1"}, "nonce": "n1"})
	if _, err := l.Exchange(context.Background(), "code1", "r", "v", "n1"); err != nil {
		t.Fatalf("a one-element audience list (what IAMKit sends): %v", err)
	}
}

// TestExchangeRefusesAForeignIDToken: the ID token binds the code to this
// sign-in. Another sign-in's nonce, another client's token, another user's
// access token or no ID token at all is a sign-in to start over.
func TestExchangeRefusesAForeignIDToken(t *testing.T) {
	var form url.Values
	var user, basic string
	var tok string
	srv := iam(t, &tok, &form, &user, &basic)
	l := accessiam.New(srv.URL, srv.URL, "client1", "secret")
	for name, c := range map[string]struct {
		claims map[string]any
		nonce  string
		raw    string
	}{
		"another nonce":        {claims: map[string]any{"sub": "u1", "aud": "client1", "nonce": "other"}, nonce: "n1"},
		"no nonce in token":    {claims: map[string]any{"sub": "u1", "aud": "client1"}, nonce: "n1"},
		"no nonce in the flow": {claims: map[string]any{"sub": "u1", "aud": "client1", "nonce": ""}, nonce: ""},
		"another client":       {claims: map[string]any{"sub": "u1", "aud": "client2", "nonce": "n1"}, nonce: "n1"},
		"another user":         {claims: map[string]any{"sub": "u2", "aud": "client1", "nonce": "n1"}, nonce: "n1"},
		"no sub":               {claims: map[string]any{"aud": "client1", "nonce": "n1"}, nonce: "n1"},
		"no aud":               {claims: map[string]any{"sub": "u1", "nonce": "n1"}, nonce: "n1"},
		"null aud":             {claims: map[string]any{"sub": "u1", "aud": nil, "nonce": "n1"}, nonce: "n1"},
		"numeric aud":          {claims: map[string]any{"sub": "u1", "aud": 7, "nonce": "n1"}, nonce: "n1"},
		"object aud":           {claims: map[string]any{"sub": "u1", "aud": map[string]any{"client1": true}, "nonce": "n1"}, nonce: "n1"},
		"shared, no azp":       {claims: map[string]any{"sub": "u1", "aud": []string{"client1", "other"}, "nonce": "n1"}, nonce: "n1"},
		"azp of another":       {claims: map[string]any{"sub": "u1", "aud": "client1", "azp": "other", "nonce": "n1"}, nonce: "n1"},
		"no id token":          {raw: "-", nonce: "n1"},
		"garbage":              {raw: "a.%%%.c", nonce: "n1"},
	} {
		tok = c.raw
		if c.raw == "-" {
			tok = ""
		} else if c.claims != nil {
			tok = idToken(t, c.claims)
		}
		_, err := l.Exchange(context.Background(), "code1", "r", "v", c.nonce)
		var e *errx.Error
		if !errx.As(err, &e) || e.Code != access.CodeFlowInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExchangeFailures(t *testing.T) {
	status := 400
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	l := accessiam.New(srv.URL, srv.URL, "c", "s")
	_, err := l.Exchange(context.Background(), "bad", "r", "v", "n")
	var e *errx.Error
	if !errx.As(err, &e) || e.Code != access.CodeFlowInvalid {
		t.Fatalf("a refused code: %v", err)
	}
	status = 503
	if _, err := l.Exchange(context.Background(), "c", "r", "v", "n"); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("IAMKit down: %v", err)
	}
}
