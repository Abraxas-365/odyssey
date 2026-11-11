// Package aihttp exposes an environment's AI over HTTP (docs/api.md §11):
// /projects/{id}/environments/{environment}/ai.
package aihttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
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
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/projects/{id}/environments/{environment}/ai")
	g.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/", h.get)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/", h.enable)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Delete("/", h.disable)
	g.With(httpx.RequirePermissions(permission.ProjectsWrite)).Post("/rotate", h.rotate)
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

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Get(r.Context(), at.org, at.project, at.environment)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// enable answers 202: the key is made and delivered by the worker, and the
// app is wired by a configuration deployment that follows. The body is
// optional: {"services": [...]} gives the AI to those services only ([]:
// the whole app; absent: as it is, the whole app when it is off). With the
// AI on, other services move it there (same key).
func (h *Handler) enable(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	in := ai.Enable{RequestedBy: at.user}
	if httpx.HasBody(r) {
		var body struct {
			Services *[]string `json:"services"`
		}
		if err := httpx.DecodeJSON(r, &body); err != nil {
			return err
		}
		in.Services = body.Services
	}
	out, err := h.commands.Enable(r.Context(), at.org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.commands.Rotate(r.Context(), at.org, at.project, at.environment, ai.Rotate{RequestedBy: at.user})
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) error {
	at, err := parse(r)
	if err != nil {
		return err
	}
	out, err := h.commands.Disable(r.Context(), at.org, at.project, at.environment, ai.Disable{RequestedBy: at.user})
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}
