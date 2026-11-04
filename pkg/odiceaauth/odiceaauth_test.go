package odiceaauth_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/odiceaauth"
)

const aud = "app.apps.example.com"

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

type signer struct {
	kid  string
	priv ed25519.PrivateKey
}

func newSigner(seed byte) signer {
	seedBytes := make([]byte, ed25519.SeedSize)
	seedBytes[0] = seed
	priv := ed25519.NewKeyFromSeed(seedBytes)
	sum := sha256.Sum256(priv.Public().(ed25519.PublicKey))
	return signer{kid: base64.RawURLEncoding.EncodeToString(sum[:])[:16], priv: priv}
}

func (s signer) jwk() map[string]string {
	return map[string]string{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": s.kid,
		"x": base64.RawURLEncoding.EncodeToString(s.priv.Public().(ed25519.PublicKey)),
	}
}

func b64(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// sign builds a token from arbitrary header and claims.
func (s signer) sign(header, claims map[string]any) string {
	in := b64(header) + "." + b64(claims)
	return in + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(in)))
}

func (s signer) header() map[string]any {
	return map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": s.kid}
}

func claims(now time.Time, edit func(map[string]any)) map[string]any {
	c := map[string]any{
		"iss": odiceaauth.Issuer, "aud": aud, "sub": "user-1", "email": "ana@acme.test",
		"name": "Ana", "org": "org-1", "app_org": "org-1", "env": "production", "mode": "team",
		"iat": now.Unix(), "exp": now.Add(120 * time.Second).Unix(), "typ": odiceaauth.Type,
	}
	if edit != nil {
		edit(c)
	}
	return c
}

// jwks serves a mutable key set and counts requests.
type jwks struct {
	*httptest.Server
	mu    sync.Mutex
	keys  []signer
	down  bool
	calls atomic.Int32
}

func newJWKS(t *testing.T, keys ...signer) *jwks {
	t.Helper()
	j := &jwks{keys: keys}
	j.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.calls.Add(1)
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var set []map[string]string
		for _, k := range j.keys {
			set = append(set, k.jwk())
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": set})
	}))
	t.Cleanup(j.Close)
	return j
}

func (j *jwks) set(down bool, keys ...signer) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.down, j.keys = down, keys
}

// clock is a movable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func verifier(j *jwks, c *clock) *odiceaauth.Verifier {
	return &odiceaauth.Verifier{JWKSURL: j.URL, Audience: aud, Now: c.now}
}

