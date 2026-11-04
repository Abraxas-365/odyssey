// Package accessiam is the access sign-in's OIDC client: IAMKit's
// authorization-code + PKCE flow with the confidential "access" client.
package accessiam

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/authclient"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Login talks to IAMKit.
type Login struct {
	oauth     *authclient.OAuthClient
	publicIAM string
	clientID  string
}

var _ access.Login = (*Login)(nil)

// New builds the client. iamURL is IAMKit as the API reaches it; publicIAM as
// browsers do (the authorization endpoint).
func New(iamURL, publicIAM, clientID, clientSecret string) *Login {
	return &Login{
		oauth:     authclient.NewOAuth(iamURL, clientID, clientSecret, authclient.WithOAuthHTTPClient(&http.Client{Timeout: 10 * time.Second})),
		publicIAM: strings.TrimRight(publicIAM, "/"), clientID: clientID,
	}
}

func (l *Login) AuthorizeURL(r access.LoginRequest) string {
	q := url.Values{
		"client_id": {l.clientID}, "redirect_uri": {r.Redirect}, "response_type": {"code"},
		"scope": {"openid profile email"}, "state": {r.State}, "nonce": {r.Nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {r.Challenge},
	}
	if r.Organization != "" {
		q.Set("organization_id", r.Organization)
	}
	return l.publicIAM + "/oauth/authorize?" + q.Encode()
}

func (l *Login) Exchange(ctx context.Context, code, redirect, verifier, nonce string) (access.Identity, error) {
	tokens, err := l.oauth.Exchange(ctx, code, redirect, verifier)
	if err != nil {
		var oe *authclient.OAuthError
		if asOAuth(err, &oe) && oe.HTTPStatus >= 400 && oe.HTTPStatus < 500 {
			return access.Identity{}, flowInvalid()
		}
		return access.Identity{}, errx.Wrap(err, "exchange the access code", errx.TypeExternal)
	}
	id, ok := l.idClaims(tokens.IDToken)
	if !ok || nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 {
		return access.Identity{}, flowInvalid()
	}
	info, err := l.oauth.UserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return access.Identity{}, errx.Wrap(err, "read the signed-in user", errx.TypeExternal)
	}
	// The user the access token speaks for is the one the ID token names.
	if info.Subject == "" || info.Subject != id.Subject {
		return access.Identity{}, flowInvalid()
	}
	return access.Identity{Subject: info.Subject, Email: info.Email, Name: info.Name, Organization: info.OrganizationID, Environment: info.EnvironmentID}, nil
}

func flowInvalid() error {
	return errx.Validation("el inicio de sesión expiró o no es válido").WithCode(access.CodeFlowInvalid)
}

// idToken is the part of the ID token the sign-in checks.
type idToken struct {
	Subject  string          `json:"sub"`
	Nonce    string          `json:"nonce"`
	Audience json.RawMessage `json:"aud"`
	AZP      string          `json:"azp"`
}

// idClaims reads the ID token's claims. It came straight from IAMKit's token
// endpoint in answer to this client's authenticated request, so its signature
// need not be checked (OIDC Core 3.1.3.7, step 6); what binds it to this
// sign-in is the nonce, and its audience must be this client (OIDC Core
// 3.1.3.7 steps 3-5: with several audiences, azp names this client).
func (l *Login) idClaims(raw string) (idToken, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || l.clientID == "" {
		return idToken{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idToken{}, false
	}
	var t idToken
	if json.Unmarshal(payload, &t) != nil || t.Subject == "" {
		return idToken{}, false
	}
	var one string
	var many []string
	switch {
	case json.Unmarshal(t.Audience, &one) == nil:
		many = []string{one}
	case json.Unmarshal(t.Audience, &many) == nil:
	default:
		return idToken{}, false
	}
	if !slices.Contains(many, l.clientID) || (len(many) > 1 && t.AZP != l.clientID) || (t.AZP != "" && t.AZP != l.clientID) {
		return idToken{}, false
	}
	return t, true
}

func asOAuth(err error, target **authclient.OAuthError) bool { return errors.As(err, target) }
