// Package resourcemcp is the resource domain's MCP tools: create_database
// (creates a database and connects it to the app) and list_resources. They
// call the same project and resource Commands/Queries as the REST API.
package resourcemcp

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
}

type tools struct {
	projects project.Queries
	commands resource.Commands
	queries  resource.Queries
	opts     Options
}

// Tools are create_database and list_resources.
func Tools(projects project.Queries, commands resource.Commands, queries resource.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{projects: projects, commands: commands, queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "create_database",
			Title: "Crear una base de datos",
			Description: "Crea una base de datos (PostgreSQL por defecto; también un caché Redis o un volumen de archivos) para una app y la conecta: " +
				"la app recibirá sus variables (DATABASE_URL, …) en una versión nueva de la app que empieza sola. Los datos y las contraseñas se quedan " +
				"en el clúster: nunca se muestran. La base tarda un momento en estar lista; list_resources muestra su estado. " +
				"Creates a database (PostgreSQL by default; also a Redis cache or a files volume) for an app and connects it: " +
				"the app will receive its variables (DATABASE_URL, …) in a new version of the app that starts by itself. Data and passwords stay " +
				"in the cluster and are never shown. It takes a moment to be ready; list_resources shows its state. " +
				"app is the slug (see list_apps) or the id; environment defaults to production; kind is postgres, redis or files; " +
				"name defaults to db; prefix (like ANALYTICS) goes before the variable names so an app can use several. " +
				"En una app de varias partes, services (nombres de list_services) limita qué partes reciben las variables; sin services las recibe toda la app. " +
				"For an app with several services (parts), services (names from list_services) limits which parts receive the variables; without it the whole app does. " +
				"A files volume of such an app must name exactly one part.",
			Toolset:     "resources",
			Permissions: []string{permission.ProjectsRead, permission.ProjectsWrite},
			Risk:        mcpx.RiskWrite,
		}, t.createDatabase),
		mcpx.Typed(mcpx.Tool{
			Name:  "list_resources",
			Title: "Bases de datos y recursos de una app",
			Description: "Lista las bases de datos, cachés y volúmenes de una app, con su tipo, tamaño, estado (preparando, lista, falló) " +
				"y las variables que la app recibe. Úsala para saber si una base ya está lista. " +
				"Lists an app's databases, caches and volumes with their kind, size, state (preparing, ready, failed) " +
				"and the variables the app receives. Use it to see whether a database is ready. " +
				"app is the slug (see list_apps) or the id; environment defaults to production.",
			Toolset:     "resources",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.listResources),
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

// Resource is a resource as an assistant sees it.
type Resource struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind" jsonschema:"postgres, redis o files"`
	StorageGi int      `json:"storage_gi" jsonschema:"tamaño en GiB"`
	Status    string   `json:"status" jsonschema:"preparing (se está preparando), ready (lista), failed (falló: mira message) o deleting (se está eliminando)"`
	Message   string   `json:"message,omitempty" jsonschema:"por qué falló, si falló"`
	Connected bool     `json:"connected" jsonschema:"si la app recibe sus variables"`
	Variables []string `json:"variables" jsonschema:"nombres de las variables que la app lee (sus valores nunca se muestran)"`
	Services  []string `json:"services,omitempty" jsonschema:"partes de la app que reciben sus variables; ausente = toda la app"`
}

func view(v resource.View) Resource {
	status := "preparing"
	switch {
	case v.Status == resource.StatusDeleting:
		status = "deleting"
	case v.Phase == resource.PhaseReady:
		status = "ready"
	case v.Phase == resource.PhaseFailed:
		status = "failed"
	}
	r := Resource{Name: v.Name, Kind: string(v.Kind), StorageGi: v.StorageGi, Status: status, Message: v.Message,
		Connected: v.Connected, Variables: v.Variables}
	if len(v.Services) > 0 {
		r.Services = slices.Clone(v.Services)
	}
	return r
}

// scopeUnavailableCode is the 409 of a scope the app's cluster cannot honor
// (the same code the release and fleet domains use).
const scopeUnavailableCode = "SERVICE_SETTINGS_UNAVAILABLE"

// scoped adds the next step to a SERVICE_SETTINGS_UNAVAILABLE error.
func scoped(err error) error {
	var e *errx.Error
	if errors.As(err, &e) && e.Code == scopeUnavailableCode {
		return mcpx.NextStep(e, "El clúster de esta app todavía no puede dar algo solo a algunas partes: hazlo para toda la app (sin services) o pide a TI que actualice su agente.")
	}
	return err
}