func TestVerifyValid(t *testing.T) {
	s := newSigner(1)
	c := &clock{t: t0}
	id, err := verifier(newJWKS(t, s), c).Verify(context.Background(), s.sign(s.header(), claims(t0, nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := odiceaauth.Identity{
		Subject: "user-1", Email: "ana@acme.test", Name: "Ana", Org: "org-1", AppOrg: "org-1",
		Env: "production", Mode: "team", IssuedAt: t0, Expires: t0.Add(120 * time.Second),
	}
	if id != want {
		t.Fatalf("identity %+v, want %+v", id, want)
	}
}

func TestVerifyRefuses(t *testing.T) {
	s, other := newSigner(1), newSigner(2)
	cases := map[string]struct {
		header func() map[string]any
		edit   func(map[string]any)
		signer *signer // default s
		token  string  // overrides everything
	}{
		"wrong audience":   {edit: func(c map[string]any) { c["aud"] = "other.apps.example.com" }},
		"audience as list": {edit: func(c map[string]any) { c["aud"] = []string{aud} }},
		"no audience":      {edit: func(c map[string]any) { delete(c, "aud") }},
		"wrong issuer":     {edit: func(c map[string]any) { c["iss"] = "someone-else" }},
		"no issuer":        {edit: func(c map[string]any) { delete(c, "iss") }},
		"expired": {edit: func(c map[string]any) {
			c["iat"], c["exp"] = t0.Add(-10*time.Minute).Unix(), t0.Add(-8*time.Minute).Unix()
		}},
		"just past leeway": {edit: func(c map[string]any) {
			c["iat"], c["exp"] = t0.Add(-150*time.Second).Unix(), t0.Add(-31*time.Second).Unix()
		}},
		"issued in future": {edit: func(c map[string]any) {
			c["iat"], c["exp"] = t0.Add(2*time.Minute).Unix(), t0.Add(4*time.Minute).Unix()
		}},
		"no exp":               {edit: func(c map[string]any) { delete(c, "exp") }},
		"no iat":               {edit: func(c map[string]any) { delete(c, "iat") }},
		"lives too long":       {edit: func(c map[string]any) { c["exp"] = t0.Add(301 * time.Second).Unix() }},
		"exp before iat":       {edit: func(c map[string]any) { c["exp"] = t0.Add(-time.Second).Unix() }},
		"wrong claim typ":      {edit: func(c map[string]any) { c["typ"] = "odicea-ticket" }},
		"no claim typ":         {edit: func(c map[string]any) { delete(c, "typ") }},
		"empty sub":            {edit: func(c map[string]any) { c["sub"] = "" }},
		"no sub":               {edit: func(c map[string]any) { delete(c, "sub") }},
		"header typ not JWT":   {header: func() map[string]any { return map[string]any{"alg": "EdDSA", "typ": "at+jwt", "kid": s.kid} }},
		"no header typ":        {header: func() map[string]any { return map[string]any{"alg": "EdDSA", "kid": s.kid} }},
		"alg none":             {header: func() map[string]any { return map[string]any{"alg": "none", "typ": "JWT", "kid": s.kid} }},
		"alg HS256":            {header: func() map[string]any { return map[string]any{"alg": "HS256", "typ": "JWT", "kid": s.kid} }},
		"alg RS256":            {header: func() map[string]any { return map[string]any{"alg": "RS256", "typ": "JWT", "kid": s.kid} }},
		"no kid":               {header: func() map[string]any { return map[string]any{"alg": "EdDSA", "typ": "JWT"} }},
		"unknown kid":          {header: func() map[string]any { return map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "nope"} }},
		"signed by a stranger": {signer: &other, header: func() map[string]any { return map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": s.kid} }},
		"empty":                {token: ""},
		"garbage":              {token: "not-a-jwt"},
		"two parts":            {token: "a.b"},
		"four parts":           {token: "a.b.c.d"},
		"unsigned (alg none)":  {token: b64(map[string]any{"alg": "none", "typ": "JWT", "kid": s.kid}) + "." + b64(claims(t0, nil)) + "."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sg := s
			if tc.signer != nil {
				sg = *tc.signer
			}
			h := sg.header()
			if tc.header != nil {
				h = tc.header()
			}
			token := tc.token
			if token == "" && name != "empty" {
				token = sg.sign(h, claims(t0, tc.edit))
			}
			v := verifier(newJWKS(t, s), &clock{t: t0})
			if id, err := v.Verify(context.Background(), token); err == nil {
				t.Fatalf("accepted: %+v", id)
			}
		})
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	s := newSigner(1)
	v := verifier(newJWKS(t, s), &clock{t: t0})
	parts := strings.Split(s.sign(s.header(), claims(t0, nil)), ".")
	parts[1] = b64(claims(t0, func(c map[string]any) { c["email"] = "boss@acme.test" }))
	if _, err := v.Verify(context.Background(), strings.Join(parts, ".")); err == nil {
		t.Fatal("accepted a payload that was not signed")
	}
}

func TestLeeway(t *testing.T) {
	s := newSigner(1)
	v := verifier(newJWKS(t, s), &clock{t: t0})
	ok := map[string]func(map[string]any){
		"expired 20s ago": func(c map[string]any) {
			c["iat"], c["exp"] = t0.Add(-140*time.Second).Unix(), t0.Add(-20*time.Second).Unix()
		},
		"issued 20s from now": func(c map[string]any) {
			c["iat"], c["exp"] = t0.Add(20*time.Second).Unix(), t0.Add(140*time.Second).Unix()
		},
		"lives exactly 300s": func(c map[string]any) { c["exp"] = t0.Add(300 * time.Second).Unix() },
	}
	for name, edit := range ok {
		if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, edit))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCustomIssuer(t *testing.T) {
	s := newSigner(1)
	v := verifier(newJWKS(t, s), &clock{t: t0})
	v.Issuer = "other-gate"
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, nil))); err == nil {
		t.Fatal("accepted the default issuer while another is expected")
	}
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, func(c map[string]any) { c["iss"] = "other-gate" }))); err != nil {
		t.Fatal(err)
	}
}

