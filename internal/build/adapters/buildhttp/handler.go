// Package buildhttp exposes builds: their status and their log
// (docs/api.md). Sources are uploaded through the deployments route
// (releasehttp): publishing from code is a deployment with a build step.
package buildhttp

import (
	"net/http"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type Handler struct {
	queries build.Queries
}

func New(queries build.Queries) *Handler { return &Handler{queries: queries} }

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r *httpx.Router) {
	r.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/projects/{id}/builds/{build}", h.find)
	r.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/projects/{id}/builds/{build}/logs", h.logs)
}

// load finds the build of the path's project (another project's is NotFound).
func (h *Handler) load(r *http.Request) (identity.OrganizationID, build.Build, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return identity.OrganizationID{}, build.Build{}, err
	}
	p, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return org, build.Build{}, err
	}
	id, err := build.ParseID(r.PathValue("build"))
	if err != nil {
		return org, build.Build{}, err
	}
	b, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return org, build.Build{}, err
	}
	if b.Project != p {
		return org, build.Build{}, errx.NotFound("build not found")
	}
	return org, b, nil
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	_, b, err := h.load(r)
	if err != nil {
		return err
	}
	return httpx.JSON(w, b)
}

// logs pages the log: ?after=<seq>&limit=<n>. Poll with after=next until
// finished.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) error {
	org, b, err := h.load(r)
	if err != nil {
		return err
	}
	var q build.LogQuery
	if q.After, err = intParam(r, "after"); err != nil {
		return err
	}
	limit, err := intParam(r, "limit")
	if err != nil {
		return err
	}
	q.Limit = int(min(limit, build.MaxLogPage))
	page, err := h.queries.Logs(r.Context(), org, b.ID, q)
	if err != nil {
		return err
	}
	return httpx.JSON(w, page)
}

func intParam(r *http.Request, name string) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errx.InvalidField(name, name+" must be a whole number, 0 or more")
	}
	return n, nil
}