// CreateDatabaseIn is create_database's input.
type CreateDatabaseIn struct {
	App         string   `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Environment string   `json:"environment,omitempty" jsonschema:"entorno de la app; production por defecto"`
	Kind        string   `json:"kind,omitempty" jsonschema:"postgres (por defecto), redis o files"`
	Name        string   `json:"name,omitempty" jsonschema:"nombre del recurso: minúsculas, números y guiones; db por defecto"`
	Prefix      string   `json:"prefix,omitempty" jsonschema:"prefijo en MAYÚSCULAS para las variables (ANALYTICS → ANALYTICS_DATABASE_URL), para tener varias bases"`
	Services    []string `json:"services,omitempty" jsonschema:"partes de la app que reciben las variables (nombres de list_services); vacío = toda la app. Solo para apps de varias partes; un volumen files de una app de varias partes debe nombrar exactamente una"`
}

// CreateDatabaseOut is create_database's result.
type CreateDatabaseOut struct {
	// Pending: IT's rules hold the connection for approval; nothing was created.
	mcpx.Pending
	App      string   `json:"app" jsonschema:"slug de la app"`
	Resource Resource `json:"resource"`
	// ConfigDeployment is set when a configuration deployment started.
	ConfigDeployment *string `json:"config_deployment_id,omitempty" jsonschema:"la versión nueva que conecta la base, si empezó una"`
	ConsoleURL       string  `json:"console_url" jsonschema:"enlace a la app en la consola"`
	NextStep         string  `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) createDatabase(ctx context.Context, caller mcpx.Principal, in CreateDatabaseIn) (CreateDatabaseOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App, in.Environment)
	if err != nil {
		return CreateDatabaseOut{}, err
	}
	kind := resourcekind.Kind(strings.ToLower(strings.TrimSpace(in.Kind)))
	if kind == "" {
		kind = resourcekind.Postgres
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "db"
	}
	connect := &resource.ConnectOptions{Prefix: in.Prefix}
	if len(in.Services) > 0 {
		services := slices.Clone(in.Services)
		connect.Services = &services
	}
	out, err := t.commands.Create(ctx, caller.Organization, p.ID, env.ID, resource.Create{Name: name, Kind: kind,
		Connect: connect, RequestedBy: caller.User})
	if err != nil {
		return CreateDatabaseOut{}, scoped(err)
	}
	if out.Held() {
		return CreateDatabaseOut{Pending: mcpx.PendingOf(out.Pending), App: p.Slug, NextStep: mcpx.PendingStep(out.Pending),
			ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String()}, nil
	}
	res := CreateDatabaseOut{App: p.Slug, Resource: view(out.View), ConfigDeployment: out.Deployment,
		ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String()}
	vars := strings.Join(out.Variables, ", ")
	if out.Deployment != nil {
		res.NextStep = "Listo: " + name + " se está preparando y la app recibirá " + vars + " en la versión nueva que empezó " +
			"(config_deployment_id; get_deployment la sigue). Pásale a la app esas variables por su nombre; los valores nunca se muestran. " +
			"list_resources dice cuándo la base está lista."
	} else {
		res.NextStep = "Listo: " + name + " se está preparando y la app recibirá " + vars + " cuando se publique. " +
			"Publica la app (deploy_folder o deploy_image); list_resources dice cuándo la base está lista."
	}
	return res, nil
}

// ListResourcesIn is list_resources' input.
type ListResourcesIn struct {
	App         string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Environment string `json:"environment,omitempty" jsonschema:"entorno de la app; production por defecto"`
}

// ListResourcesOut is list_resources' result.
type ListResourcesOut struct {
	App       string     `json:"app" jsonschema:"slug de la app"`
	Resources []Resource `json:"resources"`
	// Kinds are what the app's cluster can prepare.
	Kinds    []string `json:"available_kinds" jsonschema:"tipos que el clúster de la app puede crear (vacío: sin clúster conectado)"`
	NextStep string   `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) listResources(ctx context.Context, caller mcpx.Principal, in ListResourcesIn) (ListResourcesOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App, in.Environment)
	if err != nil {
		return ListResourcesOut{}, err
	}
	l, err := t.queries.List(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return ListResourcesOut{}, err
	}
	out := ListResourcesOut{App: p.Slug, Resources: make([]Resource, 0, len(l.Items)), Kinds: make([]string, 0, len(l.Kinds))}
	for _, v := range l.Items {
		out.Resources = append(out.Resources, view(v))
	}
	for _, k := range l.Kinds {
		out.Kinds = append(out.Kinds, string(k))
	}
	switch {
	case len(out.Resources) == 0:
		out.NextStep = "La app no tiene bases de datos. create_database crea una y la conecta."
	default:
		out.NextStep = "Una base 'preparing' aún no acepta conexiones: espera a que diga 'ready'. Si dice 'failed', muestra su message al usuario."
	}
	return out, nil
}
