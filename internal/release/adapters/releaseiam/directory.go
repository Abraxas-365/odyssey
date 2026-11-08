// Package releaseiam is release.People on IAMKit: the organization's members
// as the backend's service account (iam:members:read) reads them. It is the
// only place release meets the IAMKit SDK.
package releaseiam

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/apiclient"
	"github.com/Abraxas-365/iamkit/sdk/authclient"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Directory names people from their organization's member list. It caches
// the service account's token until shortly before it expires and each
// organization's members for TTL, so a burst of deployments asks IAMKit once.
type Directory struct {
	iamURL, environment, secret string
	auth                        *authclient.Client
	http                        *http.Client
	ttl                         time.Duration
	now                         func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
	orgs    map[identity.OrganizationID]members
}

type members struct {
	names map[string]string
	read  time.Time
}

var _ release.People = (*Directory)(nil)

// Options tune the directory; zero values are the defaults.
type Options struct {
	// TTL is how long an organization's member list is reused (default 1 minute).
	TTL time.Duration
	// HTTP is the client IAMKit is called with (default: 10 s timeout).
	HTTP *http.Client
	// Now is the clock (tests set it).
	Now func() time.Time
}

// New builds the directory for IAMKit at iamURL, in its environment, with
// the backend service account's secret (ik_svc_…).
func New(iamURL, environment, secret string, opts Options) *Directory {
	if opts.TTL <= 0 {
		opts.TTL = time.Minute
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Directory{iamURL: iamURL, environment: environment, secret: secret,
		auth: authclient.New(iamURL, authclient.WithHTTPClient(opts.HTTP)), http: opts.HTTP, ttl: opts.TTL, now: opts.Now,
		orgs: map[identity.OrganizationID]members{}}
}

// Name is the user's name among the organization's members ("" when they
// are not one, or have no name).
func (d *Directory) Name(ctx context.Context, organization identity.OrganizationID, user identity.UserID) (string, error) {
	names, err := d.members(ctx, organization)
	if err != nil {
		return "", err
	}
	return names[user.String()], nil
}

func (d *Directory) members(ctx context.Context, organization identity.OrganizationID) (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m, ok := d.orgs[organization]; ok && d.now().Sub(m.read) < d.ttl {
		return m.names, nil
	}
	token, err := d.tokenLocked(ctx)
	if err != nil {
		return nil, err
	}
	list, err := apiclient.New(d.iamURL, token, apiclient.WithHTTPClient(d.http)).Environment(d.environment).Members(ctx, organization.String())
	if err != nil {
		d.token = "" // a revoked or rotated token: ask for a new one next time
		return nil, errx.Wrap(err, "list the organization's members", errx.TypeExternal)
	}
	names := make(map[string]string, len(list))
	for _, m := range list {
		if m.Active && m.UserName != "" {
			names[m.UserID] = m.UserName
		}
	}
	d.orgs[organization] = members{names: names, read: d.now()}
	return names, nil
}

// Token is the service account's access token (cached), for other adapters
// reading IAMKit as the backend (catalogiam): the secret has one owner.
func (d *Directory) Token(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tokenLocked(ctx)
}

// ForgetToken drops the cached token (a caller got 401 with it: revoked or
// rotated), so the next Token asks for a new one.
func (d *Directory) ForgetToken() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.token = ""
}

// tokenLocked is the service account's access token, renewed a minute
// before it expires. The caller holds mu.
func (d *Directory) tokenLocked(ctx context.Context) (string, error) {
	if d.token != "" && d.now().Before(d.expires) {
		return d.token, nil
	}
	pair, err := d.auth.MachineToken(ctx, d.secret)
	if err != nil {
		return "", errx.Wrap(err, "get the service account's token", errx.TypeExternal)
	}
	d.token = pair.AccessToken
	d.expires = d.now().Add(time.Duration(pair.ExpiresIn)*time.Second - time.Minute)
	return d.token, nil
}
