// Package cataloghttp exposes the organization's resource catalog over HTTP
// (docs/api.md, docs/catalog.md).
package cataloghttp

import (
	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands catalog.Commands
	queries  catalog.Queries
}

func New(commands catalog.Commands, queries catalog.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router. Reading
// what is offered is anyone's in the organization (who creates a resource
// sees the choices); editing an offering is IT's: odyssey:catalog:manage.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/catalog")
	g.Get("/recipes", h.list)
	g.Get("/recipes/:recipe", h.get)
	g.Put("/recipes/:recipe/offering", httpx.RequirePermissions(permission.CatalogManage), h.update)
	g.Get("/teams", httpx.RequirePermissions(permission.CatalogManage), h.teams)
}

// teams are the organization's teams IT can limit an offering to.
func (h *Handler) teams(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Teams(c.UserContext(), org)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	entries, err := h.queries.List(c.UserContext(), org)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": entries})
}

func (h *Handler) get(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	e, err := h.queries.Find(c.UserContext(), org, c.Params("recipe"))
	if err != nil {
		return err
	}
	return c.JSON(e)
}

func (h *Handler) update(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var draft catalog.OfferingDraft
	if err := httpx.DecodeJSON(c, &draft); err != nil {
		return err
	}
	e, err := h.commands.UpdateOffering(c.UserContext(), org, c.Params("recipe"), catalog.UpdateOffering{OfferingDraft: draft, UpdatedBy: user})
	if err != nil {
		return err
	}
	return c.JSON(e)
}
