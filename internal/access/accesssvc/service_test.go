package accesssvc_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/accesssvc"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessjwt"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type targets map[project.EnvironmentID]release.AccessTarget

func (t targets) FindAccessTarget(_ context.Context, e project.EnvironmentID) (release.AccessTarget, error) {
	if x, ok := t[e]; ok {
		return x, nil
	}
	return release.AccessTarget{}, errx.NotFound("environment not found")
}

// login is a fake IAMKit: it remembers the request and answers who.
type login struct {
	who      access.Identity
	err      error
	exchange int
	last     access.LoginRequest
	verifier string
	nonce    string
}

func (l *login) AuthorizeURL(r access.LoginRequest) string {
	l.last = r
	return "https://iam.example/oauth/authorize?state=" + r.State + "&org=" + r.Organization
}
func (l *login) Exchange(_ context.Context, code, redirect, verifier, nonce string) (access.Identity, error) {
	l.exchange++
	l.verifier = verifier
	l.nonce = nonce
	if l.err != nil {
		return access.Identity{}, l.err
	}
	return l.who, nil
}

type fixture struct {
	targets targets
	svc     *accesssvc.Service
	login   *login
	env     project.EnvironmentID
	org     identity.OrganizationID
	now     time.Time
	keys    []access.Key
}

func setup(t *testing.T, appURL string, opts accesssvc.Options) *fixture {
	t.Helper()
	f := &fixture{login: &login{}, env: project.NewEnvironmentID(), org: identity.NewOrganizationID(), now: time.Now().Truncate(time.Second)}
	signer, err := accessjwt.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.keys = signer.Keys()
	opts.Issuer = "https://api.example"
	opts.IAMEnvironment = "iam-env"
	opts.Now = func() time.Time { return f.now }
	f.targets = targets{f.env: {Environment: f.env, Organization: f.org, URL: appURL, Mode: release.AccessTeam}}
	f.svc, err = accesssvc.New(f.targets, signer, f.login, []byte("secret"), opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// serve sets the mode the cluster was last told.
func (f *fixture) serve(m release.AccessMode) {
	t := f.targets[f.env]
	t.Mode = m
	f.targets[f.env] = t
}

func (f *fixture) start(t *testing.T, mode, ret string) access.Redirect {
	t.Helper()
	out, err := f.svc.Authorize(context.Background(), access.Authorize{Environment: f.env.String(), Mode: mode, Return: ret})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAuthorizeValidatesTheQuestion(t *testing.T) {
	f := setup(t, "https://app.example", accesssvc.Options{})
	other := project.NewEnvironmentID().String()
	cases := []struct {
		name string
		in   access.Authorize
		code string
		kind errx.Type
	}{
		{"mode public", access.Authorize{Environment: f.env.String(), Mode: "public", Return: "https://app.example/"}, "", errx.TypeValidation},
		{"no mode", access.Authorize{Environment: f.env.String(), Return: "https://app.example/"}, "", errx.TypeValidation},
		{"env not an id", access.Authorize{Environment: "x", Mode: "team", Return: "https://app.example/"}, "", errx.TypeValidation},
		{"unknown env", access.Authorize{Environment: other, Mode: "team", Return: "https://app.example/"}, "", errx.TypeNotFound},
		{"other host", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "https://evil.example/"}, access.CodeBadReturn, errx.TypeValidation},
		{"host as a prefix", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "https://app.example.evil.com/"}, access.CodeBadReturn, errx.TypeValidation},
		{"userinfo trick", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "https://app.example@evil.example/"}, access.CodeBadReturn, errx.TypeValidation},
		{"backslash trick", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "https://evil.example\\@app.example/"}, access.CodeBadReturn, errx.TypeValidation},
		{"relative", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "/dashboard"}, access.CodeBadReturn, errx.TypeValidation},
		{"scheme-relative", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "//evil.example/"}, access.CodeBadReturn, errx.TypeValidation},
		{"javascript", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "javascript:alert(1)"}, access.CodeBadReturn, errx.TypeValidation},
		{"plain http", access.Authorize{Environment: f.env.String(), Mode: "team", Return: "http://app.example/"}, access.CodeBadReturn, errx.TypeValidation},
		{"empty", access.Authorize{Environment: f.env.String(), Mode: "team"}, access.CodeBadReturn, errx.TypeValidation},
	}
	for _, c := range cases {
		_, err := f.svc.Authorize(context.Background(), c.in)
		var e *errx.Error
		if !errx.As(err, &e) || e.Type != c.kind || (c.code != "" && e.Code != c.code) {
			t.Errorf("%s: %v, want %s %s", c.name, err, c.kind, c.code)
		}
	}
	if f.login.last.State != "" {
		t.Fatal("a refused question started a sign-in")
	}
}

