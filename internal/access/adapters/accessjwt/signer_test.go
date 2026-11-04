package accessjwt_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessjwt"
)

func seed(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSignedTicketVerifiesWithThePublishedKey(t *testing.T) {
	s, err := accessjwt.New(seed(1), seed(2))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	raw, err := s.Sign(access.Ticket{Issuer: "https://api.example", Audience: "app.example:8443", Subject: "u1", Email: "a@b.c", Name: "Ana",
		Organization: "o1", AppOrganization: "o2", Environment: "e1", Mode: "team", ID: "j1", IssuedAt: now, Expires: now.Add(access.TicketTTL)})
	if err != nil {
		t.Fatal(err)
	}
	keys := s.Keys()
	if len(keys) != 2 {
		t.Fatalf("keys %d, want current + previous", len(keys))
	}
	pub := ed25519.NewKeyFromSeed(seed(1)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	if want := base64.RawURLEncoding.EncodeToString(sum[:])[:16]; keys[0].ID != want || !bytes.Equal(keys[0].Public, pub) {
		t.Fatalf("current key %+v, want kid %s", keys[0], want)
	}
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return keys[0].Public, nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !tok.Valid {
		t.Fatalf("verify: %v", err)
	}
	if tok.Header["kid"] != keys[0].ID || tok.Header["typ"] != "JWT" || tok.Header["alg"] != "EdDSA" {
		t.Fatalf("header %v", tok.Header)
	}
	for k, want := range map[string]any{"iss": "https://api.example", "aud": "app.example:8443", "sub": "u1", "email": "a@b.c", "name": "Ana",
		"org": "o1", "app_org": "o2", "env": "e1", "mode": "team", "jti": "j1", "typ": access.TicketType} {
		if claims[k] != want {
			t.Errorf("claim %s = %v, want %v", k, claims[k], want)
		}
	}
	if int64(claims["exp"].(float64))-int64(claims["iat"].(float64)) != 60 {
		t.Errorf("lifetime %v", claims)
	}
	// a previous key does not verify what the current one signed
	if _, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return keys[1].Public, nil }, jwt.WithValidMethods([]string{"EdDSA"})); err == nil {
		t.Fatal("verified with another key")
	}
}

func TestSignerRefusesBadSeeds(t *testing.T) {
	if _, err := accessjwt.New([]byte("short")); err == nil {
		t.Fatal("short seed accepted")
	}
	if _, err := accessjwt.New(seed(1), []byte("short")); err == nil {
		t.Fatal("short previous seed accepted")
	}
	s, err := accessjwt.New(seed(1), seed(1))
	if err != nil || len(s.Keys()) != 1 {
		t.Fatalf("the current key listed twice: %v %d", err, len(s.Keys()))
	}
}
