// Package aihttp exposes an environment's AI over HTTP (docs/api.md §11):
// /projects/:id/environments/:environment/ai.
package aihttp

import (
	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands ai.Commands
	queries  ai.Queries
}

func New(commands ai.Commands, queries ai.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router. Reading
// takes projects:read; every change projects:write (like the rest of an app's
// settings).
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/projects/:id/environments/:environment/ai")
	g.Get("/", httpx.RequirePermissions(permission.ProjectsRead), h.get)
	g.Put("/", httpx.RequirePermissions(permission.ProjectsWrite), h.enable)
	g.Delete("/", httpx.RequirePermissions(permission.ProjectsWrite), h.disable)
	g.Post("/rotate", httpx.RequirePermissions(permission.ProjectsWrite), h.rotate)
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

func (h *Handler) get(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Get(c.UserContext(), at.org, at.project, at.environment)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// enable answers 202: the key is made and delivered by the worker, and the
// app is wired by a configuration deployment that follows.
func (h *Handler) enable(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Enable(c.UserContext(), at.org, at.project, at.environment, ai.Enable{RequestedBy: at.user})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

func (h *Handler) rotate(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Rotate(c.UserContext(), at.org, at.project, at.environment, ai.Rotate{RequestedBy: at.user})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

func (h *Handler) disable(c *fiber.Ctx) error {
	at, err := parse(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Disable(c.UserContext(), at.org, at.project, at.environment, ai.Disable{RequestedBy: at.user})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}
