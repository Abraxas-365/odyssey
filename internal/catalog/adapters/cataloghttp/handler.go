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
// sees the choices), without the connection of a resource the company
// already has (its host and its vault's keys are IT's); editing an offering
// and publishing the organization's recipes is IT's: odyssey:catalog:manage,
// and a recipe that reads secret stores also odyssey:secrets:external.
func (h *Handler) Register(r fiber.Router) {
	g := r.Group("/catalog")
	g.Get("/recipes", h.list)
	g.Post("/recipes", httpx.RequirePermissions(permission.CatalogManage), h.register)
	g.Get("/recipes/:recipe", h.get)
	g.Post("/recipes/:recipe/versions", httpx.RequirePermissions(permission.CatalogManage), h.publish)
	g.Put("/recipes/:recipe/offering", httpx.RequirePermissions(permission.CatalogManage), h.update)
	g.Get("/teams", httpx.RequirePermissions(permission.CatalogManage), h.teams)
	g.Get("/stores", httpx.RequirePermissions(permission.CatalogManage), h.stores)
	g.Get("/kinds", h.kinds)
}

// kinds are the kinds' contracts: what an app receives of each, the same
// for everyone (no organization's data).
func (h *Handler) kinds(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"items": catalog.Kinds()})
}

// shown is e as the caller may read it.
func shown(c *fiber.Ctx, e catalog.Entry) catalog.Entry {
	if httpx.Can(c, permission.CatalogManage) {
		return e
	}
	return e.Redacted()
}

// stores are the secret stores the organization's clusters announce.
func (h *Handler) stores(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Stores(c.UserContext(), org)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": out})
}

func (h *Handler) register(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var in catalog.RegisterRecipe
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.PublishedBy, in.MayReadStores = user, httpx.Can(c, permission.SecretsExternal)
	e, err := h.commands.RegisterRecipe(c.UserContext(), org, in)
	if err != nil {
		return err
	}
	c.Location("/api/v1/catalog/recipes/" + e.Recipe)
	return c.Status(fiber.StatusCreated).JSON(e)
}

func (h *Handler) publish(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var in catalog.PublishVersion
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.PublishedBy, in.MayReadStores = user, httpx.Can(c, permission.SecretsExternal)
	e, err := h.commands.PublishVersion(c.UserContext(), org, c.Params("recipe"), in)
	if err != nil {
		return err
	}
	c.Location("/api/v1/catalog/recipes/" + e.Recipe)
	return c.Status(fiber.StatusCreated).JSON(e)
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
	for i := range entries {
		entries[i] = shown(c, entries[i])
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
	return c.JSON(shown(c, e))
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
