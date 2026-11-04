// Package accessjwt signs the access tickets (EdDSA JWTs) and publishes the
// keys that verify them. The JWT library stays here (architecture test).
package accessjwt

import (
	"crypto/ed25519"
	"errors"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/odyssey/internal/access"
)

// Signer signs with the current key; previous keys are only published.
type Signer struct {
	current  ed25519.PrivateKey
	currentK access.Key
	previous []access.Key
}

var _ access.Signer = (*Signer)(nil)

// New builds a Signer from Ed25519 seeds (32 bytes): the key to sign with
// and the ones that signed before.
func New(seed []byte, previous ...[]byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the access signing key must be an Ed25519 seed of 32 bytes")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	s := &Signer{current: priv, currentK: access.Key{ID: access.KeyID(pub), Public: pub}}
	for _, p := range previous {
		if len(p) != ed25519.SeedSize {
			return nil, errors.New("a previous access key must be an Ed25519 seed of 32 bytes")
		}
		pp := ed25519.NewKeyFromSeed(p).Public().(ed25519.PublicKey)
		if k := access.KeyID(pp); k != s.currentK.ID {
			s.previous = append(s.previous, access.Key{ID: k, Public: pp})
		}
	}
	return s, nil
}

// Keys is the current key first, then the previous ones.
func (s *Signer) Keys() []access.Key {
	return append([]access.Key{s.currentK}, s.previous...)
}

// Sign makes the ticket's compact JWT (docs/access.md "Ticket contract").
func (s *Signer) Sign(t access.Ticket) (string, error) {
	claims := jwt.MapClaims{
		"iss": t.Issuer, "aud": t.Audience, "sub": t.Subject, "email": t.Email, "name": t.Name,
		"org": t.Organization, "app_org": t.AppOrganization, "env": t.Environment, "mode": t.Mode,
		"iat": t.IssuedAt.Unix(), "exp": t.Expires.Unix(), "jti": t.ID, "typ": access.TicketType,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = s.currentK.ID
	return tok.SignedString(s.current)
}
