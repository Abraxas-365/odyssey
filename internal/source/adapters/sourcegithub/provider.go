// Package sourcegithub is the source.GitProvider of GitHub: Odyssey's GitHub
// App, spoken to through the REST API (github.com or Enterprise Server).
//
// The App signs a short-lived JWT with its private key; with it, it asks for
// installation tokens, and those do the work. Where it is cheap, a token is
// scoped down to the one repository and the permissions the call needs, so a
// leaked token reaches little. Tokens and JWTs are cached until shortly
// before they expire and are never logged or put in an error.
//
// This is the only package that imports go-github and golang-jwt.
package sourcegithub

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v93/github"

	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

const (
	defaultAPIURL = "https://api.github.com"
	defaultWebURL = "https://github.com"

	// jwtLife is the App JWT's lifetime (GitHub allows at most 10 minutes);
	// iat is set back a minute for clock skew.
	jwtLife     = 9 * time.Minute
	jwtBackdate = time.Minute
	// jwtRefresh and tokenRefresh are how long before expiry a cached
	// credential stops being reused.
	jwtRefresh   = time.Minute
	tokenRefresh = 5 * time.Minute

	perPage      = 100
	maxPages     = 10 // 1000 items
	maxItems     = maxPages * perPage
	maxStatusLen = 140
	maxBody      = 1 << 20
)

// Options are the adapter's seams; zero values take the defaults.
type Options struct {
	// HTTPClient is used for every call (default: a client with a 30s timeout).
	// The tarball download uses a copy of it without the overall timeout.
	HTTPClient *http.Client
	// Now is the clock (tests).
	Now func() time.Time
}

// Provider implements source.GitProvider on GitHub.
type Provider struct {
	cfg           config.GitHub
	webURL        string
	gh            *github.Client // unauthenticated: each call sets its own bearer
	plain         *http.Client   // OAuth exchange
	stream        *http.Client   // tarball downloads: no overall timeout
	key           *rsa.PrivateKey
	webhookSecret []byte
	clientSecret  string
	now           func() time.Time

	jwtMu  sync.Mutex
	jwt    string
	jwtExp time.Time

	tokMu  sync.Mutex
	tokens map[tokenKey]*tokenEntry
}

var _ source.GitProvider = (*Provider)(nil)

// New reads the App's secrets and builds the provider. A configuration
// without an App is an error: the module uses sourcefake.Disabled instead.
func New(cfg config.GitHub, opts Options) (*Provider, error) {
	if !cfg.Enabled() {
		return nil, errx.Business(source.Message(source.CodeNotConfigured)).WithCode(source.CodeNotConfigured)
	}
	keyPEM, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, errx.Wrap(err, "read GITHUB_APP_PRIVATE_KEY_FILE", errx.TypeValidation)
	}
	// ParseRSAPrivateKeyFromPEM accepts PKCS1 and PKCS8.
	key, err := jwt.ParseRSAPrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, errx.Wrap(err, "GITHUB_APP_PRIVATE_KEY_FILE is not a PEM RSA private key", errx.TypeValidation)
	}
	webhook, err := readSecret(cfg.WebhookSecretFile, "GITHUB_WEBHOOK_SECRET_FILE")
	if err != nil {
		return nil, err
	}
	client, err := readSecret(cfg.ClientSecretFile, "GITHUB_CLIENT_SECRET_FILE")
	if err != nil {
		return nil, err
	}

	apiURL := strings.TrimRight(cfg.APIURL, "/")
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	webURL := strings.TrimRight(cfg.WebURL, "/")
	if webURL == "" {
		webURL = defaultWebURL
	}

	base := opts.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	clientOpts := []github.ClientOptionsFunc{
		github.WithHTTPClient(base),
		github.WithUserAgent("odyssey"),
		// Rate limits are per token; the client's single shared view of them
		// would block one installation for another's exhaustion.
		github.WithDisableRateLimitCheck(),
	}
	if apiURL != defaultAPIURL {
		clientOpts = append(clientOpts, github.WithEnterpriseURLs(apiURL, apiURL))
	}
	gh, err := github.NewClient(clientOpts...)
	if err != nil {
		return nil, errx.Wrap(err, "GITHUB_API_URL is not valid", errx.TypeValidation)
	}

	stream := *base
	stream.Timeout = 0
	stream.CheckRedirect = redirectWithoutCredentials

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{
		cfg:           cfg,
		webURL:        webURL,
		gh:            gh,
		plain:         base,
		stream:        &stream,
		key:           key,
		webhookSecret: []byte(webhook),
		clientSecret:  client,
		now:           now,
		tokens:        make(map[tokenKey]*tokenEntry),
	}, nil
}

func readSecret(path, name string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errx.Wrap(err, "read "+name, errx.TypeValidation)
	}
	secret := strings.TrimRight(string(raw), " \t\r\n")
	if secret == "" {
		return "", errx.Validation(name + " is empty")
	}
	return secret, nil
}

