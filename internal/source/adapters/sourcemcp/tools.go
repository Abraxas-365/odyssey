// Package sourcemcp is the source domain's MCP tools: the GitHub repositories
// the organization reaches (list_repositories), connecting one to an app so
// each push publishes it (connect_repository) and publishing its branch now
// (publish_now). They call the same source.Commands and source.Queries as the
// REST API; detect_services proposes an app's parts from a repository's tree
// or a folder's file names (project.Detect).
package sourcemcp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// Options are the deployment facts the tools link to.
type Options struct {
	// ConsoleURL is the console's base URL (links for the user).
	ConsoleURL string
	// PollInterval is how often connect_repository and publish_now look for
	// the push's deployment (1 s by default; tests shorten it).
	PollInterval time.Duration
	// WaitTimeout is the longest they wait for it (10 s by default).
	WaitTimeout time.Duration
}

// Tools are list_repositories, connect_repository, publish_now and
// detect_services.
func Tools(projects project.Queries, commands source.Commands, queries source.Queries, opts Options) []mcpx.Tool {
	opts.ConsoleURL = strings.TrimRight(opts.ConsoleURL, "/")
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = 10 * time.Second
	}
	t := tools{projects: projects, commands: commands, queries: queries, opts: opts}
	return []mcpx.Tool{
		mcpx.Typed(mcpx.Tool{
			Name:  "list_repositories",
			Title: "Repositorios de GitHub",
			Description: "Lista los repositorios de GitHub a los que llega la organización (los de las cuentas donde está instalada la GitHub App de Odicea), " +
				"con su rama por defecto. Úsala para encontrar el repositorio antes de connect_repository. Si no hay ninguna cuenta conectada, " +
				"el next_step trae el enlace de la consola para instalarla (requiere un navegador). " +
				"Lists the GitHub repositories the organization reaches (those of the accounts where Odicea's GitHub App is installed), " +
				"with their default branch. Use it to find the repository before connect_repository. If no account is connected, " +
				"next_step carries the console link to install it (it needs a browser). Paginated: pass next_offset as offset to read more.",
			Toolset:     "github",
			Permissions: []string{permission.ProjectsWrite},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.listRepositories),
		mcpx.Typed(mcpx.Tool{
			Name:  "connect_repository",
			Title: "Conectar un repositorio",
			Description: "Conecta un repositorio de GitHub a una app: desde ahora cada push a la rama publica la app, y se publica el último commit ya. " +
				"repository es owner/nombre (o el id numérico de list_repositories); branch por defecto es la rama principal del repo; " +
				"root es la carpeta de la app dentro del repo (vacía si es la raíz). Espera unos segundos a que empiece la publicación y la devuelve. " +
				"Si el repo tiene varias apps (un monorepo), antes propón sus partes con detect_services (repository), guárdalas con set_services y conecta sin root: " +
				"cada parte toma su carpeta y en cada push solo se construyen las que cambiaron. " +
				"Connects a GitHub repository to an app: from now on every push to the branch publishes the app, and the latest commit is published right away. " +
				"repository is owner/name (or the numeric id from list_repositories); branch defaults to the repository's main branch; " +
				"root is the app's folder inside the repository (empty for its root). It waits a few seconds for the publication to start and returns it. " +
				"For a repository with several apps (a monorepo), first propose its parts with detect_services (repository), save them with set_services and connect " +
				"without root: each part takes its folder and every push builds only the changed ones. " +
				"app is the slug (see list_apps) or the id; env only takes non-secret values.",
			Toolset:     "github",
			Permissions: []string{permission.ProjectsWrite, permission.DeploymentsCreate},
			Risk:        mcpx.RiskWrite,
		}, t.connectRepository),
		mcpx.Typed(mcpx.Tool{
			Name:  "publish_now",
			Title: "Publicar ahora",
			Description: "Publica ya el último commit de la rama conectada de la app, sin esperar un push. Espera unos segundos a que empiece la publicación y la devuelve. " +
				"En una app de varias partes solo se construyen las que cambiaron; rebuild: true las construye todas de nuevo. " +
				"Publishes the latest commit of the app's connected branch right now, without waiting for a push. It waits a few seconds for the publication to start and returns it. " +
				"In an app of several parts only the changed ones build; rebuild: true builds them all again. " +
				"app is the slug (see list_apps) or the id. The app needs a connected repository (connect_repository).",
			Toolset:     "github",
			Permissions: []string{permission.DeploymentsCreate, permission.ProjectsRead},
			Risk:        mcpx.RiskWrite,
		}, t.publishNow),
		mcpx.Typed(mcpx.Tool{
			Name:  "detect_services",
			Title: "Proponer las partes de una app",
			Description: "Propone las partes (servicios) de un código que tiene varias apps en un mismo repositorio o carpeta (un monorepo: por ejemplo apps/web y apps/api): " +
				"qué carpetas son apps, cómo llamarlas, qué ruta de la dirección atiende cada una y cuáles son workers. Solo lee nombres de archivos, nunca su contenido, " +
				"y no guarda nada: muestra la propuesta al usuario, ajústala si hace falta y guárdala con set_services. " +
				"Para una carpeta del usuario pasa paths: las rutas de sus archivos relativas a la carpeta que se publica (basta con las que terminan en " +
				strings.Join(project.DetectFiles(), ", ") + "; sin node_modules ni .git). Para un repositorio de GitHub pasa repository (y branch si no es la principal); Odicea lee su árbol. " +
				"root limita la búsqueda a una carpeta. " +
				"Proposes the parts (services) of code that holds several apps in one repository or folder (a monorepo: say apps/web and apps/api): which folders are apps, " +
				"what to call them, which address path each one answers and which are workers. It reads file names only, never contents, and saves nothing: " +
				"show the proposal to the user, adjust it if needed and save it with set_services. For a folder of the user pass paths: its file paths relative to the folder " +
				"that is published (those ending in one of the names above are enough; no node_modules or .git). For a GitHub repository pass repository (and branch when it is not " +
				"the main one); Odicea reads its tree. root limits the search to a folder.",
			Toolset:     "apps",
			Permissions: []string{permission.ProjectsWrite},
			Risk:        mcpx.RiskRead,
			Annotations: mcpx.Annotations{Idempotent: true},
		}, t.detectServices),
	}
}