func TestVerifierNeedsConfiguration(t *testing.T) {
	s := newSigner(1)
	tok := s.sign(s.header(), claims(t0, nil))
	for name, v := range map[string]*odiceaauth.Verifier{
		"no audience": {JWKSURL: "http://127.0.0.1:1/jwks", Now: func() time.Time { return t0 }},
		"no jwks url": {Audience: aud, Now: func() time.Time { return t0 }},
	} {
		if _, err := v.Verify(context.Background(), tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUnknownKidRefetchesRateLimited(t *testing.T) {
	old, fresh := newSigner(1), newSigner(2)
	j := newJWKS(t, old)
	c := &clock{t: t0}
	v := verifier(j, c)
	ctx := context.Background()
	if _, err := v.Verify(ctx, old.sign(old.header(), claims(c.now(), nil))); err != nil {
		t.Fatal(err)
	}
	if j.calls.Load() != 1 {
		t.Fatalf("calls %d", j.calls.Load())
	}
	// the issuer rotates; a token with the new kid arrives right away: the
	// last fetch was just now, so it is refused without calling the issuer
	j.set(false, old, fresh)
	c.add(2 * time.Second)
	if _, err := v.Verify(ctx, fresh.sign(fresh.header(), claims(c.now(), nil))); err == nil {
		t.Fatal("accepted an unknown kid inside the minimum gap")
	}
	if j.calls.Load() != 1 {
		t.Fatalf("refetched inside the gap: %d calls", j.calls.Load())
	}
	// garbage kids hammered after the gap cause one fetch per gap, not one each
	c.add(10 * time.Second)
	for i := 0; i < 20; i++ {
		bogus := map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "bogus"}
		v.Verify(ctx, old.sign(bogus, claims(c.now(), nil)))
	}
	if j.calls.Load() != 2 {
		t.Fatalf("20 unknown kids made %d calls, want 1 refetch", j.calls.Load()-1)
	}
	// ...and the new key is now known
	if _, err := v.Verify(ctx, fresh.sign(fresh.header(), claims(c.now(), nil))); err != nil {
		t.Fatalf("new key after the refetch: %v", err)
	}
	if j.calls.Load() != 2 {
		t.Fatalf("calls %d", j.calls.Load())
	}
}

func TestKnownKidsAreCachedForTenMinutes(t *testing.T) {
	s := newSigner(1)
	j := newJWKS(t, s)
	c := &clock{t: t0}
	v := verifier(j, c)
	for i := 0; i < 5; i++ {
		c.add(time.Minute)
		if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(c.now(), nil))); err != nil {
			t.Fatal(err)
		}
	}
	if j.calls.Load() != 1 {
		t.Fatalf("%d fetches in 5 minutes", j.calls.Load())
	}
	c.add(7 * time.Minute) // loaded at +1m, now +12m: past the 10 minutes
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(c.now(), nil))); err != nil {
		t.Fatal(err)
	}
	if j.calls.Load() != 2 {
		t.Fatalf("%d fetches after the max age, want 2", j.calls.Load())
	}
}

func TestKeyRotationKeepsThePreviousKey(t *testing.T) {
	old, fresh := newSigner(1), newSigner(2)
	j := newJWKS(t, old, fresh) // rotation: both published
	c := &clock{t: t0}
	v := verifier(j, c)
	ctx := context.Background()
	for _, s := range []signer{old, fresh} {
		if _, err := v.Verify(ctx, s.sign(s.header(), claims(c.now(), nil))); err != nil {
			t.Fatalf("kid %s: %v", s.kid, err)
		}
	}
	if j.calls.Load() != 1 {
		t.Fatalf("calls %d", j.calls.Load())
	}
	// the previous key is retired: after the cache expires it stops verifying
	j.set(false, fresh)
	c.add(11 * time.Minute)
	if _, err := v.Verify(ctx, fresh.sign(fresh.header(), claims(c.now(), nil))); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, old.sign(old.header(), claims(c.now(), nil))); err == nil {
		t.Fatal("a retired key still verifies")
	}
}

