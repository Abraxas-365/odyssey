package bootstrap_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	gate "github.com/Abraxas-365/odyssey/agent/access"
	"github.com/Abraxas-365/odyssey/internal/access"
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

// TestTheGateAcceptsWhatTheControlPlaneSigns runs the real gate (agent/access)
// against our signer, end to end: the return URL the gate builds from its
// forwardAuth query goes through /access/authorize and /access/callback, and
// the ticket we sign has to verify there, for an app on the default port, an
// app on a development port, and a return that spells the default port out.
// The issuer is configured with a trailing slash on both sides.
func TestTheGateAcceptsWhatTheControlPlaneSigns(t *testing.T) {
	for _, c := range []struct {
		name   string
		domain string
		// spell rewrites the app's URL into the return URL the browser sends
		spell func(host string) string
	}{
		{"default port", "apps.odyssey.localhost", func(h string) string { return h }},
		{"explicit :443", "apps.odyssey.localhost", func(h string) string { return h + ":443" }},
		{"development port", "apps.odyssey.localhost:8088", func(h string) string { return h }},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := pgtest.DB(t)
			ctx := context.Background()
			if err := bootstrap.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			cluster := releasefake.New(releasefake.Options{Domain: c.domain, Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
			iam := &iamStub{}
			platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
				Workflow:     workflowpg.Options{PollInterval: 20 * time.Millisecond},
				Cluster:      cluster,
				Access:       config.Access{SigningKey: bytes.Repeat([]byte{9}, 32), ClientID: "c", ClientSecret: "s"},
				AccessIssuer: "https://odicea.test/",
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
			res = checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image("1"), "port": 8080})
			httpxtest.Want(t, res, http.StatusAccepted)
			var appURL string
			for deadline := time.Now().Add(15 * time.Second); appURL == ""; time.Sleep(20 * time.Millisecond) {
				cur := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil))
				if cur.Active == nil && cur.URL != "" {
					appURL = cur.URL
				}
				if time.Now().After(deadline) {
					t.Fatal("the app never went live")
				}
			}
			u, err := url.Parse(appURL)
			if err != nil {
				t.Fatal(err)
			}

			// The gate, configured the way the agent configures it: the issuer
			// with a trailing slash (a mistake of the person who set it up),
			// the keys fetched from the control plane's own route.
			keys := httpxtest.Do(t, app, http.MethodGet, "/.well-known/odicea-access-keys", "", nil)
			jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(keys.Body)
			}))
			t.Cleanup(jwks.Close)
			cookieKey := bytes.Repeat([]byte{3}, 32)
			codec, err := gate.NewCodec(cookieKey)
			if err != nil {
				t.Fatal(err)
			}
			g := &gate.Gate{Issuer: "https://odicea.test/", Cookies: codec, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Verifier: &gate.Verifier{Issuer: "https://odicea.test/", JWKSURL: jwks.URL}}
			handler := g.Handler()
			// ask is one forwardAuth question for the app, as Traefik sends it.
			ask := func(uri string, cookie *http.Cookie) *httptest.ResponseRecorder {
				q := url.Values{"env": {env.ID.String()}, "org": {org.String()}, "mode": {"team"}, "host": {strings.ToLower(u.Hostname())}, "scheme": {u.Scheme}}
				if p := u.Port(); p != "" {
					q.Set("port", p)
				}
				r := httptest.NewRequest(http.MethodGet, "/verify?"+q.Encode(), nil)
				r.Header.Set("X-Forwarded-Host", u.Host)
				r.Header.Set("X-Forwarded-Uri", uri)
				r.Header.Set("X-Forwarded-Method", http.MethodGet)
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}

			// 1. A browser with no session is sent to authorize with the return URL the gate builds.
			w := ask("/panel?x=1", nil)
			if w.Code != http.StatusFound {
				t.Fatalf("no session: %d %s", w.Code, w.Body)
			}
			authorize, err := url.Parse(w.Header().Get("Location"))
			if err != nil || authorize.Path != "/access/authorize" {
				t.Fatalf("gate redirect %q", w.Header().Get("Location"))
			}
			q := authorize.Query()
			ret, err := url.Parse(q.Get("return"))
			if err != nil || ret.Host != u.Host {
				t.Fatalf("gate return %q for app %s", q.Get("return"), appURL)
			}
			// a browser may spell the default port out; the gate's own URL never does
			spelled := ret.Scheme + "://" + c.spell(ret.Host) + ret.RequestURI()
			q.Set("return", spelled)
			if c.name == "explicit :443" {
				// the app's URL has no port, so a return with :443 is the same app
				if !strings.HasSuffix(strings.SplitN(spelled, "/", 4)[2], ":443") {
					t.Fatalf("test setup: %q", spelled)
				}
			}
			res = checked(t, app, http.MethodGet, "/access/authorize?"+q.Encode(), "", nil)
			httpxtest.Want(t, res, http.StatusFound)
			var flow string
			for _, ck := range strings.Split(res.Header.Get("Set-Cookie"), ", ") {
				if v, ok := strings.CutPrefix(ck, access.FlowCookie+"="); ok {
					flow = strings.SplitN(v, ";", 2)[0]
				}
			}
			if flow == "" || iam.last.Redirect != "https://odicea.test/access/callback" {
				t.Fatalf("authorize %v, iam %+v", res.Header, iam.last)
			}

			// 2. IAMKit sends the person back; we sign a ticket for this app.
			iam.who = access.Identity{Subject: "u1", Email: "ana@x.pe", Name: "Ana", Organization: org.String(), Environment: "iam-env"}
			cb := "/access/callback?code=c1&state=" + url.QueryEscape(iam.last.State)
			res = httpxtest.DoWithHeaders(t, app, http.MethodGet, cb, "", nil, map[string]string{"Cookie": access.FlowCookie + "=" + flow})
			validate(t, http.MethodGet, cb, res)
			httpxtest.Want(t, res, http.StatusFound)
			back, err := url.Parse(res.Header.Get("Location"))
			if err != nil || back.Host != u.Host || back.Path != gate.CallbackPath {
				t.Fatalf("callback location %q, app %s", res.Header.Get("Location"), appURL)
			}

			// 3. The gate takes the ticket: it opens a session for this very app
			// and sends the browser back to the app's public URL.
			w = ask(back.RequestURI(), nil)
			if w.Code != http.StatusFound || w.Header().Get("Location") != strings.TrimRight(appURL, "/")+"/panel?x=1" {
				t.Fatalf("the gate refused our ticket: %d %s %v", w.Code, w.Body, w.Header())
			}
			var session *http.Cookie
			for _, ck := range w.Result().Cookies() {
				if ck.Name == gate.CookieName {
					session = ck
				}
			}
			if session == nil {
				t.Fatalf("no session cookie: %v", w.Header())
			}
			if w = ask("/panel", session); w.Code != http.StatusOK {
				t.Fatalf("the session does not open the app: %d", w.Code)
			}
			// a ticket opens once
			if w = ask(back.RequestURI(), nil); w.Code != http.StatusForbidden {
				t.Fatalf("a ticket was taken twice: %d", w.Code)
			}
		})
	}
}
