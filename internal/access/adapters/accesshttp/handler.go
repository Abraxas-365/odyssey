// Package accesshttp serves the access sign-in: the key set the gates verify
// tickets with and the two browser steps of the IAMKit login. All three are
// unauthenticated routes (the OAuth flow is the proof); it mounts only those
// in PublicRoutes. Without a signing key and IAMKit client they answer 404.
package accesshttp

import (
	"encoding/base64"
	"html"
	"log/slog"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
)

// PublicRoutes are the unauthenticated routes of this adapter.
var PublicRoutes = []string{"GET /.well-known/odicea-access-keys", "GET /access/authorize", "GET /access/callback"}

// Handler serves them. A nil commands/queries means the sign-in is off.
type Handler struct {
	commands access.Commands
	queries  access.Queries
	// secure marks the flow cookie Secure (the API is served over https).
	secure bool
}

func New(commands access.Commands, queries access.Queries, secure bool) *Handler {
	return &Handler{commands: commands, queries: queries, secure: secure}
}

// RegisterPublic mounts PublicRoutes on the app's root, outside auth.
func (h *Handler) RegisterPublic(r fiber.Router) {
	r.Get("/.well-known/odicea-access-keys", h.keys)
	r.Get("/access/authorize", h.authorize)
	r.Get("/access/callback", h.callback)
}

func (h *Handler) enabled() error {
	if h.commands == nil || h.queries == nil {
		return errx.NotFound("the access sign-in is not configured").WithCode(access.CodeNotConfigured)
	}
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

func (h *Handler) keys(c *fiber.Ctx) error {
	if err := h.enabled(); err != nil {
		return err
	}
	out := []jwk{}
	for _, k := range h.queries.Keys() {
		out = append(out, jwk{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(k.Public), Kid: k.ID, Use: "sig", Alg: "EdDSA"})
	}
	c.Set(fiber.HeaderCacheControl, "public, max-age=60")
	return c.JSON(fiber.Map{"keys": out})
}

func (h *Handler) authorize(c *fiber.Ctx) error {
	if err := h.enabled(); err != nil {
		return err
	}
	out, err := h.commands.Authorize(httpx.PublicContext(c), access.Authorize{Environment: c.Query("env"), Mode: c.Query("mode"), Return: c.Query("return")})
	if err != nil {
		return refuse(c, "authorize", err)
	}
	c.Cookie(&fiber.Cookie{Name: access.FlowCookie, Value: out.Flow, Path: "/access", MaxAge: int(access.FlowTTL / time.Second),
		HTTPOnly: true, Secure: h.secure, SameSite: fiber.CookieSameSiteLaxMode})
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Redirect(out.Location, http.StatusFound)
}

func (h *Handler) callback(c *fiber.Ctx) error {
	if err := h.enabled(); err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	// The flow is spent whatever happens: a retry starts again at the app.
	c.Cookie(&fiber.Cookie{Name: access.FlowCookie, Value: "", Path: "/access", MaxAge: -1, HTTPOnly: true, Secure: h.secure, SameSite: fiber.CookieSameSiteLaxMode})
	out, err := h.commands.Callback(httpx.PublicContext(c), access.Callback{Flow: c.Cookies(access.FlowCookie), State: c.Query("state"), Code: c.Query("code")})
	switch {
	case err != nil:
		return refuse(c, "callback", err)
	case out.Denied:
		return page(c, http.StatusForbidden, "No tienes acceso a esta app", "Tu cuenta no está autorizada para abrirla. Pide acceso a quien la publicó.")
	}
	return c.Redirect(out.Location, http.StatusFound)
}

// refuseText is the only thing a browser is told when the sign-in cannot go
// on, whatever the reason: an unknown environment, a return URL that is not
// the app's, an expired flow and a failure of ours must look the same, so the
// public routes tell nobody what exists. The specific code is in the logs.
const refuseText = "No pudimos abrir esta app. Vuelve a intentarlo desde su enlace."

// refuse answers a failed step with the generic Spanish page: 400 for
// anything the caller got wrong, the error's own 5xx otherwise.
func refuse(c *fiber.Ctx, step string, err error) error {
	status, code := http.StatusInternalServerError, ""
	var e *errx.Error
	if errx.As(err, &e) {
		status, code = e.HTTPStatus, e.Code
		if status < 500 {
			status = http.StatusBadRequest
		}
	}
	slog.Warn("access sign-in refused", "step", step, "code", code, "status", status, "error", err.Error())
	return page(c, status, "No pudimos abrir esta app", refuseText)
}

// page is a plain Spanish page for the person who just signed in.
func page(c *fiber.Ctx, status int, title, text string) error {
	c.Status(status)
	c.Type("html", "utf-8")
	return c.SendString(`<!doctype html><html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + html.EscapeString(title) + `</title><style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#1f2937}h1{font-size:1.4rem}</style></head>` +
		`<body><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(text) + `</p></body></html>`)
}
