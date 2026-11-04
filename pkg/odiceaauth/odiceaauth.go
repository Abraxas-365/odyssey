// Package odiceaauth lets an app running on Odicea know, with cryptographic
// proof, who is using it. It needs nothing but the standard library and talks
// to no Odicea service except the key set (JWKS) the sign-in gate publishes.
//
// # Why
//
// For an app whose access is "team" or "organization" the sign-in gate
// (Odicea's forwardAuth in front of the app) adds headers to each request:
//
//	X-Odicea-User, X-Odicea-Email, X-Odicea-Name, X-Odicea-Org
//
// Those are plain text. They are display-only: a pod of the cluster that calls
// the app directly, skipping the gate, can send any value. When the app
// decides permissions by who the user is, it must verify the assertion
// instead, and never trust X-Odicea-Email alone.
//
// # The assertion
//
// The gate adds X-Odicea-Assertion to every request it lets through: a JWT
// signed (EdDSA, Ed25519) by the gate, valid for 2 minutes and made per
// request (there is no jti: the app may refuse repeats if it cares).
//
// Header: {"alg":"EdDSA","typ":"JWT","kid":"<key id>"}.
//
//	claim    meaning
//	iss      always "odicea-gate"
//	aud      host[:port] of this app, as users reach it (an assertion for app
//	         A is useless to app B)
//	sub      the user's id (never empty)
//	email    the user's email
//	name     the user's name
//	org      the user's organization
//	app_org  the organization that owns the app
//	env      the environment (e.g. production)
//	mode     the app's access mode: team or organization
//	iat, exp issued-at and expiry (seconds); exp-iat is at most 300
//	typ      always "odicea-assertion" (a claim, distinct from the header typ)
//
// [Verifier.Verify] checks: alg is EdDSA, header typ is JWT, claim typ,
// iss and aud (a single string) match, iat/exp are valid with 30 s of clock
// leeway and a life of at most 5 minutes, sub is set, and the signature
// verifies with the key named by kid.
//
// # Where the keys come from
//
// The gate serves its public keys as a JWK set (OKP/Ed25519, use=sig,
// alg=EdDSA) inside the cluster; while a key is being rotated both the new
// and the previous one are listed. Odicea injects into the app, only when it
// is team or organization:
//
//	ODICEA_AUTH_JWKS_URL   where to fetch the key set
//	ODICEA_AUTH_ISSUER     the expected iss (odicea-gate)
//	ODICEA_AUTH_AUDIENCE   the expected aud (this app's host[:port])
//	ODICEA_AUTH_HEADER     the header carrying the assertion
//
// A public app receives none of them: [FromEnv] then returns an error and the
// app must treat that as "no Odicea login" explicitly (e.g. local
// development), never as "accept everything".
//
// The key set is cached: at most 10 minutes; an unknown kid triggers a
// refetch, at most once every 10 seconds; if the gate cannot be reached, keys
// already cached are used for at most 1 hour in total and then refused.
// Fetches take 5 seconds at most, follow no redirects and run one at a time.
//
// # Use
//
//	v, err := odiceaauth.FromEnv()
//	if err != nil { /* not on Odicea (public or local): decide explicitly */ }
//	http.ListenAndServe(addr, v.Require(mux))
//
//	// inside a handler
//	id, _ := odiceaauth.FromContext(r.Context())
package odiceaauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// Header is the request header carrying the assertion.
	Header = "X-Odicea-Assertion"
	// Issuer is the iss claim of every assertion.
	Issuer = "odicea-gate"
	// Type is the typ claim of every assertion.
	Type = "odicea-assertion"

	// Environment variables Odicea injects into team/organization apps.
	EnvJWKSURL  = "ODICEA_AUTH_JWKS_URL"
	EnvIssuer   = "ODICEA_AUTH_ISSUER"
	EnvAudience = "ODICEA_AUTH_AUDIENCE"
	EnvHeader   = "ODICEA_AUTH_HEADER"
)

const (
	maxLife        = 5 * time.Minute  // exp - iat accepted
	leeway         = 30 * time.Second // clock skew
	jwksMaxAge     = 10 * time.Minute // keys are refetched after this
	jwksMinGap     = 10 * time.Second // never refetch more often than this
	jwksHardMaxAge = time.Hour        // cached keys are refused after this when refreshing fails
	fetchTimeout   = 5 * time.Second
	jwksMaxBody    = 1 << 20
	maxTokenSize   = 8 << 10
)

