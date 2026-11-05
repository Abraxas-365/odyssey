// Package approvalhttp exposes approval requests over HTTP (docs/api.md §11,
// docs/approvals.md).
package approvalhttp

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/gofiber/fiber/v2"
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
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/approvals")
	g.Get("/", h.list)
	g.Get("/:id", h.get)
	g.Post("/:id/approve", h.approve)
	g.Post("/:id/reject", h.reject)
	g.Post("/:id/cancel", h.cancel)
}

// note is the body of approve and reject; the whole body is optional.
type note struct {
	Note string `json:"note"`
}

func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	statuses, err := httpx.EnumFilter(c, "status", approval.Status.Valid, "status must be pending, approved, rejected, expired or cancelled")
	if err != nil {
		return err
	}
	f := approval.Filter{Status: statuses}
	switch c.Query("mine") {
	case "", "false":
	case "true":
		f.Mine = true
	default:
		return badBool("mine")
	}
	switch c.Query("decidable") {
	case "", "false":
	case "true":
		f.Decidable = true
	default:
		return badBool("decidable")
	}
	got, err := h.queries.List(c.UserContext(), org, approval.ViewerOf(c.UserContext()), f, page)
	if err != nil {
		return err
	}
	return c.JSON(got)
}

func (h *Handler) get(c *fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	v, err := h.queries.Find(c.UserContext(), org, id, approval.ViewerOf(c.UserContext()))
	if err != nil {
		return err
	}
	return c.JSON(v)
}

func (h *Handler) approve(c *fiber.Ctx) error {
	return h.decide(c, h.commands.Approve)
}

func (h *Handler) reject(c *fiber.Ctx) error {
	return h.decide(c, h.commands.Reject)
}

// target reads the tenant (from the token) and the request id (from the path).
func target(c *fiber.Ctx) (identity.OrganizationID, approval.ID, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return identity.OrganizationID{}, approval.ID{}, err
	}
	id, err := approval.ParseID(c.Params("id"))
	return org, id, err
}

func badBool(name string) error { return errx.InvalidField(name, name+" must be true or false") }

func (h *Handler) decide(c *fiber.Ctx, do func(ctx context.Context, org identity.OrganizationID, id approval.ID, in approval.Decide) (approval.Request, error)) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	var body note
	if len(c.Body()) > 0 {
		if err := httpx.DecodeJSON(c, &body); err != nil {
			return err
		}
	}
	r, err := do(c.UserContext(), org, id, approval.Decide{By: approval.ViewerOf(c.UserContext()), Note: body.Note})
	if err != nil {
		return err
	}
	return c.JSON(r)
}

func (h *Handler) cancel(c *fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	r, err := h.commands.Cancel(c.UserContext(), org, id, approval.Cancel{By: approval.ViewerOf(c.UserContext())})
	if err != nil {
		return err
	}
	return c.JSON(r)
}
