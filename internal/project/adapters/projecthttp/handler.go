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
	g.Put("/:id/tier", httpx.RequirePermissions(permission.ProjectsTier), h.setTier)
	g.Get("/:id/environments", httpx.RequirePermissions(permission.ProjectsRead), h.listEnvironments)
	g.Get("/:id/environments/:environment", httpx.RequirePermissions(permission.ProjectsRead), h.findEnvironment)
	g.Get("/:id/services", httpx.RequirePermissions(permission.ProjectsRead), h.listServices)
	g.Put("/:id/services", httpx.RequirePermissions(permission.ProjectsWrite), h.setServices)
}

// listEnvironments is not paginated: a project has a handful (production,
// staging, open PR previews). Production first.
func (h *Handler) listEnvironments(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	var filter project.EnvironmentFilter
	if filter.Kind, err = httpx.EnumFilter(c, "kind", project.EnvironmentKind.Valid, project.EnvironmentKindMessage); err != nil {
		return err
	}
	items, err := h.queries.ListEnvironments(c.UserContext(), org, id, filter)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

func (h *Handler) findEnvironment(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	env, err := project.ParseEnvironmentID(c.Params("environment"))
	if err != nil {
		return err
	}
	found, err := h.queries.FindEnvironment(c.UserContext(), org, id, env)
	if err != nil {
		return err
	}
	return c.JSON(found)
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

// setTier is IT's: it moves the app between sandbox, internal and
// production (the body is {"tier": ...}). Answers 200 with the project.
func (h *Handler) setTier(c *fiber.Ctx) error {
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
	var input project.SetTier
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	if err := h.commands.SetTier(c.UserContext(), org, id, input); err != nil {
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

// listServices answers {"items": [...]} in the app's order (a handful; not
// paginated).
func (h *Handler) listServices(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	items, err := h.queries.Services(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

// setServices replaces the whole list (body {"items": [...]}); answers 200
// with the stored list, or 202 with the pending approval when IT's rules
// hold the change (nothing changed). The next version of the app takes it.
func (h *Handler) setServices(c *fiber.Ctx) error {
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
	var input project.SetServices
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	out, err := h.commands.SetServices(c.UserContext(), org, id, input)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(c, out.Outcome); done {
		return err
	}
	return c.JSON(fiber.Map{"items": out.Items})
}
