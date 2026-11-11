// Package accounthttp serves the person using the console: GET /api/v1/me
// and DELETE /api/v1/me/assistants/{id} (docs/api.md §11).
package accounthttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/httpx"
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
func (h *Handler) Register(r *httpx.Router) {
	r.Get("/me", h.me)
	r.Delete("/me/assistants/{id}", h.disconnect)
}

func caller(r *http.Request) (account.Caller, error) {
	user, err := httpx.User(r)
	if err != nil {
		return account.Caller{}, err
	}
	return account.Caller{User: user, Permissions: httpx.Permissions(r), Token: httpx.BearerToken(r)}, nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	who, err := caller(r)
	if err != nil {
		return err
	}
	me, err := h.queries.Me(r.Context(), org, who)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return httpx.JSON(w, me)
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	who, err := caller(r)
	if err != nil {
		return err
	}
	if err := h.commands.Disconnect(r.Context(), org, who, r.PathValue("id")); err != nil {
		return err
	}
	return httpx.Status(w, http.StatusNoContent)
}
