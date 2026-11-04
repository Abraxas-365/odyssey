package accessjwt

import (
	"crypto/ed25519"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Verify checks a ticket the way the gate does and returns its claims: EdDSA
// only, not expired at now (zero: the clock), and for the audience and issuer
// when they are not empty. The control plane never verifies tickets; this is
// for tests and tooling, so the JWT library stays in this package.
func Verify(token string, key ed25519.PublicKey, audience, issuer string, now time.Time) (map[string]any, error) {
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{"EdDSA"})}
	if audience != "" {
		opts = append(opts, jwt.WithAudience(audience))
	}
	if issuer != "" {
		opts = append(opts, jwt.WithIssuer(issuer))
	}
	if !now.IsZero() {
		opts = append(opts, jwt.WithTimeFunc(func() time.Time { return now }))
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return key, nil }, opts...); err != nil {
		return nil, err
	}
	return claims, nil
}
