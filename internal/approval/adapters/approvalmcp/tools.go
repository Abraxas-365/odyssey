// Package approvalmcp is the approval domain's MCP tools: get_request (how an
// action IT's rules held is going) and list_requests. Assistants only read:
// deciding is IT's, in the console (docs/approvals.md). They call the same
// approval Queries as the REST API, as the caller (their own requests, and the
// ones they could decide).
package approvalmcp

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
}

type tools struct {
	queries approval.Queries
	opts    Options
}

// Tools are get_request and list_requests.
func Tools(queries approval.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "get_request",
			Title: "Estado de una solicitud a TI",
			Description: "Muestra cómo va una solicitud de aprobación que las reglas de TI abrieron al pedir algo (set_access, enable_ai, create_database, " +
				"una publicación…): si sigue pendiente, quién la decidió y con qué nota, cuándo vence y, si ya se aprobó pero no se pudo hacer, por qué. " +
				"Úsala con el request_id que devolvió la herramienta que quedó pendiente; no reintentes esa herramienta mientras tanto. " +
				"Shows how an approval request that IT's rules opened is going (set_access, enable_ai, create_database, a publication…): " +
				"whether it is still pending, who decided it and with what note, when it expires and, if it was approved but could not run, why. " +
				"Use it with the request_id the pending tool returned; do not retry that tool meanwhile.",
			Toolset:     "requests",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.get),
		mcpx.Typed(mcpx.Tool{
			Name:  "list_requests",
			Title: "Solicitudes a TI",
			Description: "Lista las solicitudes de aprobación del usuario (las que pidió él), de la más reciente a la más antigua; con status filtra " +
				"(pending, approved, rejected, expired, cancelled). Úsala para ver qué sigue esperando a TI. " +
				"Lists the user's approval requests (the ones they asked for), newest first; status filters " +
				"(pending, approved, rejected, expired, cancelled). Use it to see what is still waiting for IT. " +
				"Paginated: pass next_offset as offset to read more.",
			Toolset:     "requests",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.list),
	}
}

// GetRequestIn is get_request's input.
type GetRequestIn struct {
	RequestID string `json:"request_id" jsonschema:"la solicitud (request_id que devolvió la herramienta que quedó pendiente, o de list_requests)"`
}

// Request is one approval request as an assistant reads it.
type Request struct {
	RequestID      string     `json:"request_id"`
	Action         string     `json:"action" jsonschema:"qué se pidió: deploy, rollback, set_access, connect_resource, delete_resource o enable_ai"`
	Summary        string     `json:"summary" jsonschema:"lo pedido en una frase"`
	Status         string     `json:"status" jsonschema:"pending (espera a TI), approved (aprobada), rejected (rechazada), expired (nadie decidió a tiempo) o cancelled (se retiró)"`
	Reason         string     `json:"reason" jsonschema:"por qué hizo falta la aprobación (la regla de TI)"`
	DecisionNote   string     `json:"decision_note,omitempty" jsonschema:"la nota de quien decidió"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	Done           bool       `json:"done" jsonschema:"true cuando ya se decidió o venció (si es false sigue esperando a TI)"`
	ExecutionError string     `json:"execution_error,omitempty" jsonschema:"si se aprobó pero no se pudo hacer: por qué (puede pedirse de nuevo)"`
}

// GetRequestOut is get_request's result.
type GetRequestOut struct {
	Request
	ConsoleURL string `json:"console_url" jsonschema:"enlace a la bandeja de solicitudes en la consola"`
	NextStep   string `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) get(ctx context.Context, _ mcpx.Principal, in GetRequestIn) (GetRequestOut, error) {
	id, err := approval.ParseID(strings.TrimSpace(in.RequestID))
	if err != nil {
		return GetRequestOut{}, errx.InvalidField("request_id", "request_id must be the id a pending tool returned (or one from list_requests)")
	}
	caller, _ := mcpx.FromContext(ctx)
	v, err := t.queries.Find(ctx, caller.Organization, id, approval.ViewerOf(ctx))
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return GetRequestOut{}, mcpx.NextStep(errx.NotFound("request not found").WithField("request_id"),
				"Usa list_requests para ver tus solicitudes.")
		}
		return GetRequestOut{}, err
	}
	return GetRequestOut{Request: request(v.Request), ConsoleURL: t.opts.ConsoleURL + "/bandeja?id=" + url.QueryEscape(v.Request.ID.String()), NextStep: step(v.Request)}, nil
}

