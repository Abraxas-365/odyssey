// Package audithttp exposes the activity trail over HTTP (docs/api.md).
package audithttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/google/uuid"
)

type Handler struct{ queries audit.Queries }

func New(queries audit.Queries) *Handler { return &Handler{queries: queries} }

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/audit", httpx.RequirePermissions(permission.AuditRead))
	g.Get("/", h.list)
	g.Get("/verify", h.verify)
	g.Get("/summary", h.summary)
	g.Get("/people", h.people)
}

// list: newest first. Filters: subject, type (repeatable), actor_id,
// correlation_id, project_id, channel, from/to (dates or RFC 3339), search
// (in the summary).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	filter := audit.Filter{
		Subject:       strings.TrimSpace(q.Get("subject")),
		Types:         httpx.QueryValues(r, "type"),
		CorrelationID: strings.TrimSpace(q.Get("correlation_id")),
		Channel:       strings.TrimSpace(q.Get("channel")),
	}
	if raw := strings.TrimSpace(q.Get("actor_id")); raw != "" {
		if filter.Actor, err = identity.ParseUserID(raw); err != nil {
			return errx.InvalidField("actor_id", "actor_id must be a valid UUID")
		}
	}
	if raw := strings.TrimSpace(q.Get("project_id")); raw != "" {
		if filter.Project, err = uuid.Parse(raw); err != nil {
			return errx.InvalidField("project_id", "project_id must be a valid UUID")
		}
	}
	if filter.From, filter.Before, err = httpx.TimeRange(r, "from", "to", time.UTC); err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	result, err := h.queries.List(r.Context(), org, filter, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, result)
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	v, err := h.queries.Verify(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, v)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	s, err := h.queries.Summary(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, s)
}

func (h *Handler) people(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	people, err := h.queries.People(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": people})
}
