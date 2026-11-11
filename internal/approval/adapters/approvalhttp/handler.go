// Package approvalhttp exposes approval requests over HTTP (docs/api.md §11,
// docs/approvals.md).
package approvalhttp

import (
	"context"
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

type Handler struct {
	commands approval.Commands
	queries  approval.Queries
}

func New(commands approval.Commands, queries approval.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router. There is no
// blanket permission: whoever asked sees and cancels their own requests, and
// whoever holds one of a request's approver permissions sees and decides it
// (the service judges each request, from the actor on the context).
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/approvals")
	g.Get("/", h.list)
	g.Get("/{id}", h.get)
	g.Post("/{id}/approve", h.approve)
	g.Post("/{id}/reject", h.reject)
	g.Post("/{id}/cancel", h.cancel)
}

// note is the body of approve and reject; the whole body is optional.
type note struct {
	Note string `json:"note"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	statuses, err := httpx.EnumFilter(r, "status", approval.Status.Valid, "status must be pending, approved, rejected, expired or cancelled")
	if err != nil {
		return err
	}
	f := approval.Filter{Status: statuses}
	q := r.URL.Query()
	switch q.Get("mine") {
	case "", "false":
	case "true":
		f.Mine = true
	default:
		return badBool("mine")
	}
	switch q.Get("decidable") {
	case "", "false":
	case "true":
		f.Decidable = true
	default:
		return badBool("decidable")
	}
	got, err := h.queries.List(r.Context(), org, approval.ViewerOf(r.Context()), f, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, got)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	org, id, err := target(r)
	if err != nil {
		return err
	}
	v, err := h.queries.Find(r.Context(), org, id, approval.ViewerOf(r.Context()))
	if err != nil {
		return err
	}
	return httpx.JSON(w, v)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) error {
	return h.decide(w, r, h.commands.Approve)
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) error {
	return h.decide(w, r, h.commands.Reject)
}

// target reads the tenant (from the token) and the request id (from the path).
func target(r *http.Request) (identity.OrganizationID, approval.ID, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return identity.OrganizationID{}, approval.ID{}, err
	}
	id, err := approval.ParseID(r.PathValue("id"))
	return org, id, err
}

func badBool(name string) error { return errx.InvalidField(name, name+" must be true or false") }

func (h *Handler) decide(w http.ResponseWriter, r *http.Request, do func(ctx context.Context, org identity.OrganizationID, id approval.ID, in approval.Decide) (approval.Request, error)) error {
	org, id, err := target(r)
	if err != nil {
		return err
	}
	raw, err := httpx.Body(r)
	if err != nil {
		return err
	}
	var body note
	if len(raw) > 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			return err
		}
	}
	req, err := do(r.Context(), org, id, approval.Decide{By: approval.ViewerOf(r.Context()), Note: body.Note})
	if err != nil {
		return err
	}
	return httpx.JSON(w, req)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) error {
	org, id, err := target(r)
	if err != nil {
		return err
	}
	req, err := h.commands.Cancel(r.Context(), org, id, approval.Cancel{By: approval.ViewerOf(r.Context())})
	if err != nil {
		return err
	}
	return httpx.JSON(w, req)
}
