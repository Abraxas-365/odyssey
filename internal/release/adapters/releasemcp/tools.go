// Package releasemcp is the release domain's MCP tools: an app's state
// (get_app) and its history (list_versions), and publishing it: deploy_folder
// (an upload ticket and the command that uses it), deploy_image,
// get_deployment (which can wait), get_build_logs and rollback. They call the
// same project, release and build Commands/Queries as the REST API.
package releasemcp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
	// APIURL is the API's public base URL: where upload links point
	// (deploy_folder gives <APIURL>/uploads/<ticket>).
	APIURL string
	// Repository is the repository connected to an app, for get_app. Nil, or
	// a nil result, means none. Release does not know sources: the
	// composition root builds it over source.Queries.FindConnection.
	Repository func(ctx context.Context, org identity.OrganizationID, app project.ID) (*ConnectedRepo, error)
	// PollInterval is how often get_deployment re-reads a deployment while
	// it waits (1 s by default; tests shorten it).
	PollInterval time.Duration
}

// ConnectedRepo is the repository an app publishes from.
type ConnectedRepo struct {
	Repository string `json:"repository" jsonschema:"owner/nombre en GitHub"`
	Branch     string `json:"branch" jsonschema:"rama que se publica con cada push"`
	Root       string `json:"root,omitempty" jsonschema:"carpeta de la app dentro del repo; vacía si es la raíz"`
	LastCommit string `json:"last_commit,omitempty" jsonschema:"último commit visto en la rama"`
}