// ListRequestsIn is list_requests's input.
type ListRequestsIn struct {
	Status string `json:"status,omitempty" jsonschema:"filtra por estado: pending, approved, rejected, expired o cancelled; vacío son todas"`
	Limit  int    `json:"limit,omitempty" jsonschema:"cuántas devolver (por defecto 20, máximo 100)"`
	Offset int    `json:"offset,omitempty" jsonschema:"desde qué posición seguir (usa next_offset de la respuesta anterior)"`
}

// ListRequestsOut is list_requests's result.
type ListRequestsOut struct {
	Items      []Request `json:"items"`
	Total      int       `json:"total"`
	Limit      int       `json:"limit"`
	Offset     int       `json:"offset"`
	NextOffset *int      `json:"next_offset,omitempty" jsonschema:"offset para pedir la siguiente página; ausente si no hay más"`
	ConsoleURL string    `json:"console_url" jsonschema:"enlace a la bandeja de solicitudes en la consola"`
	NextStep   string    `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) list(ctx context.Context, caller mcpx.Principal, in ListRequestsIn) (ListRequestsOut, error) {
	if in.Limit < 0 || in.Offset < 0 {
		return ListRequestsOut{}, errx.InvalidField("limit", "limit and offset cannot be negative")
	}
	f := approval.Filter{Mine: true}
	if s := approval.Status(strings.ToLower(strings.TrimSpace(in.Status))); s != "" {
		if !s.Valid() {
			return ListRequestsOut{}, errx.InvalidField("status", "status must be pending, approved, rejected, expired or cancelled")
		}
		f.Status = []approval.Status{s}
	}
	page, err := t.queries.List(ctx, caller.Organization, approval.ViewerOf(ctx), f, query.Pagination{Limit: in.Limit, Offset: in.Offset}.Normalize())
	if err != nil {
		return ListRequestsOut{}, err
	}
	out := ListRequestsOut{Items: make([]Request, 0, len(page.Items)), Total: page.Page.Total, Limit: page.Page.Limit,
		Offset: page.Page.Offset, ConsoleURL: t.opts.ConsoleURL + "/bandeja",
		NextStep: "Para una en particular usa get_request con su request_id; si una sigue pending, está esperando a TI y no hay que repetirla."}
	for _, v := range page.Items {
		out.Items = append(out.Items, request(v.Request))
	}
	if next := page.Page.Offset + len(page.Items); next < page.Page.Total {
		out.NextOffset = &next
	}
	return out, nil
}

func request(r approval.Request) Request {
	return Request{RequestID: r.ID.String(), Action: r.Action, Summary: r.Summary, Status: string(r.Status), Reason: r.Reason,
		DecisionNote: r.DecisionNote, DecidedAt: r.DecidedAt, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
		Done: r.Status.Decided(), ExecutionError: r.ExecutionError}
}

// step is what the assistant does about a request in its state.
func step(r approval.Request) string {
	switch {
	case r.Status == approval.StatusPending:
		return "Sigue esperando a TI: dile al usuario que quedó pendiente y que le avisarán; no repitas lo pedido. Vuelve a mirarla más tarde si el usuario lo pide."
	case r.Status == approval.StatusApproved && r.ExecutionError != "":
		return "TI lo aprobó pero no se pudo hacer: muéstrale al usuario execution_error y, si quiere, pídelo de nuevo (puede necesitar otra aprobación)."
	case r.Status == approval.StatusApproved:
		return "TI lo aprobó y ya se hizo (o se está haciendo): usa get_app o get_deployment para ver el resultado."
	case r.Status == approval.StatusRejected:
		return "TI no lo aprobó: muéstrale al usuario la nota (decision_note) tal cual y no lo reintentes ni busques otra vía; que lo hable con TI."
	case r.Status == approval.StatusExpired:
		return "Nadie de TI lo decidió a tiempo y venció: si el usuario aún lo quiere, pídelo de nuevo."
	default:
		return "La solicitud se retiró: no se hizo nada. Si el usuario aún lo quiere, pídelo de nuevo."
	}
}