type tools struct {
	projects project.Queries
	commands source.Commands
	queries  source.Queries
	opts     Options
}

// resolve finds the app by slug or id in the caller's organization.
func (t tools) resolve(ctx context.Context, org identity.OrganizationID, app string) (project.Project, error) {
	app = strings.TrimSpace(app)
	if app == "" {
		return project.Project{}, errx.InvalidField("app", "app is required: the slug or id of the app")
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
	if errx.IsType(err, errx.TypeNotFound) {
		return project.Project{}, mcpx.NextStep(errx.NotFound("app not found").WithField("app"),
			"Usa list_apps para ver los slugs de tu organización, o create_app para crearla.")
	}
	return p, err
}

// settingsURL is the console page that installs the GitHub App.
func (t tools) settingsURL() string { return t.opts.ConsoleURL + source.SettingsPage }

// enabled refuses with a next_step when this Odicea has no GitHub App.
func (t tools) enabled(ctx context.Context) error {
	if t.queries.Info(ctx).Enabled {
		return nil
	}
	return mcpx.NextStep(errx.Business(source.Message(source.CodeNotConfigured)).WithCode(source.CodeNotConfigured),
		"GitHub no está habilitado en este Odicea; pide a TI que lo configure. Mientras tanto publica la carpeta con deploy_folder.")
}

// Repository is a GitHub repository the organization reaches.
type Repository struct {
	ID            int64  `json:"id" jsonschema:"id numérico en GitHub"`
	FullName      string `json:"full_name" jsonschema:"owner/nombre"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// ListRepositoriesIn is list_repositories's input.
type ListRepositoriesIn struct {
	Search string `json:"search,omitempty" jsonschema:"texto a buscar en owner/nombre"`
	Limit  int    `json:"limit,omitempty" jsonschema:"cuántos devolver (por defecto 20, máximo 100)"`
	Offset int    `json:"offset,omitempty" jsonschema:"desde qué posición seguir (usa next_offset de la respuesta anterior)"`
}

// ListRepositoriesOut is list_repositories's result.
type ListRepositoriesOut struct {
	Items      []Repository `json:"items"`
	Total      int          `json:"total"`
	Limit      int          `json:"limit"`
	Offset     int          `json:"offset"`
	NextOffset *int         `json:"next_offset,omitempty" jsonschema:"offset para pedir la siguiente página; ausente si no hay más"`
	NextStep   string       `json:"next_step,omitempty" jsonschema:"qué hacer ahora"`
}

func (t tools) listRepositories(ctx context.Context, caller mcpx.Principal, in ListRepositoriesIn) (ListRepositoriesOut, error) {
	if in.Limit < 0 || in.Offset < 0 {
		return ListRepositoriesOut{}, errx.InvalidField("limit", "limit and offset cannot be negative")
	}
	if err := t.enabled(ctx); err != nil {
		return ListRepositoriesOut{}, err
	}
	search := strings.TrimSpace(in.Search)
	page, err := t.queries.ListRepositories(ctx, caller.Organization, query.Pagination{Limit: in.Limit, Offset: in.Offset, Search: search}.Normalize())
	if err != nil {
		return ListRepositoriesOut{}, err
	}
	out := ListRepositoriesOut{Items: make([]Repository, 0, len(page.Items)), Total: page.Page.Total, Limit: page.Page.Limit, Offset: page.Page.Offset}
	for _, r := range page.Items {
		out.Items = append(out.Items, Repository{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private})
	}
	if next := out.Offset + len(page.Items); len(page.Items) > 0 && next < out.Total {
		out.NextOffset = &next
	}
	switch {
	case page.Page.Total == 0 && search == "":
		if err := t.noInstallations(ctx, caller.Organization); err != nil {
			return ListRepositoriesOut{}, err
		}
		out.NextStep = "La GitHub App está instalada pero no da acceso a ningún repositorio: pide a quien la administra en GitHub que agregue repositorios."
	case page.Page.Total == 0:
		out.NextStep = "Ningún repositorio coincide con la búsqueda: prueba con otro texto o sin search."
	default:
		out.NextStep = "Conecta uno a una app con connect_repository (app, repository=<full_name>)."
	}
	return out, nil
}

// noInstallations is nil when the organization has an installation, else an
// error that sends the user to the console to install the GitHub App.
func (t tools) noInstallations(ctx context.Context, org identity.OrganizationID) error {
	insts, err := t.queries.ListInstallations(ctx, org)
	if err != nil {
		return err
	}
	if len(insts) > 0 {
		return nil
	}
	return mcpx.NextStep(errx.NotFound("no GitHub account is connected to this organization").WithCode("GITHUB_NOT_CONNECTED"),
		"Aún no hay ninguna cuenta de GitHub conectada: pide al usuario que instale la GitHub App en la consola (necesita un navegador): "+t.settingsURL())
}

// ConnectRepositoryIn is connect_repository's input.
type ConnectRepositoryIn struct {
	App        string            `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Repository string            `json:"repository" jsonschema:"owner/nombre del repositorio en GitHub, o su id numérico (list_repositories)"`
	Branch     string            `json:"branch,omitempty" jsonschema:"rama que publica la app (por defecto la principal del repositorio)"`
	Root       string            `json:"root,omitempty" jsonschema:"carpeta de la app dentro del repositorio (vacía si es la raíz)"`
	Port       int               `json:"port,omitempty" jsonschema:"puerto en el que escucha la app (por defecto 8080; también llega como $PORT)"`
	HealthPath string            `json:"health_path,omitempty" jsonschema:"ruta que responde 200 cuando la app está sana (por defecto /)"`
	Replicas   int               `json:"replicas,omitempty" jsonschema:"cuántas copias correr, de 1 a 10 (por defecto 1)"`
	Env        map[string]string `json:"env,omitempty" jsonschema:"variables de entorno; SOLO valores que no son secretos (no pongas contraseñas ni llaves)"`
}

// ConnectRepositoryOut is connect_repository's result.
type ConnectRepositoryOut struct {
	App          string `json:"app" jsonschema:"slug de la app"`
	Repository   string `json:"repository" jsonschema:"owner/nombre"`
	Branch       string `json:"branch"`
	Root         string `json:"root,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"la publicación del último commit, si ya empezó"`
	NextStep     string `json:"next_step" jsonschema:"qué hacer ahora"`
}

// repoPages bounds how many pages of repositories a lookup reads.
const repoPages = 50

// findRepository finds the repository named "owner/name" (case-insensitive)
// or by GitHub's numeric id among what the organization reaches.
func (t tools) findRepository(ctx context.Context, org identity.OrganizationID, ref string) (source.Repo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return source.Repo{}, errx.InvalidField("repository", "repository is required: owner/name or the numeric id from list_repositories")
	}
	byID, _ := strconv.ParseInt(ref, 10, 64)
	search := ref
	if byID > 0 {
		search = "" // the id is not in the name: look through them all
	}
	for page := 0; page < repoPages; page++ {
		res, err := t.queries.ListRepositories(ctx, org, query.Pagination{Limit: query.MaxLimit, Offset: page * query.MaxLimit, Search: search}.Normalize())
		if err != nil {
			return source.Repo{}, err
		}
		for _, r := range res.Items {
			if strings.EqualFold(r.FullName, ref) || (byID > 0 && r.ID == byID) {
				return r, nil
			}
		}
		if len(res.Items) == 0 || (page+1)*query.MaxLimit >= res.Page.Total {
			break
		}
	}
	if err := t.noInstallations(ctx, org); err != nil {
		return source.Repo{}, err
	}
	return source.Repo{}, mcpx.NextStep(errx.NotFound("repository not found").WithField("repository"),
		"Ese repositorio no está entre los que alcanza Odicea: usa list_repositories para ver los disponibles; si falta, pide a quien administra la GitHub App que lo agregue: "+t.settingsURL())
}

