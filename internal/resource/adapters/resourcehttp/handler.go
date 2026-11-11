// Package resourcehttp exposes an environment's resources over HTTP
// (docs/api.md §11): /projects/{id}/environments/{environment}/resources.
package resourcehttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
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
func (h *Handler) Register(r *httpx.Router) {
	// what can be asked for here: the catalog as this environment sees it
	r.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/projects/{id}/environments/{environment}/catalog", h.offered)
	g := r.Group("/projects/{id}/environments/{environment}/resources")
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/", h.list)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Post("/", h.create)
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/{name}", h.find)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Patch("/{name}", h.change)
	g.With(httpx.RequirePermissions(permission.ResourcesDelete)).Delete("/{name}", h.delete)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/{name}/connection", h.connect)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Delete("/{name}/connection", h.disconnect)
}

type place struct {
	org         identity.OrganizationID
	user        identity.UserID
	project     project.ID
	environment project.EnvironmentID
}

func parse(r *http.Request) (place, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return place{}, err
	}
	user, err := httpx.User(r)
	if err != nil {
		return place{}, err
	}
	p, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return place{}, err
	}
	e, err := project.ParseEnvironmentID(r.PathValue("environment"))
	if err != nil {
		return place{}, err
	}
	return place{org: org, user: user, project: p, environment: e}, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.queries.List(r.Context(), at.org, at.project, at.environment)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// offered answers what the environment's app can ask for (the recipes its
// tier is offered and its cluster honours) and how: at once, with IT's
// approval or not at all, for the caller (docs/catalog.md).
func (h *Handler) offered(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Offered(r.Context(), at.org, at.project, at.environment, at.user)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Get(r.Context(), at.org, at.project, at.environment, r.PathValue("name"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// create answers 201 with the resource; when it was connected, the config
// deployment (if one started) is in deployment_id and Location points at the
// resource.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	var in resource.Create
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.RequestedBy = at.user
	out, err := h.commands.Create(r.Context(), at.org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.Created(w, r, out.Name, out)
}

// change answers 200 with the resource, or 202 when IT must approve it.
func (h *Handler) change(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	var in resource.Change
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.RequestedBy = at.user
	out, err := h.commands.Change(r.Context(), at.org, at.project, at.environment, r.PathValue("name"), in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.JSON(w, out)
}

// delete answers 202: the cluster erases the data in its own time and the
// resource shows as deleting until it does.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.commands.Delete(r.Context(), at.org, at.project, at.environment, r.PathValue("name"), resource.Delete{RequestedBy: at.user})
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

// connect answers 202 like every settings change: the config deployment (if
// one starts) runs in the worker.
func (h *Handler) connect(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	var in resource.Connect
	body, err := httpx.Body(r)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			return err
		}
	}
	in.Name, in.RequestedBy = r.PathValue("name"), at.user
	out, err := h.commands.Connect(r.Context(), at.org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.commands.Disconnect(r.Context(), at.org, at.project, at.environment, r.PathValue("name"), at.user)
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}
