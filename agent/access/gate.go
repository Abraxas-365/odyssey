package access

import (
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gate is the HTTP handler behind Traefik's forwardAuth: /verify and /healthz.
type Gate struct {
	// Issuer is the control plane's public base URL: where browsers are
	// sent to sign in.
	Issuer   string
	Verifier *Verifier
	Cookies  *Codec
	Log      *slog.Logger
	Now      func() time.Time
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Handler serves /verify and /healthz.
func (g *Gate) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/verify", g.verify)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// request is one forwardAuth question. Everything about the app comes from
// the forwardAuth address the agent wrote (the query); the request only
// supplies the path and is checked against it.
type request struct {
	want   Expect // Host is the app's host name (no port), from the query
	scheme string // http or https, from the query (never from the request)
	port   string // public port, "" for the scheme's default
	uri    *url.URL
}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// hostName lowercases a Host / X-Forwarded-Host value and drops its port.
func hostName(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// publicURL is the app's URL for path (scheme, host and port from the query).
func (r request) publicURL(path string) string {
	host := r.want.Host
	if r.port != "" {
		host += ":" + r.port
	}
	u := url.URL{Scheme: r.scheme, Host: host, Path: "/"}
	if p, err := url.ParseRequestURI(path); err == nil {
		u.Path, u.RawPath, u.RawQuery = p.Path, p.RawPath, p.RawQuery
	}
	return u.String()
}

// parse checks the question. Anything odd is refused (false): the gate
// fails closed. The request's own host must be the app's host: a client that
// reaches the gate's verify through another app's route, or with a forged
// X-Forwarded-Host, gets nothing. Traefik (trustForwardHeader: false) sets
// X-Forwarded-Host/-Method/-Uri itself from the real request.
func (g *Gate) parse(r *http.Request) (request, bool) {
	q := r.URL.Query()
	req := request{
		want:   Expect{Host: q.Get("host"), Env: q.Get("env"), AppOrg: q.Get("org"), Mode: q.Get("mode")},
		scheme: q.Get("scheme"), port: q.Get("port"),
	}
	if n, err := strconv.Atoi(req.port); req.port != "" && (err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != req.port) {
		return req, false
	}
	uri, err := url.ParseRequestURI(r.Header.Get("X-Forwarded-Uri"))
	if err != nil || !strings.HasPrefix(uri.Path, "/") || strings.HasPrefix(uri.Path, "//") {
		uri = &url.URL{Path: "/"}
	}
	req.uri = uri
	req.want.Aud = req.want.Host
	if req.port != "" {
		req.want.Aud += ":" + req.port
	}
	switch {
	case !hostRE.MatchString(req.want.Host), req.scheme != "http" && req.scheme != "https":
		return req, false
	case req.want.Mode != ModeTeam && req.want.Mode != ModeOrganization, req.want.Env == "", req.want.AppOrg == "":
		return req, false
	case hostName(r.Header.Get("X-Forwarded-Host")) != req.want.Host:
		return req, false
	case r.Header.Get("X-Forwarded-Method") == "":
		return req, false
	}
	return req, true
}

func (g *Gate) verify(w http.ResponseWriter, r *http.Request) {
	req, ok := g.parse(r)
	if !ok {
		g.Log.Warn("verify: refused", "forwarded_host", r.Header.Get("X-Forwarded-Host"), "host", r.URL.Query().Get("host"))
		http.Error(w, "forbidden", http.StatusForbidden) // fail closed
		return
	}
	if req.uri.Path == CallbackPath {
		g.callback(w, r, req)
		return
	}
	if c, err := r.Cookie(CookieName); err == nil {
		if s, err := g.Cookies.Open(c.Value, g.now()); err == nil && g.matches(s, req.want) {
			h := w.Header()
			h.Set(HeaderUser, s.User)
			h.Set(HeaderEmail, s.Email)
			h.Set(HeaderName, s.Name)
			h.Set(HeaderOrg, s.UserOrg)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	// No (valid) session: browsers go and sign in, anything else is refused.
	if m := r.Header.Get("X-Forwarded-Method"); m != http.MethodGet && m != http.MethodHead {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, g.authorizeURL(req, req.uri.RequestURI()), http.StatusFound)
}

// matches: the session is for this very app.
func (g *Gate) matches(s Session, want Expect) bool {
	return s.Host == want.Host && s.Env == want.Env && s.Mode == want.Mode && s.AppOrg == want.AppOrg &&
		s.User != "" && (want.Mode != ModeTeam || s.UserOrg == want.AppOrg)
}

// authorizeURL sends the browser to sign in and come back to path.
func (g *Gate) authorizeURL(req request, path string) string {
	q := url.Values{"env": {req.want.Env}, "mode": {req.want.Mode}, "return": {req.publicURL(path)}}
	return strings.TrimRight(g.Issuer, "/") + "/access/authorize?" + q.Encode()
}

func (g *Gate) callback(w http.ResponseWriter, r *http.Request, req request) {
	q := req.uri.Query()
	back := safeReturn(q.Get("return"))
	t, err := g.Verifier.Verify(r.Context(), q.Get("ticket"), req.want)
	if err != nil {
		// The error says why (never the ticket itself).
		g.Log.Warn("callback: ticket refused", "host", req.want.Host, "env", req.want.Env, "reason", err.Error())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><p>No pudimos validar tu acceso. ` +
			`<a href="` + html.EscapeString(g.authorizeURL(req, back)) + `">Inicia sesión de nuevo</a>.</p>`))
		return
	}
	now := g.now()
	value := g.Cookies.Seal(Session{
		Host: req.want.Host, Env: req.want.Env, Mode: req.want.Mode, AppOrg: req.want.AppOrg,
		User: t.Subject, Email: t.Email, Name: t.Name, UserOrg: t.Org, Expires: now.Add(SessionTTL).Unix(),
	})
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: int(SessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: req.scheme == "https",
	})
	g.Log.Info("callback: signed in", "host", req.want.Host, "env", req.want.Env, "mode", req.want.Mode)
	http.Redirect(w, r, back, http.StatusFound)
}

// safeReturn accepts only a path on the same host: it starts with a single
// "/" and holds no backslash or control character. Anything else is "/".
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	u, err := url.ParseRequestURI(p)
	if err != nil || u.Host != "" || u.Scheme != "" || u.Path == CallbackPath {
		return "/"
	}
	return p
}
