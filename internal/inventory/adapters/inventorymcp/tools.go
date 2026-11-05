// Package inventorymcp is the inventory's MCP tool: get_attention, "what needs
// my attention" for the person who connected the assistant. It calls the same
// inventory.Queries as GET /api/v1/attention, as the caller.
package inventorymcp

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
}

type tools struct {
	queries inventory.Queries
	opts    Options
}

// Tools is get_attention.
func Tools(queries inventory.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "get_attention",
			Title: "Lo que necesita atención",
			Description: "Lista lo que necesita atención ahora: las apps del usuario con algo por hacer (un secreto por cargar, un recurso que falló…), " +
				"sus solicitudes a TI que esperan, fueron rechazadas o no se pudieron hacer y, si el usuario es de TI, también lo que TI vigila " +
				"(aprobaciones por decidir, apps con vulnerabilidades críticas, publicaciones fallidas, clústeres sin respuesta). " +
				"Cada elemento trae una frase en español y el enlace de la consola donde se resuelve. Una lista vacía sin 'unavailable' es 'todo en orden'; " +
				"si 'unavailable' trae fuentes, esa parte no se pudo revisar ahora y la lista está incompleta. " +
				"Lists what needs attention now: the user's apps with something to do (a secret to upload, a resource that failed…), " +
				"their requests to IT that are waiting, were rejected or could not run and, for IT users, what IT watches over " +
				"(approvals to decide, apps with critical vulnerabilities, failed publications, clusters that do not answer). " +
				"Each item has a plain-Spanish sentence and the console link that solves it. An empty list without 'unavailable' means all is in order; " +
				"sources in 'unavailable' could not be read now, so the list is incomplete.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.get),
	}
}

type noInput struct{}

// Item is one thing that needs attention.
type Item struct {
	Kind     string `json:"kind" jsonschema:"app_attention, request_pending, request_rejected, request_failed (tuyo) o approval_pending, app_vulnerable, cluster_disconnected, cluster_policy, deployment_failed (de TI)"`
	Scope    string `json:"scope" jsonschema:"mine (lo tuyo) o it (lo que TI vigila)"`
	Severity string `json:"severity" jsonschema:"critical, warning o info"`
	Sentence string `json:"sentence" jsonschema:"qué pasa, en español"`
	// ConsoleURL is where it is solved.
	ConsoleURL string     `json:"console_url" jsonschema:"enlace de la consola donde se resuelve"`
	AppID      string     `json:"app_id,omitempty"`
	RequestID  string     `json:"request_id,omitempty" jsonschema:"para get_request"`
	At         *time.Time `json:"at,omitempty"`
}

// Out is get_attention's result.
type Out struct {
	Items       []Item   `json:"items"`
	AllInOrder  bool     `json:"all_in_order" jsonschema:"true si no hay nada y todas las fuentes se pudieron revisar"`
	Unavailable []string `json:"unavailable" jsonschema:"fuentes que no se pudieron revisar ahora (approvals, apps, clusters, requests): la lista está incompleta"`
	Truncated   bool     `json:"truncated" jsonschema:"hay más elementos de los que caben; resuélvelos y vuelve a pedir"`
}

func (t tools) get(ctx context.Context, caller mcpx.Principal, _ noInput) (Out, error) {
	got, err := t.queries.Attention(ctx, caller.Organization)
	if err != nil {
		return Out{}, err
	}
	out := Out{Items: make([]Item, 0, len(got.Items)), Unavailable: []string{}, Truncated: got.Truncated}
	for _, s := range got.Unavailable {
		out.Unavailable = append(out.Unavailable, string(s))
	}
	for _, i := range got.Items {
		item := Item{Kind: string(i.Kind), Scope: string(i.Scope), Severity: string(i.Severity), Sentence: i.Sentence,
			ConsoleURL: t.opts.ConsoleURL + i.Link, RequestID: i.Request, At: i.At}
		if i.App != nil {
			item.AppID = i.App.String()
		}
		out.Items = append(out.Items, item)
	}
	out.AllInOrder = len(out.Items) == 0 && len(out.Unavailable) == 0
	return out, nil
}
