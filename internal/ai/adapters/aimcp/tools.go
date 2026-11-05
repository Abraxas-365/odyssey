// Package aimcp is the ai domain's MCP tool: enable_ai (gives an app access
// to the organization's AI). It calls the same ai Commands as the REST API.
package aimcp

import (
	"context"
	"errors"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
}

type tools struct {
	projects project.Queries
	commands ai.Commands
	opts     Options
}

// Tools is enable_ai.
func Tools(projects project.Queries, commands ai.Commands, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{projects: projects, commands: commands, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "enable_ai",
			Title: "Dar IA a una app",
			Description: "Le da a una app acceso a los modelos de IA de la organización (chat, completions, mensajes): crea una llave propia de la app, " +
				"se la entrega al clúster sin que pase por nadie y la app la recibe como variables de entorno, en una versión nueva de la app que empieza sola. " +
				"El código de la app usa las librerías oficiales de OpenAI o Anthropic sin cambiar nada más: leen OPENAI_API_KEY, OPENAI_BASE_URL, ANTHROPIC_API_KEY y ANTHROPIC_BASE_URL solos. " +
				"La llave nunca se muestra. Si ya estaba activa no cambia nada. " +
				"Gives an app access to the organization's AI models: it makes a key just for the app, hands it to the cluster sealed, " +
				"and the app receives it as environment variables, in a new version of the app that starts by itself. " +
				"App code uses the official OpenAI or Anthropic libraries unchanged: they read OPENAI_API_KEY, OPENAI_BASE_URL, ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL by themselves. " +
				"The key is never shown. Already on: nothing changes. " +
				"app is the slug (see list_apps) or the id; environment defaults to production.",
			Toolset:     "resources",
			Permissions: []string{permission.ProjectsRead, permission.ProjectsWrite},
			Risk:        mcpx.RiskWrite,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.enable),
	}
}

// resolve finds the app by slug or id, and its environment (production by default).
func (t tools) resolve(ctx context.Context, org identity.OrganizationID, app, environment string) (project.Project, project.Environment, error) {
	app = strings.TrimSpace(app)
	if app == "" {
		return project.Project{}, project.Environment{}, errx.InvalidField("app", "app is required: the slug or id of the app")
	}
	var (
		p   project.Project
		err error
	)
	if id, perr := project.ParseID(app); perr == nil {
		p, err = t.projects.Find(ctx, org, id)
	} else {
		p, err = t.projects.FindBySlug(ctx, org, strings.ToLower(app))
	}
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return project.Project{}, project.Environment{}, mcpx.NextStep(
				errx.NotFound("app not found").WithField("app"), "Usa list_apps para ver los slugs de tu organización.")
		}
		return project.Project{}, project.Environment{}, err
	}
	slug := strings.ToLower(strings.TrimSpace(environment))
	if slug == "" {
		slug = project.ProductionSlug
	}
	env, err := t.projects.FindEnvironmentBySlug(ctx, org, p.ID, slug)
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return project.Project{}, project.Environment{}, errx.InvalidField("environment", "the app has no environment named "+slug)
		}
		return project.Project{}, project.Environment{}, err
	}
	return p, env, nil
}

// EnableAIIn is enable_ai's input.
type EnableAIIn struct {
	App         string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Environment string `json:"environment,omitempty" jsonschema:"entorno de la app; production por defecto"`
}

// EnableAIOut is enable_ai's result.
//
// It does not embed mcpx.Pending: its own `status` would shadow Pending's, and a
// held result would serialise an empty status. The held case sets Status
// itself (pending_approval) and RequestID, explicitly.
type EnableAIOut struct {
	App        string   `json:"app" jsonschema:"slug de la app"`
	Status     string   `json:"status" jsonschema:"enabling (la llave se está preparando), enabled (lista), off, o pending_approval (las reglas de TI lo dejaron esperando su aprobación: no se hizo nada todavía)"`
	RequestID  string   `json:"request_id,omitempty" jsonschema:"la solicitud de aprobación cuando status es pending_approval; get_request muestra cómo va"`
	BaseURL    string   `json:"base_url" jsonschema:"dirección de la IA que recibe la app"`
	Variables  []string `json:"variables" jsonschema:"nombres de las variables que la app lee (la llave nunca se muestra)"`
	ConsoleURL string   `json:"console_url" jsonschema:"enlace a la app en la consola"`
	NextStep   string   `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) enable(ctx context.Context, caller mcpx.Principal, in EnableAIIn) (EnableAIOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App, in.Environment)
	if err != nil {
		return EnableAIOut{}, err
	}
	v, err := t.commands.Enable(ctx, caller.Organization, p.ID, env.ID, ai.Enable{RequestedBy: caller.User})
	if err != nil {
		var ee *errx.Error
		if errors.As(err, &ee) && !errx.IsType(err, errx.TypeNotFound) && !errx.IsType(err, errx.TypeValidation) && !gate.Refusal(ee) {
			return EnableAIOut{}, mcpx.NextStep(ee, "Muestra el mensaje al usuario tal cual; si habla de un nombre de variable, quítalo con set_env (unset) y vuelve a intentar.")
		}
		return EnableAIOut{}, err
	}
	if v.Held() {
		held := mcpx.PendingOf(v.Pending)
		return EnableAIOut{App: p.Slug, Status: held.Status, RequestID: held.RequestID, NextStep: mcpx.PendingStep(v.Pending),
			ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String()}, nil
	}
	out := EnableAIOut{App: p.Slug, Status: v.Status, BaseURL: v.BaseURL, Variables: v.Variables,
		ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String()}
	out.NextStep = "La llave se está preparando y la app la recibirá en una versión nueva que empieza sola (get_app muestra su estado). " +
		"En el código de la app usa las librerías oficiales de OpenAI o Anthropic sin configurar nada: leen " + strings.Join(v.Variables, ", ") + " solos. " +
		"Los modelos disponibles están en " + v.BaseURL + "/v1/models. La llave nunca se muestra."
	if v.Status == ai.StateEnabled {
		out.NextStep = "La IA ya estaba activa. En el código de la app usa las librerías oficiales de OpenAI o Anthropic sin configurar nada: leen " +
			strings.Join(v.Variables, ", ") + " solos."
	}
	return out, nil
}
