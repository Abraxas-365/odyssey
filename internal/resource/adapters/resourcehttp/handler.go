// Package resourcehttp exposes an environment's resources over HTTP
// (docs/api.md §11): /projects/:id/environments/:environment/resources.
package resourcehttp

import (
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands resource.Commands
	queries  resource.Queries
}

func New(commands resource.Commands, queries resource.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router. Reading
// takes projects:read; every change projects:write (like the rest of an
// app's settings), and deleting, which erases data, its own permission.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/projects/:id/environments/:environment/resources")
	g.Get("/", httpx.RequirePermissions(permission.ProjectsRead), h.list)
	g.Post("/", httpx.RequirePermissions(permission.ProjectsWrite), h.create)
	g.Get("/:name", httpx.RequirePermissions(permission.ProjectsRead), h.find)
	g.Patch("/:name", httpx.RequirePermissions(permission.ProjectsWrite), h.resize)
	g.Delete("/:name", httpx.RequirePermissions(permission.ResourcesDelete), h.delete)
	g.Put("/:name/connection", httpx.RequirePermissions(permission.ProjectsWrite), h.connect)
	g.Delete("/:name/connection", httpx.RequirePermissions(permission.ProjectsWrite), h.disconnect)
}

type place struct {
	org         identity.OrganizationID
	user        identity.UserID
	project     project.ID
	environment project.EnvironmentID
}

func parse(c *fiber.Ctx) (place, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return place{}, err
	}
	user, err := httpx.User(c)
	if err != nil {
		return place{}, err
	}
	p, err := project.ParseID(c.Params("id"))
	if err != nil {
		return place{}, err
	}
	e, err := project.ParseEnvironmentID(c.Params("environment"))
	if err != nil {
		return place{}, err
	}
	return place{org: org, user: user, project: p, environment: e}, nil
}

func (h *Handler) list(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.queries.List(c.UserContext(), at.org, at.project, at.environment)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) find(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Get(c.UserContext(), at.org, at.project, at.environment, c.Params("name"))
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// create answers 201 with the resource; when it was connected, the config
// deployment (if one started) is in deployment_id and Location points at the
// resource.
func (h *Handler) create(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	var in resource.Create
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.RequestedBy = at.user
	out, err := h.commands.Create(c.UserContext(), at.org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(c, out.Outcome); done {
		return err
	}
	return httpx.Created(c, out.Name, out)
}

func (h *Handler) resize(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	var in resource.Resize
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.RequestedBy = at.user
	out, err := h.commands.Resize(c.UserContext(), at.org, at.project, at.environment, c.Params("name"), in)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// delete answers 202: the cluster erases the data in its own time and the
// resource shows as deleting until it does.
func (h *Handler) delete(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Delete(c.UserContext(), at.org, at.project, at.environment, c.Params("name"), resource.Delete{RequestedBy: at.user})
	if err != nil {
		return err
	}
	if done, err := httpx.Held(c, out.Outcome); done {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

// connect answers 202 like every settings change: the config deployment (if
// one starts) runs in the worker.
func (h *Handler) connect(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	var in resource.Connect
	if len(c.Body()) > 0 {
		if err := httpx.DecodeJSON(c, &in); err != nil {
			return err
		}
	}
	in.Name, in.RequestedBy = c.Params("name"), at.user
	out, err := h.commands.Connect(c.UserContext(), at.org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(c, out.Outcome); done {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

func (h *Handler) disconnect(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Disconnect(c.UserContext(), at.org, at.project, at.environment, c.Params("name"), at.user)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}
