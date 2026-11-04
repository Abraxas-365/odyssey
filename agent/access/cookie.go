package access

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// SessionTTL is how long a signed-in cookie lives.
const SessionTTL = 8 * time.Hour

// Session is what the cookie says: who signed in, and for which app. A
// cookie from one app is no good on another: host, environment, mode and the
// app's organization must all match.
type Session struct {
	Host    string `json:"h"`
	Env     string `json:"e"`
	Mode    string `json:"m"`
	AppOrg  string `json:"a"`
	User    string `json:"s"`
	Email   string `json:"em,omitempty"`
	Name    string `json:"n,omitempty"`
	UserOrg string `json:"o"`
	Expires int64  `json:"x"` // Unix seconds
}

// Codec seals and opens sessions with an HMAC-SHA256 key.
type Codec struct{ key []byte }

// NewCodec returns a Codec; the key must be at least 32 bytes.
func NewCodec(key []byte) (*Codec, error) {
	if len(key) < 32 {
		return nil, errors.New("cookie key must be at least 32 bytes")
	}
	return &Codec{key: append([]byte(nil), key...)}, nil
}

func (c *Codec) mac(payload string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// MaxCookieValue keeps the whole Set-Cookie line under what browsers store
// (4096 bytes with name and attributes).
const MaxCookieValue = 3800

// Seal returns the cookie value: base64url(json).base64url(hmac). A name or
// email that would make it too big is truncated (identity headers are for
// display; the user id is what matters).
func (c *Codec) Seal(s Session) string {
	for {
		raw, _ := json.Marshal(s)
		payload := base64.RawURLEncoding.EncodeToString(raw)
		v := payload + "." + base64.RawURLEncoding.EncodeToString(c.mac(payload))
		if len(v) < MaxCookieValue {
			return v
		}
		switch {
		case len(s.Name) > 0 && len(s.Name) >= len(s.Email):
			s.Name = cut(s.Name)
		case len(s.Email) > 0:
			s.Email = cut(s.Email)
		default:
			return v // ids alone are too big: nothing more to cut
		}
	}
}

// cut halves s on a rune boundary.
func cut(s string) string {
	n := len(s) / 2
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Open verifies value's signature and expiry.
func (c *Codec) Open(value string, now time.Time) (Session, error) {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || len(value) > 4096 {
		return Session{}, errors.New("malformed cookie")
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, c.mac(payload)) {
		return Session{}, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Session{}, errors.New("malformed cookie")
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return Session{}, errors.New("malformed cookie")
	}
	if now.Unix() >= s.Expires {
		return Session{}, errors.New("expired")
	}
	return s, nil
}
