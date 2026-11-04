package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	name   = "portal.apps.test"
	host   = name + ":8088" // as browsers send it (the ticket's aud)
	envID  = "11111111-1111-4111-8111-111111111111"
	orgID  = "22222222-2222-4222-8222-222222222222"
	otherO = "33333333-3333-4333-8333-333333333333"
)

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t      *testing.T
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	issuer *httptest.Server
	fetchs atomic.Int32
	gate   *Gate
	now    time.Time
	jti    int
}

func newFixture(t *testing.T) *fixture {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fixture{t: t, pub: pub, priv: priv, now: t0}
	f.issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetchs.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(f.pub), "kid": "k1", "use": "sig", "alg": "EdDSA"}}})
	}))
	t.Cleanup(f.issuer.Close)
	key := make([]byte, 32)
	rand.Read(key)
	codec, _ := NewCodec(key)
	v := &Verifier{Issuer: "http://issuer.test", JWKSURL: f.issuer.URL, Now: func() time.Time { return f.now }}
	f.gate = &Gate{Issuer: "http://issuer.test", Verifier: v, Cookies: codec, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return f.now }}
	return f
}

// ticket signs a valid ticket; edit changes the claims first.
func (f *fixture) ticket(edit func(jwt.MapClaims)) string {
	f.jti++
	c := jwt.MapClaims{
		"iss": "http://issuer.test", "aud": host, "sub": "user-1", "email": "ana@x.test", "name": "Ana",
		"org": orgID, "app_org": orgID, "env": envID, "mode": "team",
		"iat": f.now.Unix(), "exp": f.now.Add(time.Minute).Unix(), "jti": "jti-" + string(rune('a'+f.jti)), "typ": TicketType,
	}
	if edit != nil {
		edit(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(f.priv)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// query is the forwardAuth query the agent writes for the app.
func query(mode, org string) url.Values {
	return url.Values{"env": {envID}, "org": {org}, "mode": {mode}, "host": {name}, "scheme": {"http"}, "port": {"8088"}}
}

func (f *fixture) verifyReq(mode, org, uri, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/verify?"+query(mode, org).Encode(), nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("X-Forwarded-Uri", uri)
	r.Header.Set("X-Forwarded-Method", "GET")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	return w
}

func (f *fixture) callback(ticket, ret string) *httptest.ResponseRecorder {
	return f.callbackAs("team", orgID, ticket, ret, "http")
}

func (f *fixture) callbackAs(mode, org, ticket, ret, proto string) *httptest.ResponseRecorder {
	q := query(mode, org)
	q.Set("scheme", proto)
	r := httptest.NewRequest(http.MethodGet, "/verify?"+q.Encode(), nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("X-Forwarded-Uri", CallbackPath+"?"+url.Values{"ticket": {ticket}, "return": {ret}}.Encode())
	r.Header.Set("X-Forwarded-Method", "GET")
	w := httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	return w
}

func cookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

func TestCookieRoundTripAndTamper(t *testing.T) {
	c, _ := NewCodec([]byte(strings.Repeat("k", 32)))
	s := Session{Host: host, Env: envID, User: "u", Expires: t0.Add(time.Hour).Unix()}
	v := c.Seal(s)
	if got, err := c.Open(v, t0); err != nil || got != s {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := c.Open(v, t0.Add(2*time.Hour)); err == nil {
		t.Error("an expired cookie opened")
	}
	payload, sig, _ := strings.Cut(v, ".")
	forged := Session{Host: host, Env: envID, User: "admin", Expires: s.Expires}
	raw, _ := json.Marshal(forged)
	if _, err := c.Open(base64.RawURLEncoding.EncodeToString(raw)+"."+sig, t0); err == nil {
		t.Error("a payload with another's signature opened")
	}
	other, _ := NewCodec([]byte(strings.Repeat("z", 32)))
	if _, err := other.Open(v, t0); err == nil {
		t.Error("a cookie sealed with another key opened")
	}
	for _, bad := range []string{"", "x", payload, payload + ".", "." + sig} {
		if _, err := c.Open(bad, t0); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
	if _, err := NewCodec([]byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestTicketVerification(t *testing.T) {
	f := newFixture(t)
	want := Expect{Host: name, Aud: host, Env: envID, Mode: "team", AppOrg: orgID}
	ctx := context.Background()
	verify := func(tok string, w Expect) error { _, err := f.gate.Verifier.Verify(ctx, tok, w); return err }

	if err := verify(f.ticket(nil), want); err != nil {
		t.Fatalf("a good ticket: %v", err)
	}
	// forged signature: signed by another key under the known kid
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	forged := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "http://issuer.test", "aud": host, "sub": "u", "org": orgID, "app_org": orgID,
		"env": envID, "mode": "team", "iat": f.now.Unix(), "exp": f.now.Add(time.Minute).Unix(), "jti": "forged", "typ": TicketType})
	forged.Header["kid"] = "k1"
	fs, _ := forged.SignedString(evil)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "http://issuer.test", "aud": host, "exp": f.now.Add(time.Minute).Unix()})
	hs.Header["kid"] = "k1"
	hss, _ := hs.SignedString([]byte("x"))
	for name, c := range map[string]struct {
		tok  string
		want Expect
	}{
		"forged signature":       {fs, want},
		"HS256 downgrade":        {hss, want},
		"garbage":                {"a.b.c", want},
		"wrong audience":         {f.ticket(func(c jwt.MapClaims) { c["aud"] = "other.apps.test:8088" }), want},
		"served on another host": {f.ticket(nil), Expect{Host: "other.apps.test", Aud: "other.apps.test:8088", Env: envID, Mode: "team", AppOrg: orgID}},
		"wrong issuer":           {f.ticket(func(c jwt.MapClaims) { c["iss"] = "http://evil.test" }), want},
		"expired": {f.ticket(func(c jwt.MapClaims) {
			c["exp"] = f.now.Add(-time.Minute).Unix()
			c["iat"] = f.now.Add(-2 * time.Minute).Unix()
		}), want},
		"no expiry":          {f.ticket(func(c jwt.MapClaims) { delete(c, "exp") }), want},
		"lives too long":     {f.ticket(func(c jwt.MapClaims) { c["exp"] = f.now.Add(time.Hour).Unix() }), want},
		"wrong typ":          {f.ticket(func(c jwt.MapClaims) { c["typ"] = "access" }), want},
		"other mode":         {f.ticket(func(c jwt.MapClaims) { c["mode"] = "organization" }), want},
		"other environment":  {f.ticket(func(c jwt.MapClaims) { c["env"] = "x" }), want},
		"other app org":      {f.ticket(func(c jwt.MapClaims) { c["app_org"] = otherO }), want},
		"team, user outside": {f.ticket(func(c jwt.MapClaims) { c["org"] = otherO }), want},
		"no jti":             {f.ticket(func(c jwt.MapClaims) { delete(c, "jti") }), want},
		"unknown kid": {func() string {
			tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{})
			tk.Header["kid"] = "nope"
			s, _ := tk.SignedString(f.priv)
			return s
		}(), want},
	} {
		if err := verify(c.tok, c.want); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// organization mode accepts a user of another org
	orgWant := Expect{Host: name, Aud: host, Env: envID, Mode: "organization", AppOrg: orgID}
	if err := verify(f.ticket(func(c jwt.MapClaims) { c["mode"] = "organization"; c["org"] = otherO }), orgWant); err != nil {
		t.Errorf("organization mode: %v", err)
	}
}

func TestTicketIsSingleUse(t *testing.T) {
	f := newFixture(t)
	want := Expect{Host: name, Aud: host, Env: envID, Mode: "team", AppOrg: orgID}
	tok := f.ticket(nil)
	if _, err := f.gate.Verifier.Verify(context.Background(), tok, want); err != nil {
		t.Fatal(err)
	}
	if _, err := f.gate.Verifier.Verify(context.Background(), tok, want); err == nil {
		t.Fatal("a replayed ticket was accepted")
	}
}

func TestReplayCacheForgetsExpiredAndBounds(t *testing.T) {
	r := newReplayCache(2)
	if !r.use("a", t0.Add(time.Minute), t0) || r.use("a", t0.Add(time.Minute), t0) {
		t.Fatal("first use only")
	}
	if !r.use("a", t0.Add(3*time.Minute), t0.Add(2*time.Minute)) {
		t.Error("an expired jti is forgotten")
	}
	r.use("b", t0.Add(time.Hour), t0)
	r.use("c", t0.Add(time.Hour), t0)
	if r.order.Len() > 2 {
		t.Errorf("cache grew past its bound: %d", r.order.Len())
	}
}

func TestJWKSCacheAndRateLimit(t *testing.T) {
	f := newFixture(t)
	want := Expect{Host: name, Aud: host, Env: envID, Mode: "team", AppOrg: orgID}
	for i := 0; i < 3; i++ {
		if _, err := f.gate.Verifier.Verify(context.Background(), f.ticket(nil), want); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.fetchs.Load(); n != 1 {
		t.Errorf("keys fetched %d times, want once (cached)", n)
	}
	unknown := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{})
	unknown.Header["kid"] = "rotated"
	s, _ := unknown.SignedString(f.priv)
	for i := 0; i < 5; i++ {
		f.gate.Verifier.Verify(context.Background(), s, want)
	}
	if n := f.fetchs.Load(); n != 1 {
		t.Errorf("unknown kids must not hammer the issuer: %d fetches", n)
	}
	f.now = f.now.Add(time.Minute) // past the rate limit
	f.gate.Verifier.Verify(context.Background(), s, want)
	if n := f.fetchs.Load(); n != 2 {
		t.Errorf("an unknown kid refetches once the gap passed: %d", n)
	}
}

func TestVerifyFlow(t *testing.T) {
	f := newFixture(t)

	// no cookie: GET goes to sign in, with the original URL
	w := f.verifyReq("team", orgID, "/reports?x=1", "")
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc.Host != "issuer.test" || loc.Path != "/access/authorize" ||
		loc.Query().Get("env") != envID || loc.Query().Get("mode") != "team" || loc.Query().Get("return") != "http://"+host+"/reports?x=1" {
		t.Fatalf("redirect to sign in: %d %s", w.Code, w.Header().Get("Location"))
	}
	// other methods are refused, not redirected
	r := httptest.NewRequest(http.MethodGet, "/verify?"+query("team", orgID).Encode(), nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("X-Forwarded-Method", "POST")
	w = httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
		t.Errorf("POST: %d", w.Code)
	}

	// the callback trades a ticket for a cookie and returns to the path
	w = f.callback(f.ticket(nil), "/reports?x=1")
	c := cookieOf(w)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/reports?x=1" || c == nil {
		t.Fatalf("callback: %d %v", w.Code, w.Header())
	}
	if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.Domain != "" || c.Secure {
		t.Errorf("cookie attributes: %+v", c)
	}
	if sec := cookieOf(f.callbackAs("team", orgID, f.ticket(nil), "/", "https")); sec == nil || !sec.Secure {
		t.Error("the cookie is Secure behind https")
	}

	// with the cookie: 200 and identity headers
	w = f.verifyReq("team", orgID, "/", c.Value)
	if w.Code != 200 || w.Header().Get(HeaderUser) != "user-1" || w.Header().Get(HeaderEmail) != "ana@x.test" ||
		w.Header().Get(HeaderName) != "Ana" || w.Header().Get(HeaderOrg) != orgID {
		t.Fatalf("verified: %d %v", w.Code, w.Header())
	}
	// the cookie is bound to the app: other mode / org / env / host, and time
	for name, w := range map[string]*httptest.ResponseRecorder{
		"other mode": f.verifyReq("organization", orgID, "/", c.Value),
		"other org":  f.verifyReq("team", otherO, "/", c.Value),
	} {
		if w.Code == 200 {
			t.Errorf("%s: a cookie opened another app", name)
		}
	}
	// app B's own gate question (query host B, honest X-Forwarded-Host B)
	// with app A's cookie: refused, and sent to sign in for B.
	qb := query("team", orgID)
	qb.Set("host", "other.apps.test")
	r = httptest.NewRequest(http.MethodGet, "/verify?"+qb.Encode(), nil)
	r.Header.Set("X-Forwarded-Host", "other.apps.test:8088")
	r.Header.Set("X-Forwarded-Method", "GET")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	if w.Code == 200 {
		t.Error("a cookie opened another app")
	}
	f.now = f.now.Add(SessionTTL + time.Second)
	if w = f.verifyReq("team", orgID, "/", c.Value); w.Code == 200 {
		t.Error("an expired cookie opened the app")
	}
}

func TestCallbackRefusals(t *testing.T) {
	f := newFixture(t)
	// a bad ticket: 403, no cookie, no leak of the ticket
	bad := f.ticket(func(c jwt.MapClaims) { c["aud"] = "other:1" })
	w := f.callback(bad, "/")
	if w.Code != http.StatusForbidden || cookieOf(w) != nil || strings.Contains(w.Body.String(), bad) {
		t.Errorf("bad ticket: %d %s", w.Code, w.Body)
	}
	// open redirects: anything but a same-host path becomes "/"
	for _, ret := range []string{"//evil.test/x", "https://evil.test/", "http://" + host + "/", "/\\evil.test", "evil", "", "/a\r\nSet-Cookie: x=1", CallbackPath + "?ticket=x"} {
		w := f.callback(f.ticket(nil), ret)
		if loc := w.Header().Get("Location"); w.Code != http.StatusFound || loc != "/" {
			t.Errorf("return %q went to %q", ret, loc)
		}
	}
	// a bad request (missing/unknown mode, host) fails closed
	for _, mode := range []string{"", "public", "x"} {
		if w := f.verifyReq(mode, orgID, "/", ""); w.Code != http.StatusForbidden {
			t.Errorf("mode %q: %d", mode, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/verify?"+query("team", orgID).Encode(), nil)
	r.Header.Set("X-Forwarded-Host", "evil.test/path")
	r.Header.Set("X-Forwarded-Method", "GET")
	w = httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("odd host: %d", w.Code)
	}
	w = httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("healthz: %d", w.Code)
	}
}

func TestForgedHostIsRefused(t *testing.T) {
	f := newFixture(t)
	c := cookieOf(f.callback(f.ticket(nil), "http://"+host+"/"))
	if c == nil {
		t.Fatal("no session")
	}
	// A valid cookie for the app, but the request claims another host (a
	// forged X-Forwarded-Host, or the route of another app): 403.
	for name, xfh := range map[string]string{"other host": "evil.apps.test", "other host and port": "evil.apps.test:8088", "empty": ""} {
		q := query("team", orgID)
		r := httptest.NewRequest(http.MethodGet, "/verify?"+q.Encode(), nil)
		if xfh != "" {
			r.Header.Set("X-Forwarded-Host", xfh)
		}
		r.Header.Set("X-Forwarded-Method", "GET")
		r.AddCookie(c)
		w := httptest.NewRecorder()
		f.gate.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, w.Code)
		}
	}
	// The same host in another case or with another port is the same host.
	r := httptest.NewRequest(http.MethodGet, "/verify?"+query("team", orgID).Encode(), nil)
	r.Header.Set("X-Forwarded-Host", "PORTAL.apps.test:9999")
	r.Header.Set("X-Forwarded-Method", "GET")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("same host, other spelling: %d", w.Code)
	}
	// A query without host or scheme is refused (an old Middleware).
	for _, drop := range []string{"host", "scheme"} {
		q := query("team", orgID)
		q.Del(drop)
		r := httptest.NewRequest(http.MethodGet, "/verify?"+q.Encode(), nil)
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Method", "GET")
		w := httptest.NewRecorder()
		f.gate.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("without %s: %d", drop, w.Code)
		}
	}
	// A missing X-Forwarded-Method is refused, not taken as GET.
	r = httptest.NewRequest(http.MethodGet, "/verify?"+query("team", orgID).Encode(), nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	f.gate.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("no method: %d", w.Code)
	}
}