// Tools are get_app, list_versions, deploy_folder, deploy_image,
// get_deployment, get_build_logs and rollback.
func Tools(projects project.Queries, commands release.Commands, queries release.Queries, builds build.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	t := tools{projects: projects, commands: commands, queries: queries, builds: builds, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "get_app",
			Title: "Estado de una app",
			Description: "Muestra el estado de una app: si está en línea, su dirección, la versión que sirve (commit y autor si vino de GitHub), " +
				"la publicación en curso y la última publicación, con el motivo si falló. Úsala para saber cómo va una app o por qué no se publicó. " +
				"También dice lo que la app usa (acceso, identidad, bases de datos, secretos, IA, salida a internet) en `uses`, y lo que le falta en `uses.attention`. " +
				"Shows an app's state: whether it is online, its URL, the version it serves (commit and author when it came from GitHub), " +
				"the publication in progress and the latest one, with the reason if it failed. Use it to see how an app is doing or why it did not publish. " +
				"It also reports what the app uses (access, identity, databases, secrets, AI, internet egress) in `uses`, and what is missing in `uses.attention`. " +
				"app is the slug (see list_apps) or the id.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsRead, permission.DeploymentsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.getApp),
		mcpx.Typed(mcpx.Tool{
			Name:  "list_versions",
			Title: "Versiones de una app",
			Description: "Lista el historial de versiones (publicaciones) de una app, de la más reciente a la más antigua, con su estado, " +
				"commit, autor y quién la publicó; marca la que está en línea. Úsala para elegir a qué versión volver o ver qué falló. " +
				"Lists an app's version (deployment) history, newest first, with status, commit, author and who published it; " +
				"it marks the one that is online. Use it to pick a version to go back to or to see what failed. " +
				"app is the slug (see list_apps) or the id. Paginated: pass next_offset as offset to read more.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsRead, permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.listVersions),
		mcpx.Typed(mcpx.Tool{
			Name:  "deploy_folder",
			Title: "Publicar una carpeta",
			Description: "Publica la carpeta de código de la máquina del usuario como una versión nueva de la app. Esta herramienta NO recibe archivos: " +
				"crea un enlace de subida de un solo uso (10 minutos) y devuelve el comando `command`, que TÚ debes ejecutar en una shell, " +
				"dentro de la carpeta de la app; sube la carpeta (respeta .gitignore y excluye .git, node_modules y .env*) y responde con el deployment_id. " +
				"No le muestres el enlace al usuario ni lo guardes. Después sigue con get_deployment (con wait_seconds) hasta que quede en línea. " +
				"Pasa siempre `message`: qué cambió, en una frase corta para el usuario; es el nombre de la versión en la consola. " +
				"Publishes the code folder on the user's machine as a new version of the app. This tool does NOT take files: " +
				"it issues a one-use upload link (10 minutes) and returns `command`, which YOU must run yourself in a shell, " +
				"inside the app's folder; it uploads the folder (honouring .gitignore, excluding .git, node_modules and .env*) and prints the deployment_id. " +
				"Do not show the link to the user or keep it. Then follow with get_deployment (with wait_seconds) until it is online. " +
				"app is the slug (see list_apps) or the id; env only takes non-secret values.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsCreate, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
		}, t.deployFolder),
		mcpx.Typed(mcpx.Tool{
			Name:  "deploy_image",
			Title: "Publicar una imagen",
			Description: "Publica una imagen de contenedor ya construida (por ejemplo por el CI del equipo) como una versión nueva de la app. " +
				"La imagen debe estar fijada por digest: registro/repositorio@sha256:<64 hex>. Para código fuente usa deploy_folder. " +
				"Publishes an already-built container image (for example from the team's CI) as a new version of the app. " +
				"The image must be pinned by digest: registry/repository@sha256:<64 hex>. For source code use deploy_folder. " +
				"app is the slug (see list_apps) or the id; env only takes non-secret values.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsCreate, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
		}, t.deployImage),
		mcpx.Typed(mcpx.Tool{
			Name:  "get_deployment",
			Title: "Estado de una publicación",
			Description: "Muestra una publicación de la app: paso actual, resultado, dirección cuando está en línea y el error con qué hacer si falló. " +
				"Sin deployment_id es la que está en curso o, si no hay, la que está en línea. Con wait_seconds (hasta 45) espera a que termine antes de responder: " +
				"úsala después de publicar, repitiéndola mientras done sea false. " +
				"Shows one publication of the app: current step, outcome, its URL once online and the error with what to do if it failed. " +
				"Without deployment_id it is the one in progress or, if none, the one online. With wait_seconds (up to 45) it waits for it to finish before answering: " +
				"use it after publishing, repeating while done is false. app is the slug (see list_apps) or the id.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsRead, permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.getDeployment),
		mcpx.Typed(mcpx.Tool{
			Name:  "get_build_logs",
			Title: "Logs del build",
			Description: "Muestra el log del build de una publicación hecha desde código (carpeta o GitHub): por defecto las últimas 200 líneas, para no llenar el contexto. " +
				"Úsala cuando get_deployment diga que el build falló. Con after (el next de la respuesta anterior) sigue leyendo desde ahí. " +
				"Shows the build log of a publication made from source (folder or GitHub): the last 200 lines by default, to spare context. " +
				"Use it when get_deployment says the build failed. With after (the previous answer's next) it keeps reading from there. " +
				"app is the slug (see list_apps) or the id; without deployment_id it is the same publication get_deployment shows.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsRead, permission.ProjectsRead},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.getBuildLogs),
		mcpx.Typed(mcpx.Tool{
			Name:  "rollback",
			Title: "Volver a una versión anterior",
			Description: "Vuelve a una versión anterior de la app que ya estuvo en línea; la versión actual deja de servirse. " +
				"Sin deployment_id elige la última versión en línea distinta de la actual (list_versions muestra el historial). " +
				"Goes back to an earlier version of the app that was online; the current version stops being served. " +
				"Without deployment_id it picks the newest online version other than the current one (list_versions shows the history). " +
				"app is the slug (see list_apps) or the id. Follow with get_deployment.",
			Toolset:     "versions",
			Permissions: []string{permission.DeploymentsRollback, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
			Annotations: mcpx.Annotations{Destructive: true},
		}, t.rollback),
		mcpx.Typed(mcpx.Tool{
			Name:  "set_access",
			Title: "Quién puede abrir la app",
			Description: "Cambia quién puede abrir la app: team (solo el equipo de la organización, lo normal), organization (toda la empresa) o " +
				"public (cualquiera con el enlace, sin iniciar sesión; exige el permiso odyssey:access:public, que suele tener solo TI). " +
				"Si la app ya está en línea, se vuelve a publicar la misma versión con el nuevo acceso. " +
				"Changes who can open the app: team (only the organization's team, the default), organization (the whole company) or " +
				"public (anyone with the link, no sign-in; needs the permission odyssey:access:public, usually only IT has it). " +
				"If the app is already online, the same version is published again with the new access. " +
				"app is the slug (see list_apps) or the id. Follow with get_deployment when next_step says so; get_app shows the current mode.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsWrite, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
		}, t.setAccess),
		mcpx.Typed(mcpx.Tool{
			Name:  "set_env",
			Title: "Variables de la app",
			Description: "Crea, cambia o quita variables de entorno de la app (configuración en texto, NO secretos: quien puede ver la app las ve). " +
				"set es nombre → valor; unset, nombres a quitar. Si la app ya está en línea, se vuelve a publicar la misma versión con las variables nuevas. " +
				"Para contraseñas, tokens o llaves usa request_secret. " +
				"Creates, changes or removes the app's environment variables (plain configuration, NOT secrets: anyone who can see the app sees them). " +
				"set is name → value; unset, names to remove. If the app is online, the same version is published again with the new variables. " +
				"For passwords, tokens or keys use request_secret. app is the slug (see list_apps) or the id.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsWrite, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
		}, t.setEnv),
		mcpx.Typed(mcpx.Tool{
			Name:  "request_secret",
			Title: "Pedir un secreto",
			Description: "Pide que una persona cargue el valor de una variable secreta (contraseña, token, llave de API) en la consola. " +
				"Tú nunca ves ni envías el valor: devuelve console_url, el enlace que le pasas al usuario; allí lo escribe y se cifra en su navegador. " +
				"Cuando lo carga, la app se vuelve a publicar sola con el secreto. Si ya existe, dice en qué estado está. " +
				"Asks a person to load a secret variable's value (password, token, API key) in the console. " +
				"You never see nor send the value: it returns console_url, the link you give the user; they type it there and it is encrypted in their browser. " +
				"Once loaded, the app is published again by itself with the secret. If it exists already, it tells its state. " +
				"Never ask the user to paste a secret in the chat. app is the slug (see list_apps) or the id.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsWrite, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.requestSecret),
	}
}

type tools struct {
	projects project.Queries
	commands release.Commands
	queries  release.Queries
	builds   build.Queries
	opts     Options
}

// resolve finds the app by slug or id in the caller's organization, and its
// production environment (the one agents publish to).
func (t tools) resolve(ctx context.Context, org identity.OrganizationID, app string) (project.Project, project.Environment, error) {
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
	env, err := t.projects.FindEnvironmentBySlug(ctx, org, p.ID, project.ProductionSlug)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, env, nil
}

// Failure is why a publication did not go live.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message" jsonschema:"qué pasó, en español claro"`
	Detail  string `json:"detail,omitempty" jsonschema:"las palabras del clúster o del build, si dijo algo"`
}

// Version is one publication of an app.
type Version struct {
	DeploymentID  string     `json:"deployment_id"`
	Status        string     `json:"status" jsonschema:"building, pending, applying, verifying, ready, failed o cancelled"`
	Step          string     `json:"step,omitempty" jsonschema:"paso en el que está (o en el que falló): build, apply, wait_ready, promote"`
	Reason        string     `json:"reason" jsonschema:"deploy (nueva versión), rollback (volvió a una anterior), auto_rollback (la plataforma restauró la anterior tras un fallo) o config (la misma versión otra vez, por un cambio de quién puede abrirla)"`
	Image         string     `json:"image,omitempty" jsonschema:"imagen corta; vacía mientras se construye"`
	Message       string     `json:"message,omitempty" jsonschema:"qué cambió, en palabras de quien la pidió (el message de deploy_folder o deploy_image)"`
	Repository    string     `json:"repository,omitempty" jsonschema:"repo de GitHub de donde vino (owner/nombre)"`
	Branch        string     `json:"branch,omitempty"`
	Commit        string     `json:"commit,omitempty" jsonschema:"SHA completo del commit si vino de GitHub"`
	Title         string     `json:"title,omitempty" jsonschema:"primera línea del mensaje del commit"`
	Author        string     `json:"author,omitempty" jsonschema:"autor del commit"`
	CreatedBy     string     `json:"created_by,omitempty" jsonschema:"usuario que la publicó; vacío si la inició la plataforma"`
	CreatedByName string     `json:"created_by_name,omitempty" jsonschema:"nombre de quien la publicó según el directorio de la empresa al publicarla; vacío si aún no se conoce"`
	RequestedVia  string     `json:"requested_via,omitempty" jsonschema:"desde dónde se pidió: console, assistant:<preset>, github o platform; vacío en versiones antiguas"`
	Error         *Failure   `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	IsCurrent     bool       `json:"is_current" jsonschema:"si es la versión que está en línea ahora"`
}

func version(d release.Deployment, current *release.Deployment) Version {
	v := Version{
		DeploymentID: d.ID.String(),
		Status:       string(d.Status),
		Step:         d.Step,
		Reason:       string(d.Reason),
		Image:        shortImage(d.Image),
		Message:      d.Message,
		CreatedAt:    d.CreatedAt,
		FinishedAt:   d.FinishedAt,
		IsCurrent:    current != nil && current.ID == d.ID,
		RequestedVia: d.RequestedVia,
	}
	if !d.RequestedBy.IsZero() {
		v.CreatedBy, v.CreatedByName = d.RequestedBy.String(), d.RequestedByName
	}
	if d.Source != nil && d.Source.Git != nil {
		g := d.Source.Git
		v.Repository, v.Branch, v.Commit, v.Title, v.Author = g.Repository, g.Branch, g.Commit, g.Title, g.Author
	}
	if d.Error != nil {
		v.Error = &Failure{Code: d.Error.Code, Message: d.Error.Message, Detail: d.Error.Detail}
	}
	return v
}

// shortImage drops the registry path so a version reads "visitas:abc123".
func shortImage(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if len(image) > 80 {
		image = image[:80]
	}
	return image
}

// GetAppIn is get_app's input.
type GetAppIn struct {
	App string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
}

// GetAppOut is get_app's result.
type GetAppOut struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status" jsonschema:"estado del proyecto: active o archived"`
	ConsoleURL  string `json:"console_url" jsonschema:"enlace a la app en la consola"`
	// State is what a user sees: online, publishing, failed or not_published.
	State string `json:"state" jsonschema:"online (hay una versión en línea), publishing (se está publicando una versión), failed (nunca estuvo en línea y la última publicación falló) o not_published (aún sin publicar)"`
	URL   string `json:"url,omitempty" jsonschema:"dirección pública de la app; vacía hasta que se publica"`
	// NeedsAttention is true when the latest publication failed or something
	// in Uses.Attention needs a person (who is not wait).
	NeedsAttention bool     `json:"needs_attention" jsonschema:"true si la última publicación falló (mira last_deployment.error) o si algo en uses.attention necesita a una persona (who owner o it; lo que está en curso, who wait, no cuenta); también true si no se pudo leer nada de uses"`
	Current        *Version `json:"current,omitempty" jsonschema:"la versión que está en línea"`
	// Access is who can open the app: the settings in force.
	Access         *AccessOut `json:"access,omitempty" jsonschema:"quién puede abrir la app: mode es team (solo el equipo), organization (toda la empresa) o public (cualquiera con el enlace); pending es true mientras se aplica un cambio"`
	Active         *Version   `json:"active,omitempty" jsonschema:"la publicación en curso, si hay"`
	LastDeployment *Version   `json:"last_deployment,omitempty" jsonschema:"la publicación más reciente, termine como termine"`
	// Uses is what the app depends on and what is missing.
	Uses UsesOut `json:"uses" jsonschema:"lo que usa la app (acceso, identidad, bases de datos, secretos, IA, salida a internet) y lo que le falta: no hace falta llamar a otras tools para saberlo"`
	// Repository is the GitHub repository the app publishes from, if connected.
	Repository *ConnectedRepo `json:"repository,omitempty" jsonschema:"repo de GitHub conectado: cada push a su rama publica la app"`
}

func (t tools) getApp(ctx context.Context, caller mcpx.Principal, in GetAppIn) (GetAppOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return GetAppOut{}, err
	}
	cur, err := t.queries.Current(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return GetAppOut{}, err
	}
	last, err := t.queries.ListDeployments(ctx, caller.Organization, p.ID, env.ID, release.DeploymentFilter{}, query.Pagination{Limit: 1}.Normalize())
	if err != nil {
		return GetAppOut{}, err
	}
	out := GetAppOut{ID: p.ID.String(), Slug: p.Slug, Name: p.Name, Description: p.Description, Status: string(p.Status),
		ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String(), State: "not_published"}
	// Uses reads the settings once (access comes from it too). It never fails
	// get_app: its sources degrade on their own, and if even the read model
	// cannot be made the app is shown with every section unknown.
	uses, err := t.queries.Uses(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		slog.Warn("get_app: uses unavailable", "app", p.ID, "error", err)
		out.Uses = usesUnavailable()
		// nothing could be read: say so rather than "all is fine"
		out.NeedsAttention = true
	} else {
		out.Uses = usesOf(uses)
		out.Access = &AccessOut{Mode: string(uses.Access.Mode), Pending: uses.Access.Pending}
		out.NeedsAttention = uses.NeedsPeople()
	}
	if cur.Deployment != nil {
		v := version(*cur.Deployment, cur.Deployment)
		out.Current, out.State, out.URL = &v, "online", cur.URL
	}
	if cur.Active != nil {
		v := version(*cur.Active, cur.Deployment)
		out.Active, out.State = &v, "publishing"
	}
	if len(last.Items) > 0 {
		d := last.Items[0]
		v := version(d, cur.Deployment)
		out.LastDeployment = &v
		if d.Status == release.StatusFailed {
			out.NeedsAttention = true
			if out.State == "not_published" {
				out.State = "failed"
			}
		}
	}
	if t.opts.Repository != nil {
		repo, err := t.opts.Repository(ctx, caller.Organization, p.ID)
		if err != nil && !errx.IsType(err, errx.TypeNotFound) {
			return GetAppOut{}, err
		}
		if err == nil {
			out.Repository = repo
		}
	}
	return out, nil
}

// Version listing limits.
const (
	defaultVersions = 10
	maxVersions     = 50
)

// ListVersionsIn is list_versions's input.
type ListVersionsIn struct {
	App    string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"cuántas versiones devolver (por defecto 10, máximo 50)"`
	Offset int    `json:"offset,omitempty" jsonschema:"desde qué posición seguir (usa next_offset de la respuesta anterior)"`
}

// ListVersionsOut is list_versions's result.
type ListVersionsOut struct {
	App        string    `json:"app" jsonschema:"slug de la app"`
	Items      []Version `json:"items" jsonschema:"de la más reciente a la más antigua"`
	Total      int       `json:"total"`
	Limit      int       `json:"limit"`
	Offset     int       `json:"offset"`
	NextOffset *int      `json:"next_offset,omitempty" jsonschema:"offset para pedir la siguiente página; ausente si no hay más"`
}

func (t tools) listVersions(ctx context.Context, caller mcpx.Principal, in ListVersionsIn) (ListVersionsOut, error) {
	if in.Limit < 0 || in.Offset < 0 {
		return ListVersionsOut{}, errx.InvalidField("limit", "limit and offset cannot be negative")
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultVersions
	}
	limit = min(limit, maxVersions)
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return ListVersionsOut{}, err
	}
	cur, err := t.queries.Current(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return ListVersionsOut{}, err
	}
	page, err := t.queries.ListDeployments(ctx, caller.Organization, p.ID, env.ID, release.DeploymentFilter{},
		query.Pagination{Limit: limit, Offset: in.Offset}.Normalize())
	if err != nil {
		return ListVersionsOut{}, err
	}
	out := ListVersionsOut{App: p.Slug, Items: make([]Version, 0, len(page.Items)), Total: page.Page.Total, Limit: page.Page.Limit, Offset: page.Page.Offset}
	for _, d := range page.Items {
		out.Items = append(out.Items, version(d, cur.Deployment))
	}
	if next := out.Offset + len(page.Items); len(page.Items) > 0 && next < out.Total {
		out.NextOffset = &next
	}
	return out, nil
}
