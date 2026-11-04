package server

import (
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/gofiber/fiber/v2"
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
func (a Assistant) Register(r fiber.Router) {
	r.Get("/assistant", httpx.RequirePermissions(permission.ProjectsRead), func(c *fiber.Ctx) error {
		return c.JSON(a.Info)
	})
}

// RegisterPublic mounts the skill download: the skill holds no secrets and
// the assistant's installer fetches it with curl, without a token.
func (a Assistant) RegisterPublic(r fiber.Router) { r.Get("/skills/odicea.tar.gz", a.skill) }

func (a Assistant) skill(c *fiber.Ctx) error {
	etag := strconv.Quote(a.Version)
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, "public, max-age=300")
	if matches(c.Get(fiber.HeaderIfNoneMatch), etag) {
		return c.SendStatus(fiber.StatusNotModified)
	}
	c.Set(fiber.HeaderContentType, "application/gzip")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="odicea.tar.gz"`)
	return c.Send(a.Archive)
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