func TestIssuerDownServesCacheUntilTheHardMaxAge(t *testing.T) {
	s := newSigner(1)
	j := newJWKS(t, s)
	c := &clock{t: t0}
	v := verifier(j, c)
	ctx := context.Background()
	if _, err := v.Verify(ctx, s.sign(s.header(), claims(c.now(), nil))); err != nil {
		t.Fatal(err)
	}
	j.set(true) // down
	for _, after := range []time.Duration{15 * time.Minute, 30 * time.Minute, 59 * time.Minute} {
		c.t = t0.Add(after)
		if _, err := v.Verify(ctx, s.sign(s.header(), claims(c.now(), nil))); err != nil {
			t.Fatalf("at +%s with the issuer down: %v", after, err)
		}
	}
	c.t = t0.Add(time.Hour + time.Second)
	if _, err := v.Verify(ctx, s.sign(s.header(), claims(c.now(), nil))); err == nil {
		t.Fatal("served a key past the hard max age")
	}
	// it recovers by itself
	j.set(false, s)
	c.add(time.Minute)
	if _, err := v.Verify(ctx, s.sign(s.header(), claims(c.now(), nil))); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestIssuerDownFromTheStart(t *testing.T) {
	s := newSigner(1)
	j := newJWKS(t, s)
	j.set(true)
	v := verifier(j, &clock{t: t0})
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, nil))); err == nil {
		t.Fatal("verified without ever having keys")
	}
}

func TestFetchFollowsNoRedirects(t *testing.T) {
	s := newSigner(1)
	real := newJWKS(t, s)
	redirect := httptest.NewServer(http.RedirectHandler(real.URL, http.StatusFound))
	defer redirect.Close()
	v := &odiceaauth.Verifier{JWKSURL: redirect.URL, Audience: aud, Now: func() time.Time { return t0 }}
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, nil))); err == nil {
		t.Fatal("followed a redirect to fetch keys")
	}
	if real.calls.Load() != 0 {
		t.Fatal("the redirect target was called")
	}
}

func TestOneFetchAtATime(t *testing.T) {
	s := newSigner(1)
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{s.jwk()}})
	}))
	defer srv.Close()
	v := &odiceaauth.Verifier{JWKSURL: srv.URL, Audience: aud, Now: func() time.Time { return t0 }}
	tok := s.sign(s.header(), claims(t0, nil))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verify(context.Background(), tok)
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d concurrent fetches, want 1", calls.Load())
	}
}

func TestFetchSkipsUnusableKeys(t *testing.T) {
	s := newSigner(1)
	bad := []map[string]string{
		{"kty": "RSA", "kid": "rsa", "x": "AAAA"},
		{"kty": "OKP", "crv": "Ed25519", "kid": "short", "x": "AAAA"},
		{"kty": "OKP", "crv": "Ed25519", "kid": "enc", "use": "enc", "x": s.jwk()["x"]},
		{"kty": "OKP", "crv": "Ed25519", "kid": "", "x": s.jwk()["x"]},
		s.jwk(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": bad})
	}))
	defer srv.Close()
	v := &odiceaauth.Verifier{JWKSURL: srv.URL, Audience: aud, Now: func() time.Time { return t0 }}
	if _, err := v.Verify(context.Background(), s.sign(s.header(), claims(t0, nil))); err != nil {
		t.Fatal(err)
	}
	h := s.header()
	h["kid"] = "enc"
	if _, err := v.Verify(context.Background(), s.sign(h, claims(t0, nil))); err == nil {
		t.Fatal("used a key whose use is not sig")
	}
}

func TestRequire(t *testing.T) {
	s := newSigner(1)
	c := &clock{t: t0}
	v := verifier(newJWKS(t, s), c)
	var got odiceaauth.Identity
	var ok bool
	var reached int
	h := v.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		got, ok = odiceaauth.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	do := func(header, value string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do(odiceaauth.Header, s.sign(s.header(), claims(t0, nil))); w.Code != http.StatusNoContent || !ok || got.Subject != "user-1" || got.Email != "ana@acme.test" {
		t.Fatalf("valid: %d ok=%v %+v", w.Code, ok, got)
	}
	reached = 0
	// a forged identity header alone is never enough
	for name, w := range map[string]*httptest.ResponseRecorder{
		"no header":      do("", ""),
		"email only":     do("X-Odicea-Email", "ana@acme.test"),
		"garbage":        do(odiceaauth.Header, "x.y.z"),
		"wrong audience": do(odiceaauth.Header, s.sign(s.header(), claims(t0, func(c map[string]any) { c["aud"] = "x" }))),
	} {
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: WWW-Authenticate set", name)
		}
		if strings.Contains(w.Body.String(), "signature") || strings.Contains(w.Body.String(), "odiceaauth") {
			t.Errorf("%s: body leaks the reason: %q", name, w.Body.String())
		}
	}
	if reached != 0 {
		t.Fatal("next ran for a refused request")
	}
	if _, ok := odiceaauth.FromContext(context.Background()); ok {
		t.Fatal("identity in an empty context")
	}
}

