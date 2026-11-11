package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// Assistant serves "Conecta tu asistente": GET /api/v1/assistant (what the
// console shows to connect an assistant) and the skill's archive at
// GET /skills/odicea.tar.gz. Both are static for the life of the process.
type Assistant struct {
	Info    assistant.Info
	Archive []byte
	// Version is the archive's version: its ETag.
	Version string
}

// Register mounts the authenticated route.
func (a Assistant) Register(r *httpx.Router) {
	r.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/assistant", func(w http.ResponseWriter, _ *http.Request) error {
		return httpx.JSON(w, a.Info)
	})
}

// RegisterPublic mounts the skill download: the skill holds no secrets and
// the assistant's installer fetches it with curl, without a token.
func (a Assistant) RegisterPublic(r *httpx.Router) { r.Get("/skills/odicea.tar.gz", a.skill) }

func (a Assistant) skill(w http.ResponseWriter, r *http.Request) error {
	etag := strconv.Quote(a.Version)
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, max-age=300")
	if matches(r.Header.Get("If-None-Match"), etag) {
		return httpx.Status(w, http.StatusNotModified)
	}
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="odicea.tar.gz"`)
	return httpx.Send(w, a.Archive)
}

// matches reports whether an If-None-Match header names etag (RFC 9110
// §13.1.2): a list, `*`, and weak validators (W/"…") compare equal for GET.
func matches(header, etag string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}