// Identity is the verified user of a request and the app they opened.
type Identity struct {
	Subject string // the user's id
	Email   string
	Name    string
	Org     string // the user's organization
	AppOrg  string // the organization that owns the app
	Env     string
	Mode    string // team or organization
	// IssuedAt and Expires bound the assertion.
	IssuedAt, Expires time.Time
}

// Verifier checks assertions against the gate's published keys. Build it once
// (FromEnv) and share it: it caches the key set. Do not copy it after use.
type Verifier struct {
	// JWKSURL is where the gate's key set is fetched. Required.
	JWKSURL string
	// Issuer is the expected iss; empty means Issuer ("odicea-gate").
	Issuer string
	// Audience is the expected aud: this app's host[:port]. Required.
	Audience string
	// HeaderName is the request header Require reads; empty means Header.
	HeaderName string
	// HTTPClient fetches the key set. The default times out after 5 seconds
	// and follows no redirects. Set your own only if you need a proxy or TLS
	// settings.
	HTTPClient *http.Client
	// Now replaces time.Now (tests).
	Now func() time.Time

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time // last attempt, successful or not
	loadedAt  time.Time // last success
	inflight  *fetchCall
}

// FromEnv builds a Verifier from the ODICEA_AUTH_* variables. It fails when
// the key set URL or the audience is missing: the app is public or does not
// run on Odicea, and the caller must decide what that means.
func FromEnv() (*Verifier, error) {
	v := &Verifier{
		JWKSURL:    strings.TrimSpace(os.Getenv(EnvJWKSURL)),
		Issuer:     strings.TrimSpace(os.Getenv(EnvIssuer)),
		Audience:   strings.TrimSpace(os.Getenv(EnvAudience)),
		HeaderName: strings.TrimSpace(os.Getenv(EnvHeader)),
	}
	var missing []string
	if v.JWKSURL == "" {
		missing = append(missing, EnvJWKSURL)
	}
	if v.Audience == "" {
		missing = append(missing, EnvAudience)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("odiceaauth: %s not set: this app is public or does not run on Odicea with a login", strings.Join(missing, ", "))
	}
	return v, nil
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) issuer() string {
	if v.Issuer != "" {
		return v.Issuer
	}
	return Issuer
}

func (v *Verifier) header() string {
	if v.HeaderName != "" {
		return v.HeaderName
	}
	return Header
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type claims struct {
	Iss    string          `json:"iss"`
	Aud    json.RawMessage `json:"aud"`
	Sub    string          `json:"sub"`
	Email  string          `json:"email"`
	Name   string          `json:"name"`
	Org    string          `json:"org"`
	AppOrg string          `json:"app_org"`
	Env    string          `json:"env"`
	Mode   string          `json:"mode"`
	Iat    *float64        `json:"iat"`
	Exp    *float64        `json:"exp"`
	Typ    string          `json:"typ"`
}

func unix(f float64) time.Time {
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// Verify checks token (the value of the assertion header) and returns who it
// says the user is. Every failure is an error; none says more than needed.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	if v.JWKSURL == "" || v.Audience == "" {
		return Identity{}, errors.New("odiceaauth: verifier needs JWKSURL and Audience")
	}
	if token == "" || len(token) > maxTokenSize {
		return Identity{}, errors.New("odiceaauth: no assertion")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, errors.New("odiceaauth: malformed assertion")
	}
	var h jwtHeader
	if err := decodePart(parts[0], &h); err != nil {
		return Identity{}, errors.New("odiceaauth: malformed header")
	}
	if h.Alg != "EdDSA" {
		return Identity{}, errors.New("odiceaauth: alg must be EdDSA")
	}
	if h.Typ != "JWT" {
		return Identity{}, errors.New("odiceaauth: header typ must be JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Identity{}, errors.New("odiceaauth: malformed signature")
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return Identity{}, err
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return Identity{}, errors.New("odiceaauth: bad signature")
	}
	var c claims
	if err := decodePart(parts[1], &c); err != nil {
		return Identity{}, errors.New("odiceaauth: malformed claims")
	}
	var aud string
	if json.Unmarshal(c.Aud, &aud) != nil {
		return Identity{}, errors.New("odiceaauth: aud must be a single string")
	}
	switch {
	case c.Typ != Type:
		return Identity{}, errors.New("odiceaauth: wrong typ claim")
	case c.Iss != v.issuer():
		return Identity{}, errors.New("odiceaauth: wrong issuer")
	case aud != v.Audience:
		return Identity{}, errors.New("odiceaauth: assertion is for another app")
	case c.Iat == nil || c.Exp == nil:
		return Identity{}, errors.New("odiceaauth: needs iat and exp")
	case c.Sub == "":
		return Identity{}, errors.New("odiceaauth: needs sub")
	}
	iat, exp := unix(*c.Iat), unix(*c.Exp)
	now := v.now()
	switch {
	case now.After(exp.Add(leeway)):
		return Identity{}, errors.New("odiceaauth: assertion expired")
	case iat.After(now.Add(leeway)):
		return Identity{}, errors.New("odiceaauth: assertion issued in the future")
	case !exp.After(iat) || exp.Sub(iat) > maxLife:
		return Identity{}, errors.New("odiceaauth: assertion lives too long")
	}
	return Identity{
		Subject: c.Sub, Email: c.Email, Name: c.Name, Org: c.Org, AppOrg: c.AppOrg,
		Env: c.Env, Mode: c.Mode, IssuedAt: iat, Expires: exp,
	}, nil
}

func decodePart(s string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

type ctxKey struct{}

// FromContext returns the Identity Require put in the request context.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Require wraps next: a request whose assertion header verifies reaches next
// with the Identity in its context (see [FromContext]); any other gets a
// plain 401 and next never runs. On the way in, the display headers
// (X-Odicea-User, -Email, -Name, -Org) are replaced by the verified values,
// so code that still reads them cannot be fooled by a caller that pairs a
// valid assertion with forged ones.
func (v *Verifier) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := v.Verify(r.Context(), strings.TrimSpace(r.Header.Get(v.header())))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))
		verified := map[string]string{
			"x-odicea-user": id.Subject, "x-odicea-email": id.Email, "x-odicea-name": id.Name, "x-odicea-org": id.Org,
		}
		h := make(http.Header, len(r.Header))
		for name, values := range r.Header { // any spelling: case, '_' for '-'
			if _, ours := verified[strings.ToLower(strings.ReplaceAll(name, "_", "-"))]; !ours {
				h[name] = values
			}
		}
		for name, value := range verified {
			h.Set(name, value)
		}
		r.Header = h
		next.ServeHTTP(w, r)
	})
}