func TestAuthorizeStartsTheSignInPerMode(t *testing.T) {
	f := setup(t, "https://app.example", accesssvc.Options{})
	team := f.start(t, "team", "https://app.example/a?b=1")
	if f.login.last.Organization != f.org.String() || f.login.last.Redirect != "https://api.example/access/callback" ||
		f.login.last.Challenge == "" || f.login.last.State == "" || team.Flow == "" {
		t.Fatalf("team request %+v", f.login.last)
	}
	f.serve(release.AccessOrganization)
	f.start(t, "organization", "https://app.example/")
	if f.login.last.Organization != "" {
		t.Fatalf("organization mode hints an organization: %+v", f.login.last)
	}
	// a uppercase host in the return is the same host
	f.serve(release.AccessTeam)
	f.start(t, "team", "https://APP.example/")
}

func TestDevHostsMayUseHTTP(t *testing.T) {
	for _, host := range []string{"localhost", "demo.localhost", "127.0.0.1", "app.k3d.internal"} {
		f := setup(t, "http://"+host+":8088", accesssvc.Options{})
		if _, err := f.svc.Authorize(context.Background(), access.Authorize{Environment: f.env.String(), Mode: "team", Return: "http://" + host + ":8088/x"}); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	f := setup(t, "http://app.example", accesssvc.Options{})
	if _, err := f.svc.Authorize(context.Background(), access.Authorize{Environment: f.env.String(), Mode: "team", Return: "http://app.example/"}); err == nil {
		t.Error("plain http accepted for a real host")
	}
	f = setup(t, "http://app.example", accesssvc.Options{AllowHTTP: true})
	if _, err := f.svc.Authorize(context.Background(), access.Authorize{Environment: f.env.String(), Mode: "team", Return: "http://app.example/"}); err != nil {
		t.Errorf("explicit dev flag: %v", err)
	}
}

func state(t *testing.T, loc string) string {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestCallbackAllowsAndSignsTheTicket(t *testing.T) {
	f := setup(t, "https://app.example:8443", accesssvc.Options{})
	start := f.start(t, "team", "https://app.example:8443/deep/link?x=1")
	f.login.who = access.Identity{Subject: "u1", Email: "a@b.c", Name: "Ana", Organization: strings.ToUpper(f.org.String()), Environment: "iam-env"}
	out, err := f.svc.Callback(context.Background(), access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"})
	if err != nil || out.Denied {
		t.Fatalf("callback %+v %v", out, err)
	}
	u, err := url.Parse(out.Location)
	if err != nil || u.Scheme != "https" || u.Host != "app.example:8443" || u.Path != access.CallbackPath || u.Query().Get("return") != "/deep/link?x=1" {
		t.Fatalf("location %s", out.Location)
	}
	claims, err := accessjwt.Verify(u.Query().Get("ticket"), f.keys[0].Public, "", "", f.now)
	if err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "app.example:8443" || claims["iss"] != "https://api.example" || claims["mode"] != "team" || claims["env"] != f.env.String() ||
		claims["app_org"] != f.org.String() || claims["sub"] != "u1" || claims["typ"] != access.TicketType || claims["jti"] == "" {
		t.Fatalf("claims %v", claims)
	}
	if f.login.verifier == "" {
		t.Fatal("no PKCE verifier reached the exchange")
	}
	// The nonce sent to IAMKit comes back from the sealed flow for the ID
	// token check, and differs from the state.
	if f.login.nonce == "" || f.login.nonce != f.login.last.Nonce || f.login.nonce == f.login.last.State {
		t.Fatalf("nonce %q, authorize sent %q (state %q)", f.login.nonce, f.login.last.Nonce, f.login.last.State)
	}
}

func TestCallbackDecisions(t *testing.T) {
	member := func(f *fixture) access.Identity {
		return access.Identity{Subject: "u", Organization: f.org.String(), Environment: "iam-env"}
	}
	cases := []struct {
		name   string
		mode   string
		who    func(*fixture) access.Identity
		denied bool
	}{
		{"team member", "team", member, false},
		{"team other organization", "team", func(f *fixture) access.Identity {
			return access.Identity{Subject: "u", Organization: identity.NewOrganizationID().String(), Environment: "iam-env"}
		}, true},
		{"team without organization", "team", func(f *fixture) access.Identity { return access.Identity{Subject: "u", Environment: "iam-env"} }, true},
		{"organization, same company", "organization", func(f *fixture) access.Identity {
			return access.Identity{Subject: "u", Organization: identity.NewOrganizationID().String(), Environment: "iam-env"}
		}, false},
		{"organization, other company", "organization", func(f *fixture) access.Identity {
			return access.Identity{Subject: "u", Organization: f.org.String(), Environment: "another-env"}
		}, true},
		{"organization, no environment", "organization", func(f *fixture) access.Identity { return access.Identity{Subject: "u"} }, true},
	}
	for _, c := range cases {
		f := setup(t, "https://app.example", accesssvc.Options{})
		f.serve(release.AccessMode(c.mode))
		start := f.start(t, c.mode, "https://app.example/")
		f.login.who = c.who(f)
		out, err := f.svc.Callback(context.Background(), access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"})
		if err != nil || out.Denied != c.denied || (c.denied && out.Location != "") || (!c.denied && out.Location == "") {
			t.Errorf("%s: %+v %v", c.name, out, err)
		}
	}
}

func TestCallbackRefusesABrokenFlow(t *testing.T) {
	f := setup(t, "https://app.example", accesssvc.Options{})
	start := f.start(t, "team", "https://app.example/")
	st := state(t, start.Location)
	f.login.who = access.Identity{Subject: "u", Organization: f.org.String()}
	ctx := context.Background()
	bad := map[string]access.Callback{
		"no cookie":      {State: st, Code: "c"},
		"garbage cookie": {Flow: "AAAA", State: st, Code: "c"},
		"other state":    {Flow: start.Flow, State: st + "x", Code: "c"},
		"no state":       {Flow: start.Flow, Code: "c"},
		"no code":        {Flow: start.Flow, State: st},
		"tampered":       {Flow: start.Flow[:len(start.Flow)-2] + "AA", State: st, Code: "c"},
	}
	for name, in := range bad {
		_, err := f.svc.Callback(ctx, in)
		var e *errx.Error
		if !errx.As(err, &e) || e.Code != access.CodeFlowInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.login.exchange != 0 {
		t.Fatalf("a broken flow reached IAMKit %d times", f.login.exchange)
	}
	// a flow of another installation (other secret) is not ours
	other := setup(t, "https://app.example", accesssvc.Options{})
	if _, err := other.svc.Callback(ctx, access.Callback{Flow: start.Flow, State: st, Code: "c"}); err == nil {
		t.Fatal("a sealed flow of another key opened")
	}
	// expired
	f.now = f.now.Add(access.FlowTTL + time.Second)
	if _, err := f.svc.Callback(ctx, access.Callback{Flow: start.Flow, State: st, Code: "c"}); err == nil {
		t.Fatal("an expired flow opened")
	}
}

func TestCallbackRechecksTheEnvironment(t *testing.T) {
	// the app moved to another host after the sign-in started
	f := setup(t, "https://app.example", accesssvc.Options{})
	start := f.start(t, "team", "https://app.example/")
	moved := targets{f.env: {Environment: f.env, Organization: f.org, URL: "https://moved.example"}}
	signer, _ := accessjwt.New(make([]byte, 32))
	svc, err := accesssvc.New(moved, signer, f.login, []byte("secret"), accesssvc.Options{Issuer: "https://api.example", Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.login.who = access.Identity{Subject: "u", Organization: f.org.String()}
	if _, err := svc.Callback(context.Background(), access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"}); err == nil {
		t.Fatal("a ticket was issued for a host that is no longer the app's")
	}
}

func TestCallbackPassesIAMKitFailures(t *testing.T) {
	f := setup(t, "https://app.example", accesssvc.Options{})
	start := f.start(t, "team", "https://app.example/")
	f.login.err = errx.Validation("expired").WithCode(access.CodeFlowInvalid)
	_, err := f.svc.Callback(context.Background(), access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"})
	var e *errx.Error
	if !errx.As(err, &e) || e.Code != access.CodeFlowInvalid {
		t.Fatalf("%v", err)
	}
}

// The sign-in only runs for the mode the cluster was last told. A public app
// has none; and a flow started before the access changed gets no ticket.
func TestTheModeIsWhatTheClusterWasTold(t *testing.T) {
	ctx := context.Background()
	f := setup(t, "https://app.example", accesssvc.Options{})
	ask := func(mode string) error {
		_, err := f.svc.Authorize(ctx, access.Authorize{Environment: f.env.String(), Mode: mode, Return: "https://app.example/"})
		return err
	}
	// the app is team: a gate that asks for organization is out of date
	if err := ask("organization"); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("organization on a team app: %v", err)
	}
	// public: no sign-in at all, whatever the gate asks
	f.serve(release.AccessPublic)
	for _, mode := range []string{"team", "organization"} {
		if err := ask(mode); !errx.IsType(err, errx.TypeValidation) {
			t.Fatalf("%s on a public app: %v", mode, err)
		}
	}
	if f.login.last.State != "" {
		t.Fatal("a refused question started a sign-in")
	}

	// a flow in progress when the access changes is refused at the callback,
	// before IAMKit is asked anything
	for _, now := range []release.AccessMode{release.AccessPublic, release.AccessOrganization} {
		f.serve(release.AccessTeam)
		start := f.start(t, "team", "https://app.example/")
		f.serve(now)
		f.login.who = access.Identity{Subject: "u", Organization: f.org.String(), Environment: "iam-env"}
		before := f.login.exchange
		out, err := f.svc.Callback(ctx, access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"})
		if err != nil || !out.Denied || out.Location != "" || f.login.exchange != before {
			t.Errorf("flow started as team, app now %s: %+v %v (exchanges %d)", now, out, err, f.login.exchange-before)
		}
	}
}

// `return` is the app's address as its URL says: same scheme, same effective
// host and port (an explicit default port is the same as none). The ticket's
// aud is that host[:port], exactly what the gate computes.
func TestReturnIsTheAppsAuthority(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, app, ret string
		allowHTTP      bool
		ok             bool
		aud            string
	}{
		{"same", "https://app.example", "https://app.example/x", false, true, "app.example"},
		{"explicit default port", "https://app.example", "https://app.example:443/x", false, true, "app.example"},
		// the agent writes the port it was given even when it is the default, and
		// the gate then expects aud host:443: the ticket follows the app's URL
		{"app url carries the default port", "https://app.example:443", "https://app.example/x", false, true, "app.example:443"},
		{"other port", "https://app.example", "https://app.example:8443/x", false, false, ""},
		{"app on a port, return without", "https://app.example:8443", "https://app.example/x", false, false, ""},
		{"app on a port", "https://app.example:8443", "https://APP.example:8443/x", false, true, "app.example:8443"},
		{"other port than the app's", "https://app.example:8443", "https://app.example:9443/x", false, false, ""},
		{"http against an https app", "https://app.example", "http://app.example/x", true, false, ""},
		{"https against an http app", "http://app.example", "https://app.example/x", true, false, ""},
		{"http against an http app on :80", "http://app.example", "http://app.example:80/x", true, true, "app.example"},
		{"443 is not the default of http", "http://app.example", "http://app.example:443/x", true, false, ""},
		{"80 is not the default of https", "https://app.example", "https://app.example:80/x", false, false, ""},
		{"dev port", "http://demo.localhost:8088", "http://demo.localhost:8088/p", false, true, "demo.localhost:8088"},
		{"dev port missing", "http://demo.localhost:8088", "http://demo.localhost/p", false, false, ""},
	}
	for _, c := range cases {
		f := setup(t, c.app, accesssvc.Options{AllowHTTP: c.allowHTTP})
		start, err := f.svc.Authorize(ctx, access.Authorize{Environment: f.env.String(), Mode: "team", Return: c.ret})
		if (err == nil) != c.ok {
			t.Errorf("%s: authorize %v, want ok=%v", c.name, err, c.ok)
			continue
		}
		if !c.ok {
			if !errx.IsType(err, errx.TypeValidation) {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		f.login.who = access.Identity{Subject: "u", Organization: f.org.String(), Environment: "iam-env"}
		out, err := f.svc.Callback(ctx, access.Callback{Flow: start.Flow, State: state(t, start.Location), Code: "c"})
		if err != nil || out.Denied {
			t.Errorf("%s: callback %+v %v", c.name, out, err)
			continue
		}
		u, _ := url.Parse(out.Location)
		claims, err := accessjwt.Verify(u.Query().Get("ticket"), f.keys[0].Public, "", "", f.now)
		if err != nil || claims["aud"] != c.aud {
			t.Errorf("%s: aud %v (%v), want %q", c.name, claims["aud"], err, c.aud)
		}
		// the ticket goes back to the app's own origin, default port dropped
		if u.Host != c.aud {
			t.Errorf("%s: redirected to %s, want %s", c.name, u.Host, c.aud)
		}
	}
}
