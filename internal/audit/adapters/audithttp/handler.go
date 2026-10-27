// Package audithttp exposes the activity trail over HTTP (docs/api.md).
package audithttp

import (
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/gofiber/fiber/v2"
)

type Handler struct{ queries audit.Queries }

func New(queries audit.Queries) *Handler { return &Handler{queries: queries} }

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/audit", httpx.RequirePermissions(permission.AuditRead))
	g.Get("/", h.list)
	g.Get("/verify", h.verify)
}

// list: newest first. Filters: subject, type (repeatable), actor_id,
// correlation_id, from/to (dates or RFC 3339), search (in the summary).
func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	filter := audit.Filter{
		Subject:       strings.TrimSpace(c.Query("subject")),
		Types:         httpx.QueryValues(c, "type"),
		CorrelationID: strings.TrimSpace(c.Query("correlation_id")),
	}
	if raw := strings.TrimSpace(c.Query("actor_id")); raw != "" {
		if filter.Actor, err = identity.ParseUserID(raw); err != nil {
			return errx.InvalidField("actor_id", "actor_id must be a valid UUID")
		}
	}
	if filter.From, filter.Before, err = httpx.TimeRange(c, "from", "to", time.UTC); err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	result, err := h.queries.List(c.UserContext(), org, filter, page)
	if err != nil {
		return err
	}
	return c.JSON(result)
}

func (h *Handler) verify(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	v, err := h.queries.Verify(c.UserContext(), org)
	if err != nil {
		return err
	}
	return c.JSON(v)
}
