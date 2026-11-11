// Package policyhttp exposes IT's rulebook over HTTP (docs/api.md, docs/policy.md).
package policyhttp

import (
	"net/http"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/policy"
)

type Handler struct {
	commands  policy.Commands
	queries   policy.Queries
	evaluator policy.Evaluator
}

func New(commands policy.Commands, queries policy.Queries, evaluator policy.Evaluator) *Handler {
	return &Handler{commands: commands, queries: queries, evaluator: evaluator}
}

// Register mounts the routes on an already-authenticated router. All of them
// are IT's: odyssey:policy:manage.
func (h *Handler) Register(r *httpx.Router) {
	g := r.Group("/policy", httpx.RequirePermissions(permission.PolicyManage))
	g.Get("/rules", h.list)
	g.Post("/rules", h.create)
	g.Put("/rules/{id}", h.replace)
	g.Delete("/rules/{id}", h.delete)
	g.Post("/evaluate", h.evaluate)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	rules, err := h.queries.List(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": rules})
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
	var draft policy.Draft
	if err := httpx.DecodeJSON(r, &draft); err != nil {
		return err
	}
	rule, err := h.commands.Create(r.Context(), org, policy.Create{Draft: draft, CreatedBy: user})
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/policy/rules/"+rule.ID.String())
	return httpx.JSONStatus(w, http.StatusCreated, rule)
}

// replace is a PUT: the whole rule. A built-in rule only takes its `enabled`
// switch (422 POLICY_BUILTIN otherwise).
func (h *Handler) replace(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := policy.ParseRuleID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var draft policy.Draft
	if err := httpx.DecodeJSON(r, &draft); err != nil {
		return err
	}
	rule, err := h.commands.Replace(r.Context(), org, id, policy.Replace{Draft: draft, UpdatedBy: user})
	if err != nil {
		return err
	}
	return httpx.JSON(w, rule)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := policy.ParseRuleID(r.PathValue("id"))
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	if err := h.commands.Delete(r.Context(), org, id, policy.Delete{DeletedBy: user}); err != nil {
		return err
	}
	return httpx.Status(w, http.StatusNoContent)
}

// evaluate is the dry run: the body is an Input (what the action would look
// like) and, optionally, `rule`: an unsaved rule (expression + effect) judged
// among the saved ones, standing in for the stored rule it `replaces` when
// editing. The answer is the decision plus what each rule said. It decides
// nothing and changes nothing; a `rule` that does not compile is 422
// POLICY_INVALID.
func (h *Handler) evaluate(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	var dry policy.Dry
	if err := httpx.DecodeJSON(r, &dry); err != nil {
		return err
	}
	if err := dry.Input.Validate(); err != nil {
		return err
	}
	result, err := h.evaluator.Explain(r.Context(), org, dry.Input, dry.Candidate())
	if err != nil {
		return err
	}
	return httpx.JSON(w, result)
}
