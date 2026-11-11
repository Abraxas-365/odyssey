// Package projecthttp exposes projects over HTTP (docs/api.md).
package projecthttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type Handler struct {
	commands project.Commands
	queries  project.Queries
}

func New(commands project.Commands, queries project.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/projects")
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/", h.list)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Post("/", h.create)
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/{id}", h.find)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Patch("/{id}", h.update)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Post("/{id}/archive", h.archive)
	g.With(httpx.RequirePermissions(permission.ProjectsTier)).Put("/{id}/tier", h.setTier)
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/{id}/environments", h.listEnvironments)
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/{id}/environments/{environment}", h.findEnvironment)
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/{id}/services", h.listServices)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/{id}/services", h.setServices)
	// A proposal from a folder's file names (MS-5): no app needed (the
	// console asks while creating one), nothing written.
	r.With(httpx.RequirePermissions(permission.ProjectsWrite)).Post("/services/detect", h.detectServices)
}

// detectServices proposes services from the paths of a folder's files.
func (h *Handler) detectServices(w http.ResponseWriter, r *http.Request) error {
	if _, err := httpx.Organization(r); err != nil {
		return err
	}
	var input project.DetectServices
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return err
	}
	return httpx.JSON(w, project.Detect(input))
}

// listEnvironments is not paginated: a project has a handful (production,
// staging, open PR previews). Production first.
func (h *Handler) listEnvironments(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var filter project.EnvironmentFilter
	if filter.Kind, err = httpx.EnumFilter(r, "kind", project.EnvironmentKind.Valid, project.EnvironmentKindMessage); err != nil {
		return err
	}
	items, err := h.queries.ListEnvironments(r.Context(), org, id, filter)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": items})
}

func (h *Handler) findEnvironment(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	env, err := project.ParseEnvironmentID(r.PathValue("environment"))
	if err != nil {
		return err
	}
	found, err := h.queries.FindEnvironment(r.Context(), org, id, env)
	if err != nil {
		return err
	}
	return httpx.JSON(w, found)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input project.Create
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.CreatedBy = user
	id, err := h.commands.Create(r.Context(), org, input)
	if err != nil {
		return err
	}
	created, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.Created(w, r, id.String(), created)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	var filter project.Filter
	if filter.Status, err = httpx.EnumFilter(r, "status", project.Status.Valid, project.StatusMessage); err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	result, err := h.queries.List(r.Context(), org, filter, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, result)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	found, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, found)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input project.Update
	if err := httpx.DecodePatch(r, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	if err := h.commands.Update(r.Context(), org, id, input); err != nil {
		return err
	}
	updated, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, updated)
}

// setTier is IT's: it moves the app between sandbox, internal and
// production (the body is {"tier": ...}). Answers 200 with the project.
func (h *Handler) setTier(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input project.SetTier
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	if err := h.commands.SetTier(r.Context(), org, id, input); err != nil {
		return err
	}
	updated, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, updated)
}

// archive is an action, not a DELETE: the project and its history stay
// readable. Answers 200 with the archived project.
func (h *Handler) archive(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	if err := h.commands.Archive(r.Context(), org, id, project.Archive{ArchivedBy: user}); err != nil {
		return err
	}
	archived, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, archived)
}

// listServices answers {"items": [...]} in the app's order (a handful; not
// paginated).
func (h *Handler) listServices(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	items, err := h.queries.Services(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": items})
}

// setServices replaces the whole list (body {"items": [...]}); answers 200
// with the stored list, or 202 with the pending approval when IT's rules
// hold the change (nothing changed). The next version of the app takes it.
func (h *Handler) setServices(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input project.SetServices
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.UpdatedBy = user
	out, err := h.commands.SetServices(r.Context(), org, id, input)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": out.Items})
}
