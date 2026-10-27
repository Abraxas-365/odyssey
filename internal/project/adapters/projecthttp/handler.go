// Package projecthttp exposes projects over HTTP (docs/api.md).
package projecthttp

import (
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands project.Commands
	queries  project.Queries
}

func New(commands project.Commands, queries project.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/projects")
	g.Get("/", httpx.RequirePermissions(permission.ProjectsRead), h.list)
	g.Post("/", httpx.RequirePermissions(permission.ProjectsWrite), h.create)
	g.Get("/:id", httpx.RequirePermissions(permission.ProjectsRead), h.find)
	g.Patch("/:id", httpx.RequirePermissions(permission.ProjectsWrite), h.update)
	g.Post("/:id/archive", httpx.RequirePermissions(permission.ProjectsWrite), h.archive)
}

func (h *Handler) create(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var input project.Create
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.CreatedBy = user
	id, err := h.commands.Create(c.UserContext(), org, input)
	if err != nil {
		return err
	}
	created, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return httpx.Created(c, id.String(), created)
}

func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var filter project.Filter
	if filter.Status, err = httpx.EnumFilter(c, "status", project.Status.Valid, project.StatusMessage); err != nil {
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

func (h *Handler) find(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	found, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(found)
}

func (h *Handler) update(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var input project.Update
	if err := httpx.DecodePatch(c, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	if err := h.commands.Update(c.UserContext(), org, id, input); err != nil {
		return err
	}
	updated, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(updated)
}

// archive is an action, not a DELETE: the project and its history stay
// readable. Answers 200 with the archived project.
func (h *Handler) archive(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	if err := h.commands.Archive(c.UserContext(), org, id, project.Archive{ArchivedBy: user}); err != nil {
		return err
	}
	archived, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(archived)
}