// A valid assertion paired with forged display headers: next sees the
// verified values, never the forged ones (nor their underscore spellings).
func TestRequireReplacesDisplayHeaders(t *testing.T) {
	s := newSigner(1)
	v := verifier(newJWKS(t, s), &clock{t: t0})
	var seen http.Header
	h := v.Require(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Header }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(odiceaauth.Header, s.sign(s.header(), claims(t0, nil)))
	r.Header.Set("X-Odicea-Email", "boss@acme.test")
	r.Header.Set("X-Odicea-User", "someone-else")
	r.Header["X_Odicea_Email"] = []string{"boss@acme.test"}
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen.Get("X-Odicea-Email") != "ana@acme.test" || seen.Get("X-Odicea-User") != "user-1" {
		t.Errorf("display headers not replaced: %v", seen)
	}
	if _, ok := seen["X_Odicea_Email"]; ok {
		t.Error("underscore spelling kept")
	}
	if r.Header.Get("X-Odicea-Email") != "boss@acme.test" {
		t.Error("the caller's request was modified (headers must be cloned)")
	}
}

func TestRequireCustomHeader(t *testing.T) {
	s := newSigner(1)
	v := verifier(newJWKS(t, s), &clock{t: t0})
	v.HeaderName = "X-Custom"
	h := v.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Custom", s.sign(s.header(), claims(t0, nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("custom header: %d", w.Code)
	}
}

func TestFromEnv(t *testing.T) {
	set := func(jwksURL, issuer, audience, header string) {
		t.Setenv(odiceaauth.EnvJWKSURL, jwksURL)
		t.Setenv(odiceaauth.EnvIssuer, issuer)
		t.Setenv(odiceaauth.EnvAudience, audience)
		t.Setenv(odiceaauth.EnvHeader, header)
	}
	set("", "", "", "")
	if v, err := odiceaauth.FromEnv(); err == nil || v != nil {
		t.Fatalf("a public app (no variables) got a verifier: %v %v", v, err)
	}
	set("http://gate/jwks", "odicea-gate", "", "X-Odicea-Assertion")
	if _, err := odiceaauth.FromEnv(); err == nil || !strings.Contains(err.Error(), odiceaauth.EnvAudience) {
		t.Fatalf("no audience: %v", err)
	}
	set("", "odicea-gate", aud, "X-Odicea-Assertion")
	if _, err := odiceaauth.FromEnv(); err == nil || !strings.Contains(err.Error(), odiceaauth.EnvJWKSURL) {
		t.Fatalf("no jwks url: %v", err)
	}
	set("http://gate/jwks", "odicea-gate", aud, "X-Odicea-Assertion")
	v, err := odiceaauth.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if v.JWKSURL != "http://gate/jwks" || v.Issuer != "odicea-gate" || v.Audience != aud || v.HeaderName != "X-Odicea-Assertion" {
		t.Fatalf("%+v", v)
	}
	// issuer and header are optional: they default to the contract's
	set("http://gate/jwks", "", aud, "")
	if v, err = odiceaauth.FromEnv(); err != nil {
		t.Fatal(err)
	}
	s := newSigner(1)
	j := newJWKS(t, s)
	v.JWKSURL, v.Now = j.URL, func() time.Time { return t0 }
	h := v.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(odiceaauth.Header, s.sign(s.header(), claims(t0, nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("defaults: %d", w.Code)
	}
}

func TestConstants(t *testing.T) {
	if odiceaauth.Header != "X-Odicea-Assertion" || odiceaauth.Issuer != "odicea-gate" || odiceaauth.Type != "odicea-assertion" ||
		odiceaauth.EnvJWKSURL != "ODICEA_AUTH_JWKS_URL" || odiceaauth.EnvIssuer != "ODICEA_AUTH_ISSUER" ||
		odiceaauth.EnvAudience != "ODICEA_AUTH_AUDIENCE" || odiceaauth.EnvHeader != "ODICEA_AUTH_HEADER" {
		t.Fatal("the contract's names changed")
	}
}