// fetchCall is a key fetch in flight: callers that need keys meanwhile wait
// for it instead of starting another (and never hold v.mu while it runs).
type fetchCall struct{ done chan struct{} }

// key returns the public key kid. The set is refetched when it is older than
// jwksMaxAge or kid is unknown, never more often than jwksMinGap (an unknown
// kid inside the gap is refused at once: nobody can make the app hammer the
// gate). One fetch runs at a time and none under the lock. Same rules as the
// gate's ticket verifier.
func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if kid == "" {
		return nil, errors.New("odiceaauth: assertion has no kid")
	}
	v.mu.Lock()
	now := v.now()
	k, known := v.keys[kid]
	if known && now.Sub(v.loadedAt) <= jwksMaxAge {
		v.mu.Unlock()
		return k, nil
	}
	call := v.inflight
	if call == nil {
		if now.Sub(v.fetchedAt) < jwksMinGap {
			v.mu.Unlock()
			if known && now.Sub(v.loadedAt) <= jwksHardMaxAge {
				return k, nil // stale but recently tried: keep serving, up to the hard limit
			}
			return nil, errors.New("odiceaauth: unknown key id")
		}
		call = &fetchCall{done: make(chan struct{})}
		v.inflight, v.fetchedAt = call, now
		v.mu.Unlock()
		keys, err := v.fetch()
		v.mu.Lock()
		if err == nil {
			v.keys, v.loadedAt = keys, now
		}
		v.inflight = nil
		close(call.done)
	}
	v.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && v.now().Sub(v.loadedAt) <= jwksHardMaxAge {
		return k, nil
	}
	return nil, errors.New("odiceaauth: unknown key id")
}

// httpClient never follows redirects and gives up quickly.
func (v *Verifier) httpClient() *http.Client {
	if v.HTTPClient != nil {
		return v.HTTPClient
	}
	return &http.Client{Timeout: fetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (v *Verifier) fetch() (map[string]ed25519.PublicKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Crv, X, Kid, Use, Alg string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, jwksMaxBody)).Decode(&set); err != nil {
		return nil, err
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "EdDSA") {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		out[k.Kid] = ed25519.PublicKey(x)
	}
	if len(out) == 0 {
		return nil, errors.New("no usable keys")
	}
	return out, nil
}
