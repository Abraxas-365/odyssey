// Package projectmcp is the project domain's MCP tools: who the caller is
// (whoami) and which apps their organization has (list_apps). They call the
// same project.Queries as the REST API.
package projectmcp

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Options are the deployment facts whoami reports.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
	// GitHubEnabled reports whether apps can be connected to GitHub here.
	// Nil means no.
	GitHubEnabled func(ctx context.Context) bool
}

// Tools are whoami and list_apps.
func Tools(queries project.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "whoami",
			Title: "Quién soy en Odicea",
			Description: "Muestra con qué cuenta y permisos está conectado este asistente a Odicea: organización, usuario, " +
				"permisos efectivos, dirección de la consola y si GitHub está habilitado. Úsala primero si no sabes qué puedes hacer " +
				"o cuando una herramienta falla por permisos. " +
				"Shows which account and permissions this assistant is connected with: organization, user, effective permissions, " +
				"console URL and whether GitHub is enabled. Use it first when unsure what you may do, or when a tool fails on permissions.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.whoami),
		mcpx.Typed(mcpx.Tool{
			Name:  "list_apps",
			Title: "Listar apps",
			Description: "Lista las apps de la organización con su slug, nombre, estado y enlace a la consola. Úsala para encontrar " +
				"el slug de una app antes de pedir su estado o sus versiones; por defecto solo las activas (status=archived para las archivadas). " +
				"Lists the organization's apps with slug, name, status and console link. Use it to find an app's slug before asking " +
				"for its state or versions; active apps only by default (status=archived for archived ones). " +
				"Paginated: pass next_offset as offset to read more.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.listApps),
	}
}

type tools struct {
	queries project.Queries
	opts    Options
}

// WhoamiOut is whoami's result.
type WhoamiOut struct {
	OrganizationID string   `json:"organization_id" jsonschema:"organización a la que pertenece esta conexión"`
	UserID         string   `json:"user_id" jsonschema:"usuario que autorizó al asistente"`
	Client         string   `json:"client" jsonschema:"asistente (cliente MCP) con el que se conectó"`
	Permissions    []string `json:"permissions" jsonschema:"permisos efectivos de esta conexión: lo que el usuario consintió y todavía tiene"`
	ConsoleURL     string   `json:"console_url" jsonschema:"dirección de la consola de Odicea"`
	GitHubEnabled  bool     `json:"github_enabled" jsonschema:"si se pueden conectar repos de GitHub en esta instalación"`
}

type noInput struct{}

func (t tools) whoami(ctx context.Context, caller mcpx.Principal, _ noInput) (WhoamiOut, error) {
	out := WhoamiOut{
		OrganizationID: caller.Organization.String(),
		UserID:         caller.User.String(),
		Client:         caller.Client,
		Permissions:    append([]string{}, caller.Permissions...),
		ConsoleURL:     t.opts.ConsoleURL,
	}
	if t.opts.GitHubEnabled != nil {
		out.GitHubEnabled = t.opts.GitHubEnabled(ctx)
	}
	return out, nil
}

// ListAppsIn is list_apps's input.
type ListAppsIn struct {
	Status string `json:"status,omitempty" jsonschema:"active (por defecto) o archived"`
	Search string `json:"search,omitempty" jsonschema:"texto a buscar en el nombre o el slug"`
	Limit  int    `json:"limit,omitempty" jsonschema:"cuántas apps devolver (por defecto 20, máximo 100)"`
	Offset int    `json:"offset,omitempty" jsonschema:"desde qué posición seguir (usa next_offset de la respuesta anterior)"`
}

// App is one app in list_apps.
type App struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug" jsonschema:"nombre corto de la app: lo que pasas como app a las demás herramientas"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status" jsonschema:"active o archived"`
	ConsoleURL  string    `json:"console_url" jsonschema:"enlace a la app en la consola"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListAppsOut is list_apps's result.
type ListAppsOut struct {
	Items      []App `json:"items"`
	Total      int   `json:"total" jsonschema:"cuántas apps coinciden en total"`
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
	NextOffset *int  `json:"next_offset,omitempty" jsonschema:"offset para pedir la siguiente página; ausente si no hay más"`
}

func (t tools) listApps(ctx context.Context, caller mcpx.Principal, in ListAppsIn) (ListAppsOut, error) {
	var filter project.Filter
	switch in.Status {
	case "":
	case string(project.StatusActive), string(project.StatusArchived):
		filter.Status = []project.Status{project.Status(in.Status)}
	default:
		return ListAppsOut{}, errx.InvalidField("status", project.StatusMessage)
	}
	if in.Limit < 0 || in.Offset < 0 {
		return ListAppsOut{}, errx.InvalidField("limit", "limit and offset cannot be negative")
	}
	page, err := t.queries.List(ctx, caller.Organization, filter,
		query.Pagination{Limit: in.Limit, Offset: in.Offset, Search: strings.TrimSpace(in.Search)}.Normalize())
	if err != nil {
		return ListAppsOut{}, err
	}
	out := ListAppsOut{Items: make([]App, 0, len(page.Items)), Total: page.Page.Total, Limit: page.Page.Limit, Offset: page.Page.Offset}
	for _, p := range page.Items {
		out.Items = append(out.Items, App{ID: p.ID.String(), Slug: p.Slug, Name: p.Name, Description: p.Description,
			Status: string(p.Status), ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String(), CreatedAt: p.CreatedAt})
	}
	if next := out.Offset + len(page.Items); len(page.Items) > 0 && next < out.Total {
		out.NextOffset = &next
	}
	return out, nil
}