func (t tools) connectRepository(ctx context.Context, caller mcpx.Principal, in ConnectRepositoryIn) (ConnectRepositoryOut, error) {
	if err := t.enabled(ctx); err != nil {
		return ConnectRepositoryOut{}, err
	}
	p, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return ConnectRepositoryOut{}, err
	}
	repo, err := t.findRepository(ctx, caller.Organization, in.Repository)
	if err != nil {
		return ConnectRepositoryOut{}, err
	}
	before, err := t.newestPush(ctx, caller.Organization, p.ID)
	if err != nil {
		return ConnectRepositoryOut{}, err
	}
	conn, err := t.commands.Connect(ctx, caller.Organization, p.ID, source.Connect{
		Installation: repo.Installation, RepositoryID: repo.ID, Branch: in.Branch, Root: in.Root,
		Config:      release.Config{Port: in.Port, HealthPath: in.HealthPath, Replicas: in.Replicas, Env: in.Env},
		ConnectedBy: caller.User,
	})
	if err != nil {
		return ConnectRepositoryOut{}, err
	}
	out := ConnectRepositoryOut{App: p.Slug, Repository: conn.Repository, Branch: conn.Branch, Root: conn.Root}
	push, err := t.awaitPush(ctx, caller.Organization, p.ID, source.Push{}, before)
	if err != nil {
		return ConnectRepositoryOut{}, err
	}
	out.DeploymentID, out.NextStep = t.pushStep(p, push)
	return out, nil
}

