package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/access/adapters/accessjwt"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// iamStub is IAMKit's side of the access sign-in: it records the request and
// answers who signed in.
type iamStub struct {
	who  access.Identity
	last access.LoginRequest
}

func (s *iamStub) AuthorizeURL(r access.LoginRequest) string {
	s.last = r
	return "https://iam.test/oauth/authorize?state=" + url.QueryEscape(r.State)
}
func (s *iamStub) Exchange(_ context.Context, _, _, _, nonce string) (access.Identity, error) {
	if nonce == "" || nonce != s.last.Nonce {
		return access.Identity{}, errors.New("the flow lost its nonce")
	}
	return s.who, nil
}

// TestAccessSignIn drives the public sign-in routes through the real server
// against a live environment: the tenant comes from our records (the gate
// sends only an environment id), the return URL must be the app's, and the
// ticket verifies with the published keys.
func TestAccessSignIn(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	iam := &iamStub{}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:     workflowpg.Options{PollInterval: 20 * time.Millisecond},
		Cluster:      cluster,
		Access:       config.Access{SigningKey: bytes.Repeat([]byte{9}, 32), ClientID: "c", ClientSecret: "s"},
		AccessIssuer: "https://odicea.test",
		IAM:          config.IAM{Environment: "iam-env"},
		AccessLogin:  iam,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)

	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	authorize := func(env, mode, ret string) httpxtest.Response {
		q := url.Values{"env": {env}, "mode": {mode}, "return": {ret}}
		return checked(t, app, http.MethodGet, "/access/authorize?"+q.Encode(), "", nil)
	}

	// Before anything is online the environment has no address: nothing to sign
	// in to, and the answer is the generic page, the same as for a bad host.
	res = authorize(env.ID.String(), "team", "https://portal.apps.odyssey.localhost/")
	httpxtest.Want(t, res, http.StatusBadRequest)
	unknown := authorize(project.NewEnvironmentID().String(), "team", "https://portal.apps.odyssey.localhost/")
	if !bytes.Equal(res.Body, unknown.Body) {
		t.Fatalf("an unknown environment looks different:\n%s\n%s", res.Body, unknown.Body)
	}

	res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080})
	httpxtest.Want(t, res, http.StatusAccepted)
	id := httpxtest.JSON[release.Deployment](t, res).ID
	var appURL string
	for deadline := time.Now().Add(15 * time.Second); appURL == ""; time.Sleep(20 * time.Millisecond) {
		res := checked(t, app, http.MethodGet, base+"/current", token, nil)
		if c := httpxtest.JSON[release.Current](t, res); c.Active == nil && c.URL != "" {
			appURL = c.URL
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment %s never went live", id)
		}
	}
	host := strings.TrimPrefix(appURL, "https://")
	// serve changes the access and waits until the cluster was told (the
	// sign-in follows what the cluster was told, not the latest request).
	serve := func(mode string) {
		t.Helper()
		res := checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": mode})
		httpxtest.Want(t, res, http.StatusAccepted)
		want := httpxtest.JSON[struct {
			Settings struct{ Revision int64 }
		}](t, res).Settings.Revision
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			c := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
			if c.Active == nil && c.AppliedSettingsRevision != nil && *c.AppliedSettingsRevision >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never reached the cluster", mode)
			}
		}
	}

	// The published keys.
	res = checked(t, app, http.MethodGet, "/.well-known/odicea-access-keys", "", nil)
	httpxtest.Want(t, res, http.StatusOK)
	keys := httpxtest.JSON[struct{ Keys []struct{ X, Kid string } }](t, res).Keys
	if len(keys) != 1 {
		t.Fatalf("keys %s", res.Body)
	}
	pub, err := base64.RawURLEncoding.DecodeString(keys[0].X)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key %q: %v", keys[0].X, err)
	}

	// Refused questions never reach IAMKit and never set a cookie.
	for name, c := range map[string][3]string{
		"another host":   {env.ID.String(), "team", "https://evil.example/"},
		"host prefix":    {env.ID.String(), "team", appURL + ".evil.example/"},
		"public":         {env.ID.String(), "public", appURL + "/"},
		"bad env":        {"nope", "team", appURL + "/"},
		"unknown env":    {project.NewEnvironmentID().String(), "team", appURL + "/"},
		"relative":       {env.ID.String(), "team", "/panel"},
		"missing return": {env.ID.String(), "team", ""},
	} {
		res := authorize(c[0], c[1], c[2])
		if res.Status < 400 || res.Header.Get("Set-Cookie") != "" || res.Header.Get("Location") != "" {
			t.Errorf("%s: %d %v %s", name, res.Status, res.Header, res.Body)
		}
	}
	if iam.last.State != "" {
		t.Fatal("a refused question reached IAMKit")
	}

	callback := func(flow, state string) httpxtest.Response {
		h := map[string]string{}
		if flow != "" {
			h["Cookie"] = access.FlowCookie + "=" + flow
		}
		res := httpxtest.DoWithHeaders(t, app, http.MethodGet, "/access/callback?code=c1&state="+url.QueryEscape(state), "", nil, h)
		validate(t, http.MethodGet, "/access/callback?code=c1&state="+url.QueryEscape(state), res)
		return res
	}
	flowOf := func(res httpxtest.Response) string {
		t.Helper()
		for _, c := range strings.Split(res.Header.Get("Set-Cookie"), ", ") {
			if v, ok := strings.CutPrefix(c, access.FlowCookie+"="); ok {
				return strings.SplitN(v, ";", 2)[0]
			}
		}
		t.Fatalf("no flow cookie in %v", res.Header)
		return ""
	}

	// A person of the app's organization gets a ticket for exactly this app.
	res = authorize(env.ID.String(), "team", appURL+"/panel?x=1")
	httpxtest.Want(t, res, http.StatusFound)
	if !strings.HasPrefix(res.Header.Get("Location"), "https://iam.test/oauth/authorize") || iam.last.Organization != org.String() ||
		iam.last.Redirect != "https://odicea.test/access/callback" || iam.last.Challenge == "" {
		t.Fatalf("authorize %v %+v", res.Header, iam.last)
	}
	flow, state := flowOf(res), iam.last.State
	iam.who = access.Identity{Subject: "u1", Email: "ana@x.pe", Name: "Ana", Organization: org.String(), Environment: "iam-env"}
	res = callback(flow, state)
	httpxtest.Want(t, res, http.StatusFound)
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || loc.Host != host || loc.Path != access.CallbackPath || loc.Query().Get("return") != "/panel?x=1" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("callback %v", res.Header)
	}
	claims, err := accessjwt.Verify(loc.Query().Get("ticket"), ed25519.PublicKey(pub), host, "https://odicea.test", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if claims["env"] != env.ID.String() || claims["app_org"] != org.String() || claims["mode"] != "team" || claims["sub"] != "u1" {
		t.Fatalf("claims %v", claims)
	}

	// Someone from another organization is told no, with no ticket.
	res = authorize(env.ID.String(), "team", appURL+"/")
	iam.who = access.Identity{Subject: "u2", Organization: identity.NewOrganizationID().String(), Environment: "iam-env"}
	res = callback(flowOf(res), iam.last.State)
	if httpxtest.Want(t, res, http.StatusForbidden); strings.Contains(string(res.Body), "ticket") || res.Header.Get("Location") != "" {
		t.Fatalf("denied %v %s", res.Header, res.Body)
	}
	// ...but organization mode lets the whole company in, once the cluster
	// was told; before that the gate's question is out of date.
	httpxtest.Want(t, authorize(env.ID.String(), "organization", appURL+"/"), http.StatusBadRequest)
	serve("organization")
	httpxtest.Want(t, authorize(env.ID.String(), "team", appURL+"/"), http.StatusBadRequest)
	res = authorize(env.ID.String(), "organization", appURL+"/")
	if iam.last.Organization != "" {
		t.Fatalf("organization mode hinted %q", iam.last.Organization)
	}
	httpxtest.Want(t, callback(flowOf(res), iam.last.State), http.StatusFound)
	// A flow that started as organization is refused once the app is team again.
	res = authorize(env.ID.String(), "organization", appURL+"/")
	stale := flowOf(res)
	staleState := iam.last.State
	serve("team")
	httpxtest.Want(t, callback(stale, staleState), http.StatusForbidden)

	// The flow is not replayable for another state, and not without its cookie.
	res = authorize(env.ID.String(), "team", appURL+"/")
	flow = flowOf(res)
	httpxtest.Want(t, callback(flow, "other"), http.StatusBadRequest)
	httpxtest.Want(t, callback("", iam.last.State), http.StatusBadRequest)
}

// TestAccessSignInIsOffWithoutItsConfiguration: the routes are mounted but
// answer 404, and the settings say the login is not available.
func TestAccessSignInIsOffWithoutItsConfiguration(t *testing.T) {
	app := bootstrap.NewServer(assemble(t), config.Config{}, httpxtest.Auth(), nil)
	for _, p := range []string{"/.well-known/odicea-access-keys", "/access/authorize?env=e&mode=team&return=x", "/access/callback?code=c&state=s"} {
		res := checked(t, app, http.MethodGet, p, "", nil)
		if httpxtest.Want(t, res, http.StatusNotFound); httpxtest.ErrorCode(res) != access.CodeNotConfigured {
			t.Errorf("%s: %s", p, res.Body)
		}
	}
}
