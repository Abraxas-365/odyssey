package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// jwksOf fetches the signer's key set through its HTTP handler.
func jwksOf(t *testing.T, s *Signer) map[string]ed25519.PublicKey {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jwks", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "max-age=300" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("jwks: %d %v", w.Code, w.Header())
	}
	var set struct {
		Keys []struct{ Kty, Crv, X, Kid, Use, Alg string } `json:"keys"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Use != "sig" || k.Alg != "EdDSA" {
			t.Errorf("jwk: %+v", k)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			t.Fatalf("x: %v", err)
		}
		out[k.Kid] = x
	}
	return out
}

func TestAssertionOnEvery200(t *testing.T) {
	f := newFixture(t)
	c := cookieOf(f.callback(f.ticket(nil), "/"))
	w := f.verifyReq("team", orgID, "/", c.Value)
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	tok := w.Header().Get(AssertionHeader)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion: %q", tok)
	}
	var hdr map[string]string
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	json.Unmarshal(raw, &hdr)
	pub := sha256.Sum256(f.gate.Signer.priv.Public().(ed25519.PublicKey))
	wantKid := base64.RawURLEncoding.EncodeToString(pub[:])[:16]
	if len(hdr) != 3 || hdr["alg"] != "EdDSA" || hdr["typ"] != "JWT" || hdr["kid"] != wantKid || len(wantKid) != 16 {
		t.Errorf("header: %v (kid %s)", hdr, wantKid)
	}
	keys := jwksOf(t, f.gate.Signer)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(keys[hdr["kid"]], []byte(parts[0]+"."+parts[1]), sig) {
		t.Error("the signature does not verify against the JWKS key")
	}
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(raw, &claims)
	want := map[string]any{
		"iss": "odicea-gate", "aud": host, "sub": "user-1", "email": "ana@x.test", "name": "Ana", "org": orgID, "app_org": orgID,
		"env": envID, "mode": "team", "typ": "odicea-assertion",
		"iat": float64(f.now.Unix()), "exp": float64(f.now.Add(120 * time.Second).Unix()),
	}
	if len(claims) != len(want) {
		t.Errorf("claims: %v", claims)
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("claim %s = %v, want %v", k, claims[k], v)
		}
	}
	// the header is the gate's: another assertion is a fresh signature, other time
	f.now = f.now.Add(time.Minute)
	if w2 := f.verifyReq("team", orgID, "/", c.Value); w2.Header().Get(AssertionHeader) == tok || w2.Code != 200 {
		t.Error("an assertion is signed per request")
	}
}

func TestNoAssertionWithout200(t *testing.T) {
	f := newFixture(t)
	c := cookieOf(f.callback(f.ticket(nil), "/"))
	for name, w := range map[string]*httptest.ResponseRecorder{
		"no cookie (302)":   f.verifyReq("team", orgID, "/", ""),
		"other org (302)":   f.verifyReq("team", otherO, "/", c.Value),
		"bad cookie (302)":  f.verifyReq("team", orgID, "/", "x.y"),
		"callback 302":      f.callback(f.ticket(nil), "/"),
		"callback 403":      f.callback("garbage", "/"),
		"bad request (403)": f.verifyReq("public", orgID, "/", c.Value),
		"post without cookie": func() *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodGet, "/verify?"+query("team", orgID).Encode(), nil)
			r.Header.Set("X-Forwarded-Host", host)
			r.Header.Set("X-Forwarded-Method", "POST")
			w := httptest.NewRecorder()
			f.gate.Handler().ServeHTTP(w, r)
			return w
		}(),
	} {
		if w.Code == 200 || w.Header().Get(AssertionHeader) != "" {
			t.Errorf("%s: %d %v", name, w.Code, w.Header())
		}
	}
	// a gate with no signer never answers 200
	f.gate.Signer = nil
	if w := f.verifyReq("team", orgID, "/", c.Value); w.Code == 200 || w.Header().Get(HeaderUser) != "" {
		t.Errorf("no signer: %d %v", w.Code, w.Header())
	}
}

func TestJWKSHandlerAndRotation(t *testing.T) {
	cur, prev := make([]byte, 32), make([]byte, 32)
	rand.Read(cur)
	rand.Read(prev)
	s, err := NewSigner(cur, prev)
	if err != nil {
		t.Fatal(err)
	}
	keys := jwksOf(t, s)
	prevPub := ed25519.NewKeyFromSeed(prev).Public().(ed25519.PublicKey)
	if len(keys) != 2 || !keys[s.KID()].Equal(ed25519.NewKeyFromSeed(cur).Public()) || !keys[KeyID(prevPub)].Equal(prevPub) || KeyID(prevPub) == s.KID() {
		t.Errorf("keys: %v", keys)
	}
	// only the current key signs
	hdr, _, _ := strings.Cut(s.Sign(Session{User: "u"}, "a", t0), ".")
	raw, _ := base64.RawURLEncoding.DecodeString(hdr)
	if !strings.Contains(string(raw), s.KID()) {
		t.Errorf("signed with %s", raw)
	}
	// the same key as previous is published once
	if s2, _ := NewSigner(cur, cur); len(jwksOf(t, s2)) != 1 {
		t.Error("a duplicated key is published twice")
	}
	// only GET and HEAD
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(m, "/jwks", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/jwks", nil))
	if w.Code != 200 {
		t.Errorf("HEAD: %d", w.Code)
	}
	// nothing else is served on the keys port
	for _, p := range []string{"/", "/verify", "/healthz"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestSigningKeyLoading(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset()
	if err := EnsureSigningKey(ctx, kc, "odyssey-system", SigningSecretName); err != nil {
		t.Fatal(err)
	}
	s1, err := LoadSigner(ctx, kc, "odyssey-system", SigningSecretName)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureSigningKey(ctx, kc, "odyssey-system", SigningSecretName); err != nil {
		t.Fatal(err)
	}
	s2, _ := LoadSigner(ctx, kc, "odyssey-system", SigningSecretName)
	if s1.KID() != s2.KID() {
		t.Error("an existing signing key must never be replaced")
	}
	// short seeds, missing key, bad previous
	for name, data := range map[string]map[string][]byte{
		"short seed":     {AssertionKeyKey: []byte("short")},
		"long seed":      {AssertionKeyKey: make([]byte, 64)},
		"no key":         {"other": make([]byte, 32)},
		"short previous": {AssertionKeyKey: make([]byte, 32), AssertionKeyPreviousKey: []byte("x")},
	} {
		kc := fake.NewSimpleClientset()
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}, Data: data}
		if _, err := kc.CoreV1().Secrets("ns").Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSigner(ctx, kc, "ns", "s"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewSigner(nil, nil); err == nil {
		t.Error("nil seed accepted")
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := LoadSigner(short, fake.NewSimpleClientset(), "ns", "s"); err == nil {
		t.Error("a missing Secret must be an error, not a new key")
	}
}
