// Package accesssvc implements access.Commands and access.Queries. It holds
// no state: the sign-in in progress travels in a sealed cookie, so an
// unauthenticated endpoint never writes to the database.
package accesssvc

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Options tune the service.
type Options struct {
	// Issuer is the control plane's public API base URL: the tickets' `iss`
	// (exactly what the gate is configured with) and the base of the OAuth
	// redirect, <Issuer>/access/callback.
	Issuer string
	// IAMEnvironment is the IAMKit environment of the installation: the
	// `organization` mode admits its users.
	IAMEnvironment string
	// AllowHTTP admits apps on plain http at any host (otherwise only
	// development hosts: see DevHost).
	AllowHTTP bool
	// Now is the clock (tests).
	Now func() time.Time
}

type Service struct {
	targets release.AccessLookup
	signer  access.Signer
	login   access.Login
	box     cipher.AEAD
	opts    Options
}

var (
	_ access.Commands = (*Service)(nil)
	_ access.Queries  = (*Service)(nil)
)

// New builds the service. secret keys the flow cookie (any private,
// stable bytes: the signing seed will do; it is hashed first).
func New(targets release.AccessLookup, signer access.Signer, login access.Login, secret []byte, opts Options) (*Service, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Issuer = strings.TrimRight(opts.Issuer, "/")
	key := sha256.Sum256(append([]byte("odicea-access-flow\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	box, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{targets: targets, signer: signer, login: login, box: box, opts: opts}, nil
}

func (s *Service) Keys() []access.Key { return s.signer.Keys() }

func (s *Service) callbackURL() string { return s.opts.Issuer + "/access/callback" }

// flow is the sign-in in progress, sealed in the cookie.
type flow struct {
	Environment string `json:"e"`
	Mode        string `json:"m"`
	Return      string `json:"r"`
	Verifier    string `json:"v"`
	State       string `json:"s"`
	Nonce       string `json:"n"`
	Expires     int64  `json:"x"`
}

// Authorize validates the gate's question and starts the IAMKit sign-in.
func (s *Service) Authorize(ctx context.Context, in access.Authorize) (access.Redirect, error) {
	mode := release.AccessMode(in.Mode)
	if mode != release.AccessTeam && mode != release.AccessOrganization {
		return access.Redirect{}, errx.InvalidField("mode", "mode must be team or organization")
	}
	site, err := s.place(ctx, in.Environment, in.Return)
	if err != nil {
		return access.Redirect{}, err
	}
	target := site.target
	// The gate asks for the mode the cluster was told; anything else (a public
	// app has no sign-in) is a gate that is out of date or a forged question.
	if mode != site.mode {
		return access.Redirect{}, badMode()
	}
	verifier, challenge, err := pkce()
	if err != nil {
		return access.Redirect{}, errx.Wrap(err, "pkce", errx.TypeInternal)
	}
	state, err := token(24)
	if err != nil {
		return access.Redirect{}, errx.Wrap(err, "state", errx.TypeInternal)
	}
	nonce, err := token(24)
	if err != nil {
		return access.Redirect{}, errx.Wrap(err, "nonce", errx.TypeInternal)
	}
	sealed, err := s.seal(flow{Environment: in.Environment, Mode: in.Mode, Return: in.Return, Verifier: verifier, State: state, Nonce: nonce,
		Expires: s.opts.Now().Add(access.FlowTTL).Unix()})
	if err != nil {
		return access.Redirect{}, err
	}
	req := access.LoginRequest{Redirect: s.callbackURL(), State: state, Challenge: challenge, Nonce: nonce}
	if mode == release.AccessTeam {
		req.Organization = target.Organization.String()
	}
	return access.Redirect{Location: s.login.AuthorizeURL(req), Flow: sealed}, nil
}

// Callback finishes the sign-in: it decides whether the user may open the app
// and, if so, signs the ticket the gate takes.
func (s *Service) Callback(ctx context.Context, in access.Callback) (access.Outcome, error) {
	f, err := s.open(in.Flow)
	if err != nil || subtle.ConstantTimeCompare([]byte(f.State), []byte(in.State)) != 1 || in.State == "" || in.Code == "" || f.Nonce == "" {
		return access.Outcome{}, invalid()
	}
	// The environment may have moved since: decide against what is true now.
	site, err := s.place(ctx, f.Environment, f.Return)
	if err != nil {
		return access.Outcome{}, invalid()
	}
	target := site.target
	// The flow's mode must still be the one the cluster was told: after a
	// change of access the sign-ins in progress are refused (never a ticket
	// for a mode the app no longer has, and never one for a public app).
	if m := release.AccessMode(f.Mode); m != site.mode || !m.Protected() {
		return access.Outcome{Denied: true}, nil
	}
	who, err := s.login.Exchange(ctx, in.Code, s.callbackURL(), f.Verifier, f.Nonce)
	if err != nil {
		return access.Outcome{}, err
	}
	switch release.AccessMode(f.Mode) {
	case release.AccessTeam:
		if who.Organization == "" || !strings.EqualFold(who.Organization, target.Organization.String()) {
			return access.Outcome{Denied: true}, nil
		}
	case release.AccessOrganization:
		if who.Environment == "" || who.Environment != s.opts.IAMEnvironment {
			return access.Outcome{Denied: true}, nil
		}
	default:
		return access.Outcome{}, invalid()
	}
	id, err := token(16)
	if err != nil {
		return access.Outcome{}, errx.Wrap(err, "ticket id", errx.TypeInternal)
	}
	now := s.opts.Now()
	ticket, err := s.signer.Sign(access.Ticket{Issuer: s.opts.Issuer, Audience: site.audience, Subject: who.Subject, Email: who.Email, Name: who.Name,
		Organization: who.Organization, AppOrganization: target.Organization.String(), Environment: f.Environment, Mode: f.Mode,
		ID: id, IssuedAt: now, Expires: now.Add(access.TicketTTL)})
	if err != nil {
		return access.Outcome{}, errx.Wrap(err, "sign ticket", errx.TypeInternal)
	}
	q := url.Values{"ticket": {ticket}, "return": {returnPath(site.back)}}
	return access.Outcome{Location: site.origin + access.CallbackPath + "?" + q.Encode()}, nil
}

// site is an environment's app as the sign-in sees it.
type site struct {
	target release.AccessTarget
	// mode is the access the cluster was last told (public: no sign-in).
	mode release.AccessMode
	back *url.URL
	// origin is scheme://host[:port] of the app, as its URL says.
	origin string
	// audience is the ticket's `aud`: host[:port] of the app's URL, exactly
	// what the gate computes from its forwardAuth query (the host lowercased
	// and the port only when the agent was given one).
	audience string
}

// place finds the environment (without a tenant: the gate sends only its id)
// and checks `return` is an address of its app: the same scheme and the same
// effective host and port as the app's URL (an explicit default port, :443 on
// https or :80 on http, is the same as none), and https unless it is a
// development host. Anything else is an open redirect. The caller checks the
// mode (site.mode).
func (s *Service) place(ctx context.Context, environment, ret string) (site, error) {
	id, err := project.ParseEnvironmentID(environment)
	if err != nil {
		return site{}, errx.InvalidField("env", "env is not an environment id")
	}
	back, err := ParseReturn(ret)
	if err != nil {
		return site{}, err
	}
	target, err := s.targets.FindAccessTarget(ctx, id)
	if err != nil {
		return site{}, err
	}
	app, err := url.Parse(target.URL)
	if err != nil || app.Hostname() == "" || (app.Scheme != "https" && app.Scheme != "http") ||
		back.Scheme != app.Scheme || authority(back) != authority(app) {
		return site{}, badReturn("return is not an address of this app")
	}
	if back.Scheme != "https" && !s.opts.AllowHTTP && !DevHost(back.Hostname()) {
		return site{}, badReturn("return must be https")
	}
	host := strings.ToLower(app.Host)
	return site{target: target, mode: target.Mode, back: back, origin: app.Scheme + "://" + host, audience: host}, nil
}

// authority is the host and port u is reached at, lowercased, without the
// scheme's default port.
func authority(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return host
	}
	return net.JoinHostPort(host, port)
}

// ParseReturn reads `return`: an absolute http(s) URL with a host and no
// credentials. The host is lowercased.
func ParseReturn(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 4096 || strings.ContainsAny(raw, "\\\r\n\t ") {
		return nil, badReturn("return must be an absolute URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, badReturn("return must be an absolute URL")
	}
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

// returnPath is the path (and query) to land on: always starts with a single "/".
func returnPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	return p
}

// DevHost reports whether plain http is acceptable for host: this machine's
// own names and the k3d host alias.
func DevHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".k3d.internal") || host == "k3d.internal"
}

func badReturn(msg string) error {
	return errx.Validation(msg).WithCode(access.CodeBadReturn).WithField("return")
}

// badMode: the app is not served with the mode asked (it is public, or its
// access changed): there is no sign-in to run.
func badMode() error {
	return errx.Validation("this app is not protected with that mode").WithCode(access.CodeBadMode)
}

func invalid() error {
	return errx.Validation("el inicio de sesión expiró o no es válido").WithCode(access.CodeFlowInvalid)
}

func (s *Service) seal(f flow) (string, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return "", errx.Wrap(err, "seal flow", errx.TypeInternal)
	}
	nonce := make([]byte, s.box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errx.Wrap(err, "seal flow", errx.TypeInternal)
	}
	return base64.RawURLEncoding.EncodeToString(s.box.Seal(nonce, nonce, raw, []byte(access.FlowCookie))), nil
}

func (s *Service) open(sealed string) (flow, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < s.box.NonceSize() {
		return flow{}, invalid()
	}
	plain, err := s.box.Open(nil, raw[:s.box.NonceSize()], raw[s.box.NonceSize():], []byte(access.FlowCookie))
	if err != nil {
		return flow{}, invalid()
	}
	var f flow
	if json.Unmarshal(plain, &f) != nil || s.opts.Now().Unix() > f.Expires {
		return flow{}, invalid()
	}
	return f, nil
}

func pkce() (verifier, challenge string, err error) {
	verifier, err = token(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func token(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
