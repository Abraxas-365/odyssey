// Package fleethttp exposes clusters over HTTP (docs/api.md): what TI
// sees and does in Infraestructura → Clústeres.
package fleethttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

type Handler struct {
	commands fleet.Commands
	queries  fleet.Queries
}

func New(commands fleet.Commands, queries fleet.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/clusters")
	g.With(httpx.RequirePermissions(permission.ClustersRead)).Get("/", h.list)
	g.With(httpx.RequirePermissions(permission.ClustersManage)).Post("/", h.create)
	g.With(httpx.RequirePermissions(permission.ClustersRead)).Get("/{id}", h.find)
	g.With(httpx.RequirePermissions(permission.ClustersManage)).Post("/{id}/enrollment-token", h.issueToken)
	g.With(httpx.RequirePermissions(permission.ClustersManage)).Post("/{id}/revoke", h.revoke)
	g.With(httpx.RequirePermissions(permission.ClustersManage)).Post("/{id}/move", h.move)
	g.With(httpx.RequirePermissions(permission.ClustersRead)).Get("/{id}/egress", h.egress)
	g.With(httpx.RequirePermissions(permission.ClustersManage)).Put("/{id}/egress", h.setEgress)
}

// egress answers 200 with the policy and whether the cluster enforces it.
func (h *Handler) egress(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	status, err := h.queries.Egress(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, status)
}

// setEgress replaces the policy (idempotent: the same body twice changes
// nothing) and answers 200 with the new state, usually "pending" until the
// agent confirms.
func (h *Handler) setEgress(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input fleet.SetEgress
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.ChangedBy = user
	status, err := h.commands.SetEgress(r.Context(), org, id, input)
	if err != nil {
		return err
	}
	return httpx.JSON(w, status)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	var filter fleet.Filter
	if filter.Status, err = httpx.EnumFilter(r, "status", fleet.Status.Valid, fleet.StatusMessage); err != nil {
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
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	found, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, found)
}

// create answers 201 with the cluster and its enrolment token: the only
// time the token is shown (only its hash is kept).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input fleet.Create
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.CreatedBy = user
	created, err := h.commands.Create(r.Context(), org, input)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return httpx.Created(w, r, created.Cluster.ID.String(), created)
}

// issueToken replaces the pending token (a lost one, or reconnecting a
// cluster whose agent lost its key).
func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	token, err := h.commands.IssueToken(r.Context(), org, id, fleet.IssueToken{IssuedBy: user})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return httpx.JSON(w, token)
}

// revoke is an action, not a DELETE: the cluster and its history stay
// readable. The body is optional ({"move_to": id} also moves its apps, in
// the same transaction). Answers 200 with the revoked cluster.
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input fleet.Revoke
	if httpx.HasBody(r) {
		if err := httpx.DecodeJSON(r, &input); err != nil {
			return err
		}
	}
	input.RevokedBy = user
	if err := h.commands.Revoke(r.Context(), org, id, input); err != nil {
		return err
	}
	revoked, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, revoked)
}

// move sends the apps of a revoked cluster to an active one. Answers 200
// with what moved.
func (h *Handler) move(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := fleet.ParseClusterID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var input fleet.Move
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.MovedBy = user
	moved, err := h.commands.Move(r.Context(), org, id, input)
	if err != nil {
		return err
	}
	return httpx.JSON(w, moved)
}
