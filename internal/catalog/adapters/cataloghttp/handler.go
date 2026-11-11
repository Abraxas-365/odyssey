// Package cataloghttp exposes the organization's resource catalog over HTTP
// (docs/api.md, docs/catalog.md).
package cataloghttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
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
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/catalog")
	g.Get("/recipes", h.list)
	g.With(httpx.RequirePermissions(permission.CatalogManage)).Post("/recipes", h.register)
	g.Get("/recipes/{recipe}", h.get)
	g.With(httpx.RequirePermissions(permission.CatalogManage)).Post("/recipes/{recipe}/versions", h.publish)
	g.With(httpx.RequirePermissions(permission.CatalogManage)).Put("/recipes/{recipe}/offering", h.update)
	g.With(httpx.RequirePermissions(permission.CatalogManage)).Get("/teams", h.teams)
	g.With(httpx.RequirePermissions(permission.CatalogManage)).Get("/stores", h.stores)
	g.Get("/kinds", h.kinds)
}

// kinds are the kinds' contracts: what an app receives of each, the same
// for everyone (no organization's data).
func (h *Handler) kinds(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, httpx.Map{"items": catalog.Kinds()})
}

// shown is e as the caller may read it.
func shown(r *http.Request, e catalog.Entry) catalog.Entry {
	if httpx.Can(r, permission.CatalogManage) {
		return e
	}
	return e.Redacted()
}

// stores are the secret stores the organization's clusters announce.
func (h *Handler) stores(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Stores(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": out})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var in catalog.RegisterRecipe
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.PublishedBy, in.MayReadStores = user, httpx.Can(r, permission.SecretsExternal)
	e, err := h.commands.RegisterRecipe(r.Context(), org, in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/catalog/recipes/"+e.Recipe)
	return httpx.JSONStatus(w, http.StatusCreated, e)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var in catalog.PublishVersion
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.PublishedBy, in.MayReadStores = user, httpx.Can(r, permission.SecretsExternal)
	e, err := h.commands.PublishVersion(r.Context(), org, r.PathValue("recipe"), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/catalog/recipes/"+e.Recipe)
	return httpx.JSONStatus(w, http.StatusCreated, e)
}

// teams are the organization's teams IT can limit an offering to.
func (h *Handler) teams(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Teams(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	entries, err := h.queries.List(r.Context(), org)
	if err != nil {
		return err
	}
	for i := range entries {
		entries[i] = shown(r, entries[i])
	}
	return httpx.JSON(w, httpx.Map{"items": entries})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	e, err := h.queries.Find(r.Context(), org, r.PathValue("recipe"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, shown(r, e))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var draft catalog.OfferingDraft
	if err := httpx.DecodeJSON(r, &draft); err != nil {
		return err
	}
	e, err := h.commands.UpdateOffering(r.Context(), org, r.PathValue("recipe"), catalog.UpdateOffering{OfferingDraft: draft, UpdatedBy: user})
	if err != nil {
		return err
	}
	return httpx.JSON(w, e)
}