func TestSealCapsTheCookie(t *testing.T) {
	c, _ := NewCodec(make([]byte, 32))
	v := c.Seal(Session{User: "u", Name: strings.Repeat("ñ", 5000), Email: strings.Repeat("e", 5000), UserOrg: "o", Expires: time.Now().Add(time.Hour).Unix()})
	if len(v) >= MaxCookieValue {
		t.Fatalf("sealed value is %d bytes", len(v))
	}
	if _, err := c.Open(v, time.Now()); err != nil {
		t.Error("a truncated session must still open")
	}
}

func TestCheckURL(t *testing.T) {
	for u, insecure := range map[string]bool{"https://issuer.example.com": false, "http://localhost:8095": false, "http://127.0.0.1:1": false,
		"http://host.k3d.internal:8095/x": false, "http://api.localhost": false, "http://issuer.example.com": true} {
		if err := CheckURL(u, insecure); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"http://issuer.example.com", "ftp://x", "issuer.example.com", "https://u:p@x.test", "http://evil-localhost.com"} {
		if err := CheckURL(u, false); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestEnsureAndLoadKey(t *testing.T) {
	kc := fake.NewSimpleClientset()
	ctx := context.Background()
	if err := EnsureKey(ctx, kc, "odyssey-system", "odyssey-access"); err != nil {
		t.Fatal(err)
	}
	k1, err := LoadKey(ctx, kc, "odyssey-system", "odyssey-access")
	if err != nil || len(k1) != 32 {
		t.Fatalf("load: %d %v", len(k1), err)
	}
	if err := EnsureKey(ctx, kc, "odyssey-system", "odyssey-access"); err != nil {
		t.Fatal(err)
	}
	k2, err := LoadKey(ctx, kc, "odyssey-system", "odyssey-access")
	if err != nil || string(k1) != string(k2) {
		t.Fatal("an existing key must never be replaced")
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := LoadKey(short, fake.NewSimpleClientset(), "odyssey-system", "odyssey-access"); err == nil {
		t.Error("a missing Secret must be an error, not a new key")
	}
}

func TestJWKSFetchIsSharedAndDoesNotFollowRedirects(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.gate.Verifier.key(context.Background(), "k1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.fetchs.Load(); n != 1 {
		t.Errorf("concurrent callers fetched %d times, want 1", n)
	}

	var hit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	redir := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redir.Close()
	v := &Verifier{Issuer: "http://issuer.test", JWKSURL: redir.URL}
	if _, err := v.key(context.Background(), "k1"); err == nil {
		t.Error("a redirecting JWKS URL must fail")
	}
	if hit.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestJWKSHardMaxAge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.gate.Verifier.key(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	f.issuer.Close() // refreshes now fail
	f.now = f.now.Add(30 * time.Minute)
	if _, err := f.gate.Verifier.key(ctx, "k1"); err != nil {
		t.Errorf("a stale key is served while the issuer is down (under the hard limit): %v", err)
	}
	f.now = f.now.Add(31 * time.Minute) // 61 min since the load
	if _, err := f.gate.Verifier.key(ctx, "k1"); err == nil {
		t.Error("a key older than the hard max age must be refused when refreshing fails")
	}
	f.now = f.now.Add(time.Second)
	if _, err := f.gate.Verifier.key(ctx, "k1"); err == nil {
		t.Error("still refused inside the min gap")
	}
}
