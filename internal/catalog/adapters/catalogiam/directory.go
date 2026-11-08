// Package catalogiam is catalog.Directory on IAMKit: the organization's
// groups (teams), read with the backend's service account (iam:members:read
// covers them). It is the only place the catalog meets the IAMKit SDK; the
// token comes from the composition root (one owner of the secret).
package catalogiam

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/apiclient"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Token gives the service account's access token; Forget drops a token
// IAMKit refused.
type Token struct {
	Get    func(context.Context) (string, error)
	Forget func()
}

// Directory reads an organization's groups and a member's groups; a
// member's are cached for TTL (a burst of creates asks IAMKit once; a change
// of team is seen within a minute).
type Directory struct {
	iamURL, environment string
	token               Token
	http                *http.Client
	ttl                 time.Duration
	now                 func() time.Time

	mu      sync.Mutex
	members map[string]cached[[]string]
}

type cached[T any] struct {
	value T
	read  time.Time
}

var _ catalog.Directory = (*Directory)(nil)

// Options tune the directory; zero values are the defaults.
type Options struct {
	// TTL is how long a read is reused (default 1 minute).
	TTL time.Duration
	// HTTP is the client IAMKit is called with (default: 10 s timeout).
	HTTP *http.Client
	// Now is the clock (tests set it).
	Now func() time.Time
}

// New builds the directory for IAMKit at iamURL, in its environment.
func New(iamURL, environment string, token Token, opts Options) *Directory {
	if opts.TTL <= 0 {
		opts.TTL = time.Minute
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Directory{iamURL: iamURL, environment: environment, token: token, http: opts.HTTP, ttl: opts.TTL, now: opts.Now,
		members: map[string]cached[[]string]{}}
}

// Teams is read fresh each time: only IT asks (the console, saving an
// offering), and a team made a moment ago must be choosable.
func (d *Directory) Teams(ctx context.Context, org identity.OrganizationID) ([]catalog.Team, error) {
	env, err := d.env(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := env.Groups(ctx, org.String(), apiclient.GroupFilter{})
	if err != nil {
		return nil, d.failed(err, "list the organization's teams")
	}
	out := make([]catalog.Team, 0, len(groups))
	for _, g := range groups {
		out = append(out, catalog.Team{ID: g.ID, Name: g.Name, Members: g.MemberCount, Directory: g.ConnectionID != nil})
	}
	slices.SortFunc(out, func(a, b catalog.Team) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// TeamsOf is cached for TTL: every create asks it.
func (d *Directory) TeamsOf(ctx context.Context, org identity.OrganizationID, user identity.UserID) ([]string, error) {
	key := org.String() + "/" + user.String()
	d.mu.Lock()
	c, ok := d.members[key]
	d.mu.Unlock()
	if ok && d.now().Sub(c.read) < d.ttl {
		return slices.Clone(c.value), nil
	}
	env, err := d.env(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := env.MemberGroups(ctx, org.String(), user.String())
	if err != nil {
		return nil, d.failed(err, "list the person's teams")
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.ID)
	}
	slices.Sort(out)
	d.mu.Lock()
	now := d.now()
	for k, c := range d.members { // one entry per person who created lately, not per person ever
		if now.Sub(c.read) >= d.ttl {
			delete(d.members, k)
		}
	}
	d.members[key] = cached[[]string]{value: out, read: now}
	d.mu.Unlock()
	return slices.Clone(out), nil
}

func (d *Directory) env(ctx context.Context) (apiclient.Environment, error) {
	token, err := d.token.Get(ctx)
	if err != nil {
		return apiclient.Environment{}, err
	}
	return apiclient.New(d.iamURL, token, apiclient.WithHTTPClient(d.http)).Environment(d.environment), nil
}

// failed drops the token (a revoked or rotated one is asked for again next
// time) and words the error as external.
func (d *Directory) failed(err error, what string) error {
	if d.token.Forget != nil {
		d.token.Forget()
	}
	return errx.Wrap(err, what, errx.TypeExternal)
}