// PublishNowIn is publish_now's input.
type PublishNowIn struct {
	App     string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Rebuild bool   `json:"rebuild,omitempty" jsonschema:"true para construir de nuevo todas las partes aunque su código no haya cambiado (imagen base nueva, un arreglo fuera del código); por defecto se reutiliza lo ya construido"`
}

// PublishNowOut is publish_now's result.
type PublishNowOut struct {
	App          string `json:"app" jsonschema:"slug de la app"`
	Repository   string `json:"repository,omitempty" jsonschema:"owner/nombre"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit,omitempty" jsonschema:"SHA del commit que se publica"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"la publicación, si ya empezó"`
	NextStep     string `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) publishNow(ctx context.Context, caller mcpx.Principal, in PublishNowIn) (PublishNowOut, error) {
	if err := t.enabled(ctx); err != nil {
		return PublishNowOut{}, err
	}
	p, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return PublishNowOut{}, err
	}
	push, err := t.commands.Publish(ctx, caller.Organization, p.ID, source.Publish{Rebuild: in.Rebuild, RequestedBy: caller.User})
	if err != nil {
		var e *errx.Error
		if errx.IsType(err, errx.TypeNotFound) && (!errx.As(err, &e) || e.Code != source.CodeNoAccess) {
			return PublishNowOut{}, mcpx.NextStep(errx.NotFound("this app has no connected repository"),
				"Esta app no tiene un repositorio conectado: usa connect_repository (app="+p.Slug+") o publica una carpeta con deploy_folder.")
		}
		return PublishNowOut{}, err
	}
	out := PublishNowOut{App: p.Slug, Repository: push.Git.Repository, Branch: push.Git.Branch, Commit: push.Git.Commit}
	if push, err = t.awaitPush(ctx, caller.Organization, p.ID, push, source.PushID{}); err != nil {
		return PublishNowOut{}, err
	}
	out.DeploymentID, out.NextStep = t.pushStep(p, push)
	return out, nil
}

// newestPush is the app's newest push (zero when none).
func (t tools) newestPush(ctx context.Context, org identity.OrganizationID, app project.ID) (source.PushID, error) {
	page, err := t.queries.ListPushes(ctx, org, app, source.PushFilter{}, query.Pagination{Limit: 1}.Normalize())
	if err != nil || len(page.Items) == 0 {
		return source.PushID{}, err
	}
	return page.Items[0].ID, nil
}

// awaitPush waits up to the timeout for a push to become a deployment or end
// without one, and returns it as last seen. The push is want when given;
// otherwise the app's newest, once it is not the one that was newest before
// (skip): connecting queues a new push. It returns the zero push if none
// showed up in time.
func (t tools) awaitPush(ctx context.Context, org identity.OrganizationID, app project.ID, want source.Push, skip source.PushID) (source.Push, error) {
	timeout := time.NewTimer(t.opts.WaitTimeout)
	defer timeout.Stop()
	tick := time.NewTicker(t.opts.PollInterval)
	defer tick.Stop()
	last := want
	for {
		page, err := t.queries.ListPushes(ctx, org, app, source.PushFilter{}, query.Pagination{Limit: 10}.Normalize())
		if err != nil {
			return last, err
		}
		for _, p := range page.Items { // newest first
			if want.ID.IsZero() && p.ID != skip {
				last = p
				break
			}
			if p.ID == want.ID {
				last = p
				break
			}
		}
		if last.Deployment != nil || (!last.ID.IsZero() && last.Status.Finished()) {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-timeout.C:
			return last, nil
		case <-tick.C:
		}
	}
}

// pushStep is the deployment a push became (empty when none yet) and what the
// agent does next.
func (t tools) pushStep(p project.Project, push source.Push) (string, string) {
	switch {
	case push.Deployment != nil:
		id := push.Deployment.String()
		return id, "Se está publicando: llama a get_deployment (app=" + p.Slug + ", deployment_id=" + id + ") con wait_seconds=30 hasta que done sea true."
	case push.Status == source.StatusFailed:
		msg := "No se pudo empezar la publicación"
		if push.Error != nil {
			msg = push.Error.Message
			if push.Error.Detail != "" {
				msg += " (" + push.Error.Detail + ")"
			}
		}
		return "", msg + ". Corrige y usa publish_now (app=" + p.Slug + ") para reintentar."
	case push.Status == source.StatusSkipped:
		return "", "Un commit más nuevo reemplazó a este: llama a get_deployment (app=" + p.Slug + ") para ver la publicación vigente."
	default:
		return "", "La publicación empezará en unos segundos: llama a get_deployment (app=" + p.Slug + ") con wait_seconds=30."
	}
}
