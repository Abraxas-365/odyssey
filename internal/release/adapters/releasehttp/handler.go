// Package releasehttp exposes what runs in each environment: deployments
// (publish, progress, history, rollback) and releases (docs/api.md).
package releasehttp

import (
	"strings"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands release.Commands
	queries  release.Queries
}

func New(commands release.Commands, queries release.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	env := r.Group("/projects/:id/environments/:environment")
	env.Get("/current", httpx.RequirePermissions(permission.DeploymentsRead), h.current)
	env.Get("/deployments", httpx.RequirePermissions(permission.DeploymentsRead), h.list)
	env.Post("/deployments", httpx.RequirePermissions(permission.DeploymentsCreate), h.deploy)
	env.Get("/deployments/:deployment", httpx.RequirePermissions(permission.DeploymentsRead), h.find)
	env.Post("/deployments/:deployment/rollback", httpx.RequirePermissions(permission.DeploymentsRollback), h.rollback)
	r.Get("/projects/:id/releases/:release", httpx.RequirePermissions(permission.DeploymentsRead), h.findRelease)
}

type place struct {
	project     project.ID
	environment project.EnvironmentID
}

func parsePlace(c *fiber.Ctx) (place, error) {
	p, err := project.ParseID(c.Params("id"))
	if err != nil {
		return place{}, err
	}
	e, err := project.ParseEnvironmentID(c.Params("environment"))
	if err != nil {
		return place{}, err
	}
	return place{project: p, environment: e}, nil
}

func (h *Handler) current(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Current(c.UserContext(), org, at.project, at.environment)
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
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	var filter release.DeploymentFilter
	if filter.Status, err = httpx.EnumFilter(c, "status", release.Status.Valid, release.StatusMessage); err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.ListDeployments(c.UserContext(), org, at.project, at.environment, filter, page)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// deploy answers 202: the deployment runs in the worker; poll its URL.
func (h *Handler) deploy(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	var input release.Deploy
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.RequestedBy = user
	d, err := h.commands.Deploy(c.UserContext(), org, at.project, at.environment, input)
	if err != nil {
		return err
	}
	httpx.SetLocation(c, d.ID.String())
	return c.Status(fiber.StatusAccepted).JSON(d)
}

func (h *Handler) find(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(c.Params("deployment"))
	if err != nil {
		return err
	}
	d, err := h.queries.FindDeployment(c.UserContext(), org, at.project, at.environment, id)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// rollback takes no body: it deploys again the release of :deployment.
func (h *Handler) rollback(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(c.Params("deployment"))
	if err != nil {
		return err
	}
	d, err := h.commands.Rollback(c.UserContext(), org, at.project, at.environment, id, release.Rollback{RequestedBy: user})
	if err != nil {
		return err
	}
	// Location is the new deployment, a sibling of :deployment.
	base := strings.TrimSuffix(strings.TrimRight(c.Path(), "/"), "/rollback")
	c.Set(fiber.HeaderLocation, base[:strings.LastIndex(base, "/")+1]+d.ID.String())
	return c.Status(fiber.StatusAccepted).JSON(d)
}

func (h *Handler) findRelease(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	p, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	id, err := release.ParseID(c.Params("release"))
	if err != nil {
		return err
	}
	r, err := h.queries.FindRelease(c.UserContext(), org, p, id)
	if err != nil {
		return err
	}
	return c.JSON(r)
}
