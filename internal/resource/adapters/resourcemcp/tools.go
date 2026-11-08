// Package resourcemcp is the resource domain's MCP tools: list_catalog (what
// the app can ask for, and how), create_resource (creates any kind through
// the catalog and, by default, connects it), create_database (its alias from
// before the catalog) and list_resources. They call the same project and
// resource Commands/Queries as the REST API.
package resourcemcp

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
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

// Tools are list_catalog, create_resource, create_database and list_resources.
func Tools(projects project.Queries, commands resource.Commands, queries resource.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	t := tools{projects: projects, commands: commands, queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "list_catalog",
			Title: "Qué recursos puede pedir una app",
			Description: "Muestra qué bases de datos, cachés y volúmenes puede pedir una app según lo que ofrece TI para su nivel y lo que su clúster puede preparar: " +
				"cada receta con sus parámetros (límites y rango preaprobado), las variables que recibe la app y si se crea al instante o pide aprobación de TI; " +
				"y los tipos que no se pueden pedir, con el motivo. Úsala antes de create_resource. " +
				"Shows what databases, caches and volumes an app can ask for, given what IT offers for its tier and what its cluster can prepare: " +
				"each recipe with its parameters (limits and pre-approved range), the variables the app receives and whether it is created at once or asks IT; " +
				"and the kinds that cannot be asked for, with why. Use it before create_resource. " +
				"app is the slug (see list_apps) or the id; environment defaults to production.",
			Toolset:     "resources",
			Permissions: []string{permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.listCatalog),
		mcpx.Typed(mcpx.Tool{
			Name:  "create_resource",
			Title: "Crear un recurso",
			Description: "Crea un recurso del catálogo de la empresa (una base de datos postgres, una caché redis o un volumen files) para una app y, salvo connect=false, lo conecta: " +
				"la app recibe sus variables (DATABASE_URL, …) en una versión nueva que empieza sola. Sin recipe usa la receta por defecto del tipo para el nivel de la app; " +
				"params (por ejemplo {\"storage_gi\": 20}) dentro del rango preaprobado se crea al instante; fuera de él queda pendiente de TI (pending_approval). " +
				"list_catalog muestra recetas, límites y variables. Los datos y las contraseñas nunca se muestran. " +
				"Creates a resource from the company's catalog (a postgres database, a redis cache or a files volume) for an app and, unless connect=false, connects it: " +
				"the app receives its variables in a new version that starts by itself. Without recipe it uses the kind's default for the app's tier; " +
				"params within the pre-approved range are created at once, outside it the request waits for IT (pending_approval). " +
				"list_catalog shows recipes, limits and variables. " +
				"app is the slug or the id; environment defaults to production; name defaults to db; prefix (like ANALYTICS) goes before the variable names; " +
				"services (names from list_services) limits which parts of a multi-service app receive the variables (a files volume names exactly one).",
			Toolset:     "resources",
			Permissions: []string{permission.ProjectsRead, permission.ProjectsWrite},
			Risk:        mcpx.RiskWrite,
		}, t.createResource),
		mcpx.Typed(mcpx.Tool{
			Name:  "create_database",
			Title: "Crear una base de datos",
			Description: "Crea una base de datos (PostgreSQL por defecto; también un caché Redis o un volumen de archivos) para una app y la conecta: " +
				"la app recibirá sus variables (DATABASE_URL, …) en una versión nueva de la app que empieza sola. Los datos y las contraseñas se quedan " +
				"en el clúster: nunca se muestran. La base tarda un momento en estar lista; list_resources muestra su estado. " +
				"Es la forma corta de create_resource con la receta por defecto (create_resource elige receta y parámetros; list_catalog los muestra). " +
				"Creates a database (PostgreSQL by default; also a Redis cache or a files volume) for an app and connects it: " +
				"the app will receive its variables (DATABASE_URL, …) in a new version of the app that starts by itself. Data and passwords stay " +
				"in the cluster and are never shown. It takes a moment to be ready; list_resources shows its state. " +
				"It is create_resource with the default recipe. " +
				"app is the slug (see list_apps) or the id; environment defaults to production; kind is postgres, redis or files; " +
				"name defaults to db; storage_gi is the size (the recipe's default when absent); prefix (like ANALYTICS) goes before the variable names so an app can use several. " +
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
	StorageGi   int      `json:"storage_gi,omitempty" jsonschema:"tamaño en GiB; el de la receta por defecto si no se indica (list_catalog muestra límites y rango preaprobado)"`
	Prefix      string   `json:"prefix,omitempty" jsonschema:"prefijo en MAYÚSCULAS para las variables (ANALYTICS → ANALYTICS_DATABASE_URL), para tener varias bases"`
	Services    []string `json:"services,omitempty" jsonschema:"partes de la app que reciben las variables (nombres de list_services); vacío = toda la app. Solo para apps de varias partes; un volumen files de una app de varias partes debe nombrar exactamente una"`
}

// CreateDatabaseOut is create_database's (and create_resource's) result.
type CreateDatabaseOut struct {
	// Pending: the catalog or IT's rules hold it for approval; nothing was created.
	mcpx.Pending
	App      string   `json:"app" jsonschema:"slug de la app"`
	Resource Resource `json:"resource"`
	// ConfigDeployment is set when a configuration deployment started.
	ConfigDeployment *string `json:"config_deployment_id,omitempty" jsonschema:"la versión nueva que conecta el recurso, si empezó una"`
	ConsoleURL       string  `json:"console_url" jsonschema:"enlace a la app en la consola"`
	NextStep         string  `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) createDatabase(ctx context.Context, caller mcpx.Principal, in CreateDatabaseIn) (CreateDatabaseOut, error) {
	var params map[string]int
	if in.StorageGi != 0 {
		params = map[string]int{catalog.ParamStorage: in.StorageGi}
	}
	return t.create(ctx, caller, CreateResourceIn{App: in.App, Environment: in.Environment, Kind: in.Kind, Name: in.Name,
		Params: params, Prefix: in.Prefix, Services: in.Services})
}

// CreateResourceIn is create_resource's input.
type CreateResourceIn struct {
	App         string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Environment string `json:"environment,omitempty" jsonschema:"entorno de la app; production por defecto"`
	Kind        string `json:"kind,omitempty" jsonschema:"postgres (por defecto), redis o files"`
	Name        string `json:"name,omitempty" jsonschema:"nombre del recurso: minúsculas, números y guiones; db por defecto"`
	Recipe      string `json:"recipe,omitempty" jsonschema:"receta del catálogo (list_catalog las muestra); sin recipe, la receta por defecto del tipo para el nivel de la app"`
	// Params are the recipe's parameters.
	Params map[string]int `json:"params,omitempty" jsonschema:"parámetros de la receta por nombre, por ejemplo {\"storage_gi\": 20}; los que falten toman su valor por defecto"`
	// Connect false creates it without connecting it.
	Connect  *bool    `json:"connect,omitempty" jsonschema:"false para crearlo sin conectarlo a la app (por defecto se conecta)"`
	Prefix   string   `json:"prefix,omitempty" jsonschema:"prefijo en MAYÚSCULAS para las variables (ANALYTICS → ANALYTICS_DATABASE_URL), para tener varios recursos del mismo tipo"`
	Services []string `json:"services,omitempty" jsonschema:"partes de la app que reciben las variables (nombres de list_services); vacío = toda la app. Un volumen files de una app de varias partes debe nombrar exactamente una"`
}

func (t tools) createResource(ctx context.Context, caller mcpx.Principal, in CreateResourceIn) (CreateDatabaseOut, error) {
	return t.create(ctx, caller, in)
}

func (t tools) create(ctx context.Context, caller mcpx.Principal, in CreateResourceIn) (CreateDatabaseOut, error) {
	connects := in.Connect == nil || *in.Connect
	if !connects && (in.Prefix != "" || len(in.Services) > 0) {
		return CreateDatabaseOut{}, errx.InvalidField("connect", "prefix and services are how it connects: leave connect out (or true) to use them")
	}
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
	req := resource.Create{Name: name, Kind: kind, Recipe: strings.ToLower(strings.TrimSpace(in.Recipe)),
		Params: maps.Clone(in.Params), RequestedBy: caller.User}
	if connects {
		req.Connect = &resource.ConnectOptions{Prefix: in.Prefix}
		if len(in.Services) > 0 {
			services := slices.Clone(in.Services)
			req.Connect.Services = &services
		}
	}
	out, err := t.commands.Create(ctx, caller.Organization, p.ID, env.ID, req)
	if err != nil {
		return CreateDatabaseOut{}, catalogued(scoped(err))
	}
	console := t.opts.ConsoleURL + "/apps/" + p.ID.String()
	if out.Held() {
		return CreateDatabaseOut{Pending: mcpx.PendingOf(out.Pending), App: p.Slug, NextStep: mcpx.PendingStep(out.Pending),
			ConsoleURL: console}, nil
	}
	res := CreateDatabaseOut{App: p.Slug, Resource: view(out.View), ConfigDeployment: out.Deployment, ConsoleURL: console}
	vars := strings.Join(out.Variables, ", ")
	switch {
	case !connects:
		res.NextStep = "Listo: " + name + " se está preparando y no está conectado a la app. Conéctalo desde la consola cuando la app lo necesite; " +
			"list_resources dice cuándo está listo."
	case out.Deployment != nil:
		res.NextStep = "Listo: " + name + " se está preparando y la app recibirá " + vars + " en la versión nueva que empezó " +
			"(config_deployment_id; get_deployment la sigue). Pásale a la app esas variables por su nombre; los valores nunca se muestran. " +
			"list_resources dice cuándo está listo."
	default:
		res.NextStep = "Listo: " + name + " se está preparando y la app recibirá " + vars + " cuando se publique. " +
			"Publica la app (deploy_folder o deploy_image); list_resources dice cuándo está listo."
	}
	return res, nil
}

// catalogSteps are the next_step of the catalog's refusals.
var catalogSteps = map[string]string{
	catalog.CodeUnavailable: "TI no ofrece eso para esta app: muéstrale el mensaje al usuario; list_catalog dice qué sí se puede pedir.",
	catalog.CodeOutOfLimits: "El valor está fuera de los límites que TI fijó: list_catalog muestra el mínimo y el máximo de cada parámetro. " +
		"No reintentes con otro valor sin preguntarle al usuario.",
	catalog.CodeChooseRecipe:  "Hay varias recetas para ese tipo: pregúntale al usuario cuál quiere (list_catalog las describe) y pásala en recipe.",
	catalog.CodeRecipeChanged: "TI cambió esa receta mientras tanto: vuelve a mirar list_catalog y pregúntale al usuario antes de pedirla de nuevo.",
	resourcekind.CodeUnavailable: "El clúster de esta app no puede preparar ese tipo: es de TI. Muéstrale el mensaje al usuario; " +
		"list_catalog dice qué sí se puede pedir.",
}

// clusterCannotStep is CodeUnavailable's step when the offering exists but
// the cluster's agent cannot honour the recipe (details.missing).
const clusterCannotStep = "El clúster de esta app todavía no puede preparar esa receta (su agente es anterior): es de TI. " +
	"Muéstrale el mensaje al usuario; list_catalog dice qué recetas sí puede preparar."

// fieldStep is the step of a recipe or parameter the catalog does not know.
const fieldStep = "Esa receta o parámetro no existe para ese tipo: list_catalog muestra las recetas y sus parámetros (usa sus nombres tal cual)."

// catalogued adds the next step to a catalog refusal.
func catalogued(err error) error {
	var e *errx.Error
	if !errors.As(err, &e) {
		return err
	}
	if _, missing := e.Details["missing"]; e.Code == catalog.CodeUnavailable && missing {
		return mcpx.NextStep(e, clusterCannotStep)
	}
	if step, ok := catalogSteps[e.Code]; ok {
		return mcpx.NextStep(e, step)
	}
	if field, _ := e.Details["field"].(string); e.Type == errx.TypeValidation &&
		(field == "recipe" || field == "params" || strings.HasPrefix(field, "params.")) {
		return mcpx.NextStep(e, fieldStep)
	}
	return err
}

// ListCatalogIn is list_catalog's input.
type ListCatalogIn struct {
	App         string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Environment string `json:"environment,omitempty" jsonschema:"entorno de la app; production por defecto"`
}

// ListCatalogOut is list_catalog's result.
type ListCatalogOut struct {
	App         string        `json:"app" jsonschema:"slug de la app"`
	Tier        string        `json:"tier" jsonschema:"nivel de la app (sandbox, internal o production): TI ofrece recetas distintas por nivel"`
	Recipes     []Offer       `json:"recipes" jsonschema:"lo que se puede crear, por tipo y con la receta por defecto de cada tipo primero"`
	Unavailable []Unavailable `json:"unavailable" jsonschema:"tipos que no se pueden crear en esta app, con el motivo"`
	CanCreate   bool          `json:"can_create" jsonschema:"si quien pregunta puede crear recursos en esta app (permiso de editar apps)"`
	ConsoleURL  string        `json:"console_url" jsonschema:"enlace a la app en la consola"`
	NextStep    string        `json:"next_step" jsonschema:"qué hacer ahora"`
}

// Offer is one recipe as an assistant sees it.
type Offer struct {
	Kind        string     `json:"kind" jsonschema:"postgres, redis o files"`
	Recipe      string     `json:"recipe" jsonschema:"lo que se pasa en create_resource.recipe"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Default     bool       `json:"default" jsonschema:"la que se usa si no se indica recipe"`
	Params      []Param    `json:"params"`
	Variables   []Variable `json:"variables" jsonschema:"las variables que la app recibe al conectarla (los valores nunca se muestran)"`
	Approval    string     `json:"approval" jsonschema:"instant (con los valores por defecto y conectada a toda la app se crea al instante; un volumen de archivos en una app de varias partes se juzga al nombrar la parte), needs_approval (TI tiene que aprobarla) o denied (una regla de TI no lo permite)"`
	Rules       []string   `json:"rules,omitempty" jsonschema:"las reglas de TI que piden aprobación o lo impiden"`
}

// Param is a recipe parameter with its limits.
type Param struct {
	Name           string `json:"name" jsonschema:"la clave en create_resource.params"`
	Label          string `json:"label"`
	Unit           string `json:"unit,omitempty"`
	Min            int    `json:"min" jsonschema:"menor valor permitido"`
	Max            int    `json:"max" jsonschema:"mayor valor permitido"`
	Default        int    `json:"default"`
	PreapprovedMin int    `json:"preapproved_min" jsonschema:"desde aquí hasta preapproved_max se crea sin pedir aprobación"`
	PreapprovedMax int    `json:"preapproved_max" jsonschema:"por encima (hasta max) TI tiene que aprobarlo"`
	Grows          bool   `json:"grows_only,omitempty" jsonschema:"solo puede crecer después de creado"`
	Fixed          bool   `json:"fixed,omitempty" jsonschema:"no se puede cambiar después de creado"`
}

// Variable is one variable the app receives.
type Variable struct {
	Name    string `json:"name"`
	Secret  bool   `json:"secret" jsonschema:"si es una contraseña o algo que la contiene"`
	Meaning string `json:"meaning"`
}

// Unavailable is a kind that cannot be created in the app.
type Unavailable struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// approvalOf words a verdict for an assistant.
func approvalOf(e gate.Effect) string {
	switch e {
	case gate.Deny:
		return "denied"
	case gate.Approval:
		return "needs_approval"
	}
	return "instant"
}

func (t tools) listCatalog(ctx context.Context, caller mcpx.Principal, in ListCatalogIn) (ListCatalogOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App, in.Environment)
	if err != nil {
		return ListCatalogOut{}, err
	}
	o, err := t.queries.Offered(ctx, caller.Organization, p.ID, env.ID, caller.User)
	if err != nil {
		return ListCatalogOut{}, err
	}
	out := ListCatalogOut{App: p.Slug, Tier: string(o.Tier), Recipes: make([]Offer, 0, len(o.Recipes)),
		Unavailable: make([]Unavailable, 0, len(o.Unavailable)), ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String()}
	for _, r := range o.Recipes {
		offer := Offer{Kind: string(r.Kind), Recipe: r.Recipe, Title: r.Title, Description: r.Description, Default: r.Default,
			Params: make([]Param, 0, len(r.Params)), Variables: make([]Variable, 0, len(r.Variables)), Approval: approvalOf(r.Verdict)}
		if len(r.Rules) > 0 {
			offer.Rules = slices.Clone(r.Rules)
		}
		for _, x := range r.Params {
			offer.Params = append(offer.Params, Param{Name: x.Name, Label: x.Label, Unit: x.Unit, Min: x.Min, Max: x.Max, Default: x.Default,
				PreapprovedMin: x.PreapprovedMin, PreapprovedMax: x.PreapprovedMax,
				Grows: x.Mutability == catalog.Grows, Fixed: x.Mutability == catalog.Immutable})
		}
		for _, v := range r.Variables {
			offer.Variables = append(offer.Variables, Variable{Name: v.Name, Secret: v.Secret, Meaning: v.Meaning})
		}
		out.Recipes = append(out.Recipes, offer)
	}
	for _, u := range o.Unavailable {
		out.Unavailable = append(out.Unavailable, Unavailable{Kind: string(u.Kind), Reason: u.Reason})
	}
	out.CanCreate = caller.Can(permission.ProjectsWrite)
	switch {
	case len(out.Recipes) == 0:
		out.NextStep = "Esta app no puede pedir recursos ahora: muéstrale al usuario los motivos de unavailable; lo resuelve TI."
	case !out.CanCreate:
		out.NextStep = "Puedes ver el catálogo pero no crear recursos en esta app: pídeselo a alguien que pueda editarla."
	default:
		out.NextStep = "create_resource crea uno (kind, y recipe si no es la de por defecto). Con approval=instant y params dentro del rango preaprobado " +
			"se crea al instante; fuera de él, o con needs_approval, queda pendiente de TI: díselo al usuario antes de pedirlo."
	}
	return out, nil
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
