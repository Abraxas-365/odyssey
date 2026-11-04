package access

import (
	"container/list"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	maxTicketLife = 5 * time.Minute  // exp - iat the gate accepts
	leeway        = 30 * time.Second // clock skew between the planes
	jwksMaxAge    = 10 * time.Minute // keys are refetched after this
	jwksMinGap    = 10 * time.Second // never refetch more often than this
	// jwksHardMaxAge: when refreshing keeps failing, a cached key is served
	// no longer than this (a revoked or rotated-out key must not live on
	// because the issuer is unreachable).
	jwksHardMaxAge = time.Hour
	jwksMaxBody    = 1 << 20
	replayMax      = 100_000 // remembered jtis (oldest forgotten first)
)

// Expect is what a ticket must say for the app being opened (it comes from
// the Ingress' forwardAuth address and the request, never from the ticket).
type Expect struct {
	Host   string // the app's host name, no port (cookie binding)
	Aud    string // host[:port] as users reach it: the ticket's aud
	Env    string
	Mode   string
	AppOrg string
}

// Ticket is a verified ticket.
type Ticket struct {
	Subject, Email, Name string
	Org                  string // the user's organization
	JTI                  string
}

type ticketClaims struct {
	jwt.RegisteredClaims
	Email  string `json:"email"`
	Name   string `json:"name"`
	Org    string `json:"org"`
	AppOrg string `json:"app_org"`
	Env    string `json:"env"`
	Mode   string `json:"mode"`
	Typ    string `json:"typ"`
}

// Verifier checks tickets against the issuer's published keys.
type Verifier struct {
	// Issuer is the control plane's public base URL (the iss claim).
	Issuer string
	// JWKSURL is where the keys are fetched (server side).
	JWKSURL string
	HTTP    *http.Client
	Now     func() time.Time

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time // last attempt, successful or not
	loadedAt  time.Time // last success
	inflight  *fetchCall
	replay    *replayCache
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify checks raw and consumes its jti: the same ticket verifies once.
func (v *Verifier) Verify(ctx context.Context, raw string, want Expect) (Ticket, error) {
	var c ticketClaims
	tok, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(strings.TrimRight(v.Issuer, "/")), // the control plane trims it too
		jwt.WithAudience(want.Aud),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || !tok.Valid {
		return Ticket{}, fmt.Errorf("ticket: %w", err)
	}
	switch {
	case c.Typ != TicketType:
		return Ticket{}, errors.New("ticket: wrong typ")
	case c.IssuedAt == nil || c.ExpiresAt.Sub(c.IssuedAt.Time) > maxTicketLife:
		return Ticket{}, errors.New("ticket: lives too long")
	case c.Env != want.Env || c.Mode != want.Mode || c.AppOrg != want.AppOrg:
		return Ticket{}, errors.New("ticket: is for another app")
	case c.Subject == "" || c.Org == "" || c.ID == "":
		return Ticket{}, errors.New("ticket: needs sub, org and jti")
	case want.Mode == ModeTeam && c.Org != c.AppOrg:
		return Ticket{}, errors.New("ticket: user is outside the app's team")
	}
	if !v.replayCache().use(c.ID, c.ExpiresAt.Time.Add(leeway), v.now()) {
		return Ticket{}, errors.New("ticket: already used")
	}
	return Ticket{Subject: c.Subject, Email: c.Email, Name: c.Name, Org: c.Org, JTI: c.ID}, nil
}

func (v *Verifier) replayCache() *replayCache {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.replay == nil {
		v.replay = newReplayCache(replayMax)
	}
	return v.replay
}

// fetchCall is a key fetch in flight: callers that need keys meanwhile wait
// for it instead of starting another (and never hold v.mu while it runs).
type fetchCall struct{ done chan struct{} }

// key returns the public key kid. The set is refetched when it is older than
// jwksMaxAge or kid is unknown, never more often than jwksMinGap (an unknown
// kid inside the gap is refused at once: nobody can make the gate hammer the
// issuer). One fetch runs at a time and none under the lock.
func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if kid == "" {
		return nil, errors.New("ticket has no kid")
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
			return nil, errors.New("unknown key id")
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
	return nil, errors.New("unknown key id")
}

// httpClient never follows redirects and gives up quickly.
func (v *Verifier) httpClient() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (v *Verifier) fetch() (map[string]ed25519.PublicKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// replayCache remembers used jtis until they expire. When full it forgets
// the oldest (tickets live minutes, so a replay needs max tickets first).
type replayCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // of *replayEntry, oldest first
	byID  map[string]*list.Element
}

type replayEntry struct {
	id      string
	expires time.Time
}

func newReplayCache(max int) *replayCache {
	return &replayCache{max: max, order: list.New(), byID: map[string]*list.Element{}}
}

func (r *replayCache) drop(e *list.Element) {
	r.order.Remove(e)
	delete(r.byID, e.Value.(*replayEntry).id)
}

// use records id and reports whether it was unused.
func (r *replayCache) use(id string, expires, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for e := r.order.Front(); e != nil; e = r.order.Front() {
		if r.order.Len() < r.max && e.Value.(*replayEntry).expires.After(now) {
			break
		}
		r.drop(e)
	}
	if e, seen := r.byID[id]; seen {
		if e.Value.(*replayEntry).expires.After(now) {
			return false
		}
		r.drop(e)
	}
	r.byID[id] = r.order.PushBack(&replayEntry{id: id, expires: expires})
	return true
}
