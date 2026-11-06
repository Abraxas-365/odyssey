// Package accounthttp serves the person using the console: GET /api/v1/me
// and DELETE /api/v1/me/assistants/:id (docs/api.md §11).
package accounthttp

import (
	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	queries  account.Queries
	commands account.Commands
}

func New(queries account.Queries, commands account.Commands) *Handler {
	return &Handler{queries: queries, commands: commands}
}

// Register mounts the routes on an already-authenticated router. Any
// signed-in user may ask about themselves: no permission is required.
func (h *Handler) Register(r fiber.Router) {
	r.Get("/me", h.me)
	r.Delete("/me/assistants/:id", h.disconnect)
}

func caller(c *fiber.Ctx) (account.Caller, error) {
	user, err := httpx.User(c)
	if err != nil {
		return account.Caller{}, err
	}
	return account.Caller{User: user, Permissions: httpx.Permissions(c), Token: httpx.BearerToken(c)}, nil
}

func (h *Handler) me(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := caller(c)
	if err != nil {
		return err
	}
	me, err := h.queries.Me(c.UserContext(), org, who)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(me)
}

func (h *Handler) disconnect(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := caller(c)
	if err != nil {
		return err
	}
	if err := h.commands.Disconnect(c.UserContext(), org, who, c.Params("id")); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}
