package access

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Signer signs the identity assertions (X-Odicea-Assertion) with the gate's
// own Ed25519 key and publishes its public keys (the current one, and the
// previous one during a rotation). Ed25519 signing costs tens of microseconds:
// it is done on every /verify 200.
type Signer struct {
	priv   ed25519.PrivateKey
	kid    string
	header string // base64url of the JWT header, fixed per key
	jwks   []byte
}

// KeyID is the kid of a public key: base64url(sha256(pub))[:16], unpadded.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

// NewSigner returns a Signer for the Ed25519 seed (exactly 32 bytes). previous
// (nil: none) is the seed of the key being rotated out: its public half stays
// in the JWKS, it never signs.
func NewSigner(seed, previous []byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("the assertion key must be an Ed25519 seed of %d bytes (got %d)", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	s := &Signer{priv: priv, kid: KeyID(pub)}
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": s.kid})
	s.header = base64.RawURLEncoding.EncodeToString(hdr)

	type jwk struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
	}
	keys := []jwk{{"OKP", "Ed25519", base64.RawURLEncoding.EncodeToString(pub), s.kid, "sig", "EdDSA"}}
	if len(previous) != 0 {
		if len(previous) != ed25519.SeedSize {
			return nil, fmt.Errorf("the previous assertion key must be an Ed25519 seed of %d bytes (got %d)", ed25519.SeedSize, len(previous))
		}
		pp := ed25519.NewKeyFromSeed(previous).Public().(ed25519.PublicKey)
		if kid := KeyID(pp); kid != s.kid {
			keys = append(keys, jwk{"OKP", "Ed25519", base64.RawURLEncoding.EncodeToString(pp), kid, "sig", "EdDSA"})
		}
	}
	s.jwks, _ = json.Marshal(map[string]any{"keys": keys})
	return s, nil
}

// KID is the id of the key that signs.
func (s *Signer) KID() string { return s.kid }

// JWKS is the published key set.
func (s *Signer) JWKS() []byte { return append([]byte(nil), s.jwks...) }

// Handler serves GET|HEAD /jwks (and nothing else) with the key set.
func (s *Signer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "max-age=300")
		_, _ = w.Write(s.jwks)
	})
	return mux
}

// assertionClaims are the claims of the assertion (see the package doc).
type assertionClaims struct {
	Iss    string `json:"iss"`
	Aud    string `json:"aud"`
	Sub    string `json:"sub"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Org    string `json:"org"`
	AppOrg string `json:"app_org"`
	Env    string `json:"env"`
	Mode   string `json:"mode"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
	Typ    string `json:"typ"`
}

// Sign returns the assertion for session s on the app whose ticket aud is aud
// (host[:port]), valid AssertionTTL from now.
func (s *Signer) Sign(sess Session, aud string, now time.Time) string {
	body, _ := json.Marshal(assertionClaims{
		Iss: AssertionIssuer, Aud: aud, Sub: sess.User, Email: sess.Email, Name: sess.Name, Org: sess.UserOrg,
		AppOrg: sess.AppOrg, Env: sess.Env, Mode: sess.Mode,
		Iat: now.Unix(), Exp: now.Add(AssertionTTL).Unix(), Typ: AssertionType,
	})
	signing := s.header + "." + base64.RawURLEncoding.EncodeToString(body)
	return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(signing)))
}

var errNoSigner = errors.New("the gate has no assertion key")
