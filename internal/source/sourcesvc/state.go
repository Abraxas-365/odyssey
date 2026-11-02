package sourcesvc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// installState is what the install link carries through GitHub and back to
// the setup callback: who asked, for which organization, where to return.
// It is signed (HMAC-SHA256 with Options.StateKey) and expires. The callback
// is public, so the state alone binds nothing to the person who completes
// it: the callback only issues a claim (below) that the asking user, logged
// in, must redeem.
type installState struct {
	Organization identity.OrganizationID `json:"o"`
	User         identity.UserID         `json:"u"`
	Return       string                  `json:"r"`
	Expires      int64                   `json:"e"`
	Nonce        string                  `json:"n"`
}

// installClaim is the proof the setup callback issues once the GitHub user
// is confirmed to own the installation's account. Claim redeems it, and only
// for the organization and the Odyssey user it names.
type installClaim struct {
	Organization identity.OrganizationID `json:"o"`
	User         identity.UserID         `json:"u"`
	External     int64                   `json:"i"`
	Return       string                  `json:"r"`
	Expires      int64                   `json:"e"`
	Nonce        string                  `json:"n"`
}

// Domains separate the signatures: a state is never a valid claim.
const (
	stateDomain = "odyssey.github-install.v1."
	claimDomain = "odyssey.github-claim.v1."
)

const (
	// stateLife bounds how long an install link works (installing may take a
	// while: choosing repositories, an organization owner's approval).
	stateLife = time.Hour
	// claimLife bounds how long the console has to redeem a claim.
	claimLife = 15 * time.Minute
)

var b64 = base64.RawURLEncoding

func (s *Service) signState(st installState) (string, error) {
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	st.Nonce = nonce
	return s.sign(stateDomain, st)
}

func (s *Service) signClaim(c installClaim) (string, error) {
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	c.Nonce = nonce
	return s.sign(claimDomain, c)
}

// openState checks the signature and the expiry.
func (s *Service) openState(state string) (installState, error) {
	var st installState
	if !s.open(stateDomain, state, &st) || st.Organization.IsZero() || st.User.IsZero() ||
		s.opts.Now().Unix() > st.Expires {
		return installState{}, refuse(source.CodeStateInvalid)
	}
	return st, nil
}

// openClaim checks the signature and the expiry.
func (s *Service) openClaim(claim string) (installClaim, error) {
	var c installClaim
	if !s.open(claimDomain, claim, &c) || c.Organization.IsZero() || c.User.IsZero() || c.External <= 0 ||
		s.opts.Now().Unix() > c.Expires {
		return installClaim{}, refuse(source.CodeStateInvalid)
	}
	return c, nil
}

func newNonce() (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", errx.Wrap(err, "install token nonce", errx.TypeInternal)
	}
	return b64.EncodeToString(nonce), nil
}

// sign renders "<payload>.<mac>" over the JSON of v, the MAC bound to domain.
func (s *Service) sign(domain string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", errx.Wrap(err, "install token", errx.TypeInternal)
	}
	payload := b64.EncodeToString(raw)
	return payload + "." + b64.EncodeToString(s.mac(domain, payload)), nil
}

// open verifies a signed token of domain and decodes its payload into v.
func (s *Service) open(domain, token string, v any) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || len(token) > source.MaxClaim {
		return false
	}
	mac, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, s.mac(domain, payload)) {
		return false
	}
	raw, err := b64.DecodeString(payload)
	return err == nil && json.Unmarshal(raw, v) == nil
}

func (s *Service) mac(domain, payload string) []byte {
	m := hmac.New(sha256.New, s.opts.StateKey)
	m.Write([]byte(domain + payload))
	return m.Sum(nil)
}

// validReturn accepts a console path ("/..."), never another origin.
func validReturn(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > 200 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "\\\r\n\t \x00") || strings.Contains(path, "://") {
		return errx.InvalidField("return_to", "return_to must be a path of the console (starting with /)")
	}
	return nil
}