// redirectWithoutCredentials follows up to 10 redirects and never takes the
// Authorization header to another host:port (Go's own rule compares host
// names only, so it would forward it between two ports of one host).
func redirectWithoutCredentials(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

func (p *Provider) Enabled() bool { return true }
func (p *Provider) Slug() string  { return p.cfg.Slug }

func (p *Provider) InstallPage(state string) string {
	return p.webURL + "/apps/" + p.cfg.Slug + "/installations/new?state=" + url.QueryEscape(state)
}

// ---------------------------------------------------------------- credentials

// appJWT is the App's JWT, signed again a minute before the last one expires.
func (p *Provider) appJWT() (string, error) {
	p.jwtMu.Lock()
	defer p.jwtMu.Unlock()
	now := p.now()
	if p.jwt != "" && now.Before(p.jwtExp.Add(-jwtRefresh)) {
		return p.jwt, nil
	}
	exp := now.Add(jwtLife)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(p.cfg.AppID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
	signed, err := token.SignedString(p.key)
	if err != nil {
		return "", errx.Wrap(err, "sign the GitHub App JWT", errx.TypeInternal)
	}
	p.jwt, p.jwtExp = signed, exp
	return signed, nil
}

// Permission sets of scoped tokens.
const (
	permsAll    = ""       // everything the installation has (no repository scope)
	permsRead   = "read"   // contents:read + metadata:read
	permsStatus = "status" // statuses:write
)

type tokenKey struct {
	installation int64
	repository   int64
	perms        string
}

type tokenEntry struct {
	mu      sync.Mutex // held while fetching: one request per key
	token   string
	expires time.Time
}

func (p *Provider) entry(key tokenKey) *tokenEntry {
	p.tokMu.Lock()
	defer p.tokMu.Unlock()
	if e, ok := p.tokens[key]; ok {
		return e
	}
	if len(p.tokens) >= 256 {
		now := p.now()
		for k, e := range p.tokens {
			if e.mu.TryLock() {
				if now.After(e.expires) {
					delete(p.tokens, k)
				}
				e.mu.Unlock()
			}
		}
	}
	e := &tokenEntry{}
	p.tokens[key] = e
	return e
}

// installationToken is a token of the installation, scoped to repository
// (when not zero) with perms; cached per key until 5 minutes before it expires.
func (p *Provider) installationToken(ctx context.Context, installation, repository int64, perms string) (string, error) {
	e := p.entry(tokenKey{installation, repository, perms})
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.token != "" && p.now().Before(e.expires.Add(-tokenRefresh)) {
		return e.token, nil
	}
	app, err := p.appJWT()
	if err != nil {
		return "", err
	}
	var body any
	if repository != 0 {
		opts := &github.InstallationTokenOptions{RepositoryIDs: []int64{repository}}
		switch perms {
		case permsRead:
			opts.Permissions = &github.InstallationPermissions{Contents: github.Ptr("read"), Metadata: github.Ptr("read")}
		case permsStatus:
			opts.Permissions = &github.InstallationPermissions{Statuses: github.Ptr("write")}
		}
		body = opts
	}
	var out github.InstallationToken
	_, err = p.call(ctx, app, http.MethodPost, fmt.Sprintf("app/installations/%d/access_tokens", installation), body, &out)
	if err != nil {
		// 404 (gone), 403 (suspended) and 422 (repository not in it) all
		// mean the installation no longer reaches what was asked.
		return "", p.fail(ctx, err, source.CodeNoAccess, http.StatusUnprocessableEntity)
	}
	if out.GetToken() == "" {
		return "", down(errors.New("GitHub answered an installation token without a token"))
	}
	e.token = out.GetToken()
	if out.ExpiresAt != nil {
		e.expires = out.ExpiresAt.Time
	} else {
		e.expires = time.Time{} // unknown: do not reuse
	}
	return e.token, nil
}

// ------------------------------------------------------------------ requests

// call sends one API request with a bearer credential and decodes the JSON
// answer into out (when not nil).
func (p *Provider) call(ctx context.Context, bearer, method, path string, body, out any) (*github.Response, error) {
	req, err := p.gh.NewRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return p.gh.Do(req, out)
}

// pages walks a paginated listing: fetch gets a page number and returns the
// next one (0 at the end).
func pages(fetch func(page int) (next int, err error)) error {
	page := 1
	for i := 0; i < maxPages && page != 0; i++ {
		next, err := fetch(page)
		if err != nil {
			return err
		}
		page = next
	}
	return nil
}

func paged(path string, page int) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sper_page=%d&page=%d", path, sep, perPage, page)
}

// ----------------------------------------------------------------- errors

func down(err error) error {
	return errx.Wrap(redact(err), source.Message(source.CodeProviderDown), errx.TypeExternal).WithCode(source.CodeProviderDown)
}

func typed(err error, code string, t errx.Type) error {
	return errx.Wrap(redact(err), source.Message(code), t).WithCode(code)
}

// redact drops the query of a URL in a transport error: a codeload redirect
// carries a token in it.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			u.RawQuery, u.User = "", nil
			ue.URL = u.String()
		}
	}
	return err
}

// fail maps a GitHub error to the port's: a 404 (or one of alsoMissing) is
// NotFound with the code missing; 403 is NoAccess unless it is a rate limit;
// rate limits, 5xx and transport failures are the provider being down.
func (p *Provider) fail(ctx context.Context, err error, missing string, alsoMissing ...int) error {
	if err == nil {
		return nil
	}
	var typedErr *errx.Error
	if errors.As(err, &typedErr) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var rate *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rate) || errors.As(err, &abuse) {
		return down(err)
	}
	var resp *github.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil {
		status := resp.Response.StatusCode
		switch {
		case status == http.StatusTooManyRequests || status >= 500:
			return down(err)
		case status == http.StatusForbidden && strings.Contains(strings.ToLower(resp.Message), "rate limit"):
			return down(err)
		case status == http.StatusNotFound || containsInt(alsoMissing, status):
			return typed(err, missing, errx.TypeNotFound)
		case status == http.StatusForbidden:
			return typed(err, source.CodeNoAccess, errx.TypeNotFound)
		default: // 401 (the App's own credentials), 400, 409, ...
			return typed(err, source.CodeInternal, errx.TypeExternal)
		}
	}
	return down(err)
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(s)
}
