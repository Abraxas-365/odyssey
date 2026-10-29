// Package fleethttp exposes clusters over HTTP (docs/api.md): what TI
// sees and does in Infraestructura → Clústeres.
package fleethttp

import (
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands fleet.Commands
	queries  fleet.Queries
}

func New(commands fleet.Commands, queries fleet.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/clusters")
	g.Get("/", httpx.RequirePermissions(permission.ClustersRead), h.list)
	g.Post("/", httpx.RequirePermissions(permission.ClustersManage), h.create)
	g.Get("/:id", httpx.RequirePermissions(permission.ClustersRead), h.find)
	g.Post("/:id/enrollment-token", httpx.RequirePermissions(permission.ClustersManage), h.issueToken)
	g.Post("/:id/revoke", httpx.RequirePermissions(permission.ClustersManage), h.revoke)
	g.Post("/:id/move", httpx.RequirePermissions(permission.ClustersManage), h.move)
}

func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var filter fleet.Filter
	if filter.Status, err = httpx.EnumFilter(c, "status", fleet.Status.Valid, fleet.StatusMessage); err != nil {
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
	id, err := fleet.ParseClusterID(c.Params("id"))
	if err != nil {
		return err
	}
	found, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(found)
}

// create answers 201 with the cluster and its enrolment token: the only
// time the token is shown (only its hash is kept).
func (h *Handler) create(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var input fleet.Create
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.CreatedBy = user
	created, err := h.commands.Create(c.UserContext(), org, input)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return httpx.Created(c, created.Cluster.ID.String(), created)
}

// issueToken replaces the pending token (a lost one, or reconnecting a
// cluster whose agent lost its key).
func (h *Handler) issueToken(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(c.Params("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	token, err := h.commands.IssueToken(c.UserContext(), org, id, fleet.IssueToken{IssuedBy: user})
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(token)
}

// revoke is an action, not a DELETE: the cluster and its history stay
// readable. The body is optional ({"move_to": id} also moves its apps, in
// the same transaction). Answers 200 with the revoked cluster.
func (h *Handler) revoke(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(c.Params("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var input fleet.Revoke
	if httpx.HasBody(c) {
		if err := httpx.DecodeJSON(c, &input); err != nil {
			return err
		}
	}
	input.RevokedBy = user
	if err := h.commands.Revoke(c.UserContext(), org, id, input); err != nil {
		return err
	}
	revoked, err := h.queries.Find(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(revoked)
}

// move sends the apps of a revoked cluster to an active one. Answers 200
// with what moved.
func (h *Handler) move(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(c.Params("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var input fleet.Move
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.MovedBy = user
	moved, err := h.commands.Move(c.UserContext(), org, id, input)
	if err != nil {
		return err
	}
	return c.JSON(moved)
}
