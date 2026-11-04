// Package accessmodule assembles the access sign-in: signer, IAMKit client,
// service and HTTP handler. Only the composition root imports it.
package accessmodule

import (
	"net/url"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/accesssvc"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accesshttp"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessiam"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessjwt"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Deps struct {
	// Targets finds an environment by id alone (release's AccessLookup).
	Targets release.AccessLookup
	IAM     config.IAM
	Access  config.Access
	// Issuer is the API's public base URL (tickets' `iss`, the OAuth redirect base).
	Issuer string
	// Login replaces the IAMKit client (tests).
	Login access.Login
}

type Module struct {
	HTTP *accesshttp.Handler
	// Enabled: the sign-in is configured (key and IAMKit client).
	Enabled bool
}

// New is a disabled module (its routes answer 404) unless the key and client
// are configured.
func New(deps Deps) (Module, error) {
	login := deps.Login
	if !deps.Access.Enabled() && login == nil {
		return Module{HTTP: accesshttp.New(nil, nil, false)}, nil
	}
	if deps.Issuer == "" {
		return Module{}, errMissingIssuer
	}
	signer, err := accessjwt.New(deps.Access.SigningKey, deps.Access.PreviousKeys...)
	if err != nil {
		return Module{}, err
	}
	if login == nil {
		// The ID token is trusted for coming straight from IAMKit's token
		// endpoint (no signature check), so that channel must be TLS unless it
		// stays on this machine or inside the cluster.
		if err := backChannelSafe(deps.IAM.URL, deps.Access.AllowHTTP); err != nil {
			return Module{}, err
		}
		login = accessiam.New(deps.IAM.URL, deps.IAM.PublicURL, deps.Access.ClientID, deps.Access.ClientSecret)
	}
	svc, err := accesssvc.New(deps.Targets, signer, login, deps.Access.SigningKey, accesssvc.Options{
		Issuer: deps.Issuer, IAMEnvironment: deps.IAM.Environment, AllowHTTP: deps.Access.AllowHTTP})
	if err != nil {
		return Module{}, err
	}
	secure := false
	if u, err := url.Parse(deps.Issuer); err == nil {
		secure = strings.EqualFold(u.Scheme, "https")
	}
	return Module{HTTP: accesshttp.New(svc, svc, secure), Enabled: true}, nil
}

var errMissingIssuer = errString("the access sign-in needs the API's public URL: set IAMKIT_AUDIENCE (https://<odicea>/mcp)")

var errPlainBackChannel = errString("the access sign-in reaches IAMKit over plain http on a public host: use https for IAMKIT_URL " +
	"(or ODYSSEY_ACCESS_ALLOW_HTTP=true if that network is private)")

// backChannelSafe accepts https, and http only to this machine or a private
// in-cluster name: a single-label service name (compose, same namespace),
// *.svc, *.svc.cluster.local, *.internal, or allowHTTP.
func backChannelSafe(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errString("IAMKIT_URL is not a URL")
	}
	if strings.EqualFold(u.Scheme, "https") || allowHTTP {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if accesssvc.DevHost(host) || !strings.Contains(host, ".") || strings.HasSuffix(host, ".svc") ||
		strings.HasSuffix(host, ".svc.cluster.local") || strings.HasSuffix(host, ".internal") {
		return nil
	}
	return errPlainBackChannel
}

type errString string

func (e errString) Error() string { return string(e) }
