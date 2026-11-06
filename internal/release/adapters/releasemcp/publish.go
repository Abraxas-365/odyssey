package releasemcp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Limits of the publishing tools.
const (
	maxWaitSeconds = 45
	defaultLogs    = 200
	maxLogs        = build.MaxLogPage
	// maxTailPages bounds how many log pages get_build_logs reads to find
	// the tail of a long log.
	maxTailPages = 20
	// rollbackScanPages bounds how many pages of ready deployments rollback
	// scans for a target.
	rollbackScanPages = 10
)

// ConfigIn is how a version runs; all optional (port 8080, health "/", one
// replica by default).
type ConfigIn struct {
	Port       int               `json:"port,omitempty" jsonschema:"puerto en el que escucha la app (por defecto 8080; también llega como $PORT)"`
	HealthPath string            `json:"health_path,omitempty" jsonschema:"ruta que responde 200 cuando la app está sana (por defecto /)"`
	Replicas   int               `json:"replicas,omitempty" jsonschema:"cuántas copias correr, de 1 a 10 (por defecto 1)"`
	Env        map[string]string `json:"env,omitempty" jsonschema:"variables de entorno; SOLO valores que no son secretos (no pongas contraseñas ni llaves)"`
}

func (c ConfigIn) config() release.Config {
	return release.Config{Port: c.Port, HealthPath: c.HealthPath, Replicas: c.Replicas, Env: c.Env}
}

// DeployFolderIn is deploy_folder's input.
type DeployFolderIn struct {
	App     string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Message string `json:"message,omitempty" jsonschema:"qué cambió en esta versión, en una frase corta en español para el usuario (por ejemplo: Agrega el filtro por vendedor); hasta 200 caracteres. Es el nombre de la versión en la consola"`
	Name    string `json:"name,omitempty" jsonschema:"nombre de la carpeta subida (opcional; solo se muestra en modo técnico)"`
	ConfigIn
}

// DeployFolderOut is deploy_folder's result.
type DeployFolderOut struct {
	App       string    `json:"app" jsonschema:"slug de la app"`
	UploadURL string    `json:"upload_url" jsonschema:"enlace de subida de un solo uso; no lo muestres ni lo guardes"`
	ExpiresAt time.Time `json:"expires_at" jsonschema:"cuándo vence el enlace (10 minutos)"`
	Command   string    `json:"command" jsonschema:"comando para ejecutar tú mismo en una shell, dentro de la carpeta de la app"`
	NextStep  string    `json:"next_step" jsonschema:"qué hacer ahora"`
}

// uploadCommand is the shell command that streams the working folder to
// url: tracked and untracked-but-not-ignored files in a git repository, else
// everything but .git, node_modules and .env*. COPYFILE_DISABLE keeps
// macOS's tar from adding AppleDouble files. tar writes a plain tar and gzip
// compresses it: bsdtar's own -z pads a piped .tar.gz with zeros to its
// 10 KiB block, data after the gzip stream that the source check refuses.
func uploadCommand(url string) string {
	return "if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then " +
		"git ls-files -z -co --exclude-standard | COPYFILE_DISABLE=1 tar -cf - --null -T -; " +
		"else COPYFILE_DISABLE=1 tar -cf - --exclude=.git --exclude=node_modules --exclude='.env*' .; fi | gzip -c | " +
		"curl --fail-with-body -sS -H 'Content-Type: application/gzip' -T - '" + url + "'"
}

func (t tools) deployFolder(ctx context.Context, caller mcpx.Principal, in DeployFolderIn) (DeployFolderOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return DeployFolderOut{}, err
	}
	if t.opts.APIURL == "" {
		return DeployFolderOut{}, errx.Internal("the API's public URL is not configured: uploads have no address")
	}
	ticket, err := t.commands.IssueUpload(ctx, caller.Organization, p.ID, env.ID,
		release.IssueUpload{Name: in.Name, Message: in.Message, Config: in.config(), RequestedBy: caller.User})
	if err != nil {
		return DeployFolderOut{}, err
	}
	url := t.opts.APIURL + "/uploads/" + ticket.Token
	return DeployFolderOut{
		App:       p.Slug,
		UploadURL: url,
		ExpiresAt: ticket.ExpiresAt,
		Command:   uploadCommand(url),
		NextStep: "Ejecuta `command` tú mismo en una shell, dentro de la carpeta de la app (sirve una sola vez y vence en 10 minutos); " +
			"luego llama a get_deployment (app=" + p.Slug + ") con wait_seconds=30 hasta que done sea true.",
	}, nil
}

// DeployImageIn is deploy_image's input.
type DeployImageIn struct {
	App     string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Image   string `json:"image" jsonschema:"imagen fijada por digest: registro/repositorio@sha256:<64 hex>"`
	Message string `json:"message,omitempty" jsonschema:"qué cambió en esta versión, en una frase corta en español para el usuario; hasta 200 caracteres. Es el nombre de la versión en la consola"`
	ConfigIn
}

// DeployOut is the result of a tool that starts a deployment: the version it
// created and what to do next.
type DeployOut struct {
	App string `json:"app" jsonschema:"slug de la app"`
	Version
	NextStep string `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) deployImage(ctx context.Context, caller mcpx.Principal, in DeployImageIn) (DeployOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return DeployOut{}, err
	}
	d, err := t.commands.Deploy(ctx, caller.Organization, p.ID, env.ID,
		release.Deploy{Image: in.Image, Message: in.Message, Config: in.config(), RequestedBy: caller.User})
	if err != nil {
		return DeployOut{}, err
	}
	return DeployOut{App: p.Slug, Version: version(d, nil), NextStep: followStep(p, d)}, nil
}

func followStep(p project.Project, d release.Deployment) string {
	return "Llama a get_deployment (app=" + p.Slug + ", deployment_id=" + d.ID.String() + ") con wait_seconds=30 hasta que done sea true."
}

// current resolves the deployment a tool talks about when the agent names
// none: the one in progress, else the one online, else the newest.
func (t tools) current(ctx context.Context, caller mcpx.Principal, p project.Project, env project.Environment, id string) (release.Deployment, error) {
	id = strings.TrimSpace(id)
	if id != "" {
		did, err := release.ParseDeploymentID(id)
		if err != nil {
			return release.Deployment{}, errx.InvalidField("deployment_id", "deployment_id must be an id from get_deployment or list_versions")
		}
		d, err := t.queries.FindDeployment(ctx, caller.Organization, p.ID, env.ID, did)
		if errx.IsType(err, errx.TypeNotFound) {
			return release.Deployment{}, mcpx.NextStep(errx.NotFound("deployment not found").WithField("deployment_id"),
				"Usa list_versions (app="+p.Slug+") para ver los deployment_id de esta app.")
		}
		return d, err
	}
	cur, err := t.queries.Current(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return release.Deployment{}, err
	}
	if cur.Active != nil {
		return *cur.Active, nil
	}
	if cur.Deployment != nil {
		return *cur.Deployment, nil
	}
	page, err := t.queries.ListDeployments(ctx, caller.Organization, p.ID, env.ID, release.DeploymentFilter{}, query.Pagination{Limit: 1}.Normalize())
	if err != nil {
		return release.Deployment{}, err
	}
	if len(page.Items) == 0 {
		return release.Deployment{}, mcpx.NextStep(errx.NotFound("this app has no publications yet"),
			"Aún no se ha publicado nada: usa deploy_folder (app="+p.Slug+") para subir la carpeta.")
	}
	return page.Items[0], nil
}

// GetDeploymentIn is get_deployment's input.
type GetDeploymentIn struct {
	App          string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"publicación a mirar; si falta, la que está en curso o la que está en línea"`
	WaitSeconds  int    `json:"wait_seconds,omitempty" jsonschema:"esperar hasta tantos segundos (0 a 45) a que la publicación termine antes de responder"`
}

// Step is one step of a deployment's progress.
type Step struct {
	Name   string `json:"name" jsonschema:"build, apply, wait_ready o promote"`
	Status string `json:"status" jsonschema:"pending, running, done, failed o skipped"`
}

// GetDeploymentOut is get_deployment's result.
type GetDeploymentOut struct {
	App          string     `json:"app" jsonschema:"slug de la app"`
	DeploymentID string     `json:"deployment_id"`
	Status       string     `json:"status" jsonschema:"building, pending, applying, verifying, ready, failed o cancelled"`
	Step         string     `json:"step,omitempty" jsonschema:"paso en el que está (o en el que falló)"`
	Steps        []Step     `json:"steps" jsonschema:"los pasos de la publicación y cómo va cada uno"`
	Reason       string     `json:"reason" jsonschema:"deploy, rollback o auto_rollback"`
	Done         bool       `json:"done" jsonschema:"true cuando ya terminó (en línea, falló o fue reemplazada); si es false vuelve a llamar con wait_seconds"`
	URL          string     `json:"url,omitempty" jsonschema:"dirección pública de la app, cuando esta versión está en línea"`
	Image        string     `json:"image,omitempty" jsonschema:"imagen corta; vacía mientras se construye"`
	Repository   string     `json:"repository,omitempty" jsonschema:"repo de GitHub de donde vino (owner/nombre)"`
	Commit       string     `json:"commit,omitempty" jsonschema:"SHA completo del commit si vino de GitHub"`
	Failure      *Failure   `json:"failure,omitempty" jsonschema:"por qué no quedó en línea"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ConsoleURL   string     `json:"console_url" jsonschema:"enlace a la app en la consola"`
	NextStep     string     `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) getDeployment(ctx context.Context, caller mcpx.Principal, in GetDeploymentIn) (GetDeploymentOut, error) {
	if in.WaitSeconds < 0 || in.WaitSeconds > maxWaitSeconds {
		return GetDeploymentOut{}, errx.InvalidField("wait_seconds", "wait_seconds must be between 0 and 45")
	}
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return GetDeploymentOut{}, err
	}
	d, err := t.current(ctx, caller, p, env, in.DeploymentID)
	if err != nil {
		return GetDeploymentOut{}, err
	}
	if in.WaitSeconds > 0 && !d.Status.Finished() {
		if d, err = t.wait(ctx, caller, p, env, d, time.Duration(in.WaitSeconds)*time.Second); err != nil {
			return GetDeploymentOut{}, err
		}
	}
	cur, err := t.queries.Current(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return GetDeploymentOut{}, err
	}
	return t.deploymentOut(p, d, cur), nil
}

// wait re-reads d every poll interval until it finishes, the wait runs out
// (it then returns d as last seen) or ctx ends.
func (t tools) wait(ctx context.Context, caller mcpx.Principal, p project.Project, env project.Environment, d release.Deployment, max time.Duration) (release.Deployment, error) {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	tick := time.NewTicker(t.opts.PollInterval)
	defer tick.Stop()
	for !d.Status.Finished() {
		select {
		case <-ctx.Done():
			return d, nil
		case <-deadline.C:
			return d, nil
		case <-tick.C:
			next, err := t.queries.FindDeployment(ctx, caller.Organization, p.ID, env.ID, d.ID)
			if err != nil {
				return d, err
			}
			d = next
		}
	}
	return d, nil
}

func (t tools) deploymentOut(p project.Project, d release.Deployment, cur release.Current) GetDeploymentOut {
	v := version(d, cur.Deployment)
	out := GetDeploymentOut{
		App: p.Slug, DeploymentID: v.DeploymentID, Status: v.Status, Step: v.Step, Steps: make([]Step, 0, len(d.Steps)), Reason: v.Reason,
		Done: d.Status.Finished(), Image: v.Image, Repository: v.Repository, Commit: v.Commit, Failure: v.Error,
		CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt, ConsoleURL: t.opts.ConsoleURL + "/apps/" + p.ID.String(),
	}
	for _, s := range d.Steps {
		out.Steps = append(out.Steps, Step{Name: s.Name, Status: string(s.Status)})
	}
	if d.Status == release.StatusReady && cur.Deployment != nil && cur.Deployment.ID == d.ID {
		out.URL = cur.URL
	}
	logs := "Usa get_build_logs (app=" + p.Slug + ", deployment_id=" + out.DeploymentID + ") para ver el log del build."
	switch {
	case d.Status == release.StatusReady && out.URL != "":
		out.NextStep = "Está en línea: comparte con el usuario la dirección " + out.URL + "."
	case d.Status == release.StatusReady:
		out.NextStep = "Esta versión terminó bien pero ya no es la que está en línea: usa get_app para ver la actual."
	case d.Status == release.StatusFailed && d.Build != nil && d.Step == release.StepBuild:
		out.NextStep = "El build falló. " + logs + " Corrige el código y vuelve a publicar con deploy_folder."
	case d.Status == release.StatusFailed:
		out.NextStep = "No quedó en línea: lee failure.message. Corrige y vuelve a publicar con deploy_folder, o usa list_versions y rollback."
	case d.Status == release.StatusCancelled:
		out.NextStep = "Otra publicación la reemplazó: llama a get_deployment sin deployment_id para ver la vigente."
	case d.Status == release.StatusWaitingApproval:
		out.NextStep = "Esta publicación espera la aprobación de TI (antes de construirse): dile al usuario que quedó pendiente y que le avisaremos. " +
			"No la repitas; get_request o get_deployment muestran cómo va."
	case d.Status == release.StatusBuilding:
		out.NextStep = "Se está construyendo: vuelve a llamar a get_deployment con wait_seconds=45 hasta que done sea true. Si quieres ver el avance, " + logs
	default:
		out.NextStep = "Vuelve a llamar a get_deployment con wait_seconds=45 hasta que done sea true."
	}
	return out
}

// GetBuildLogsIn is get_build_logs's input.
type GetBuildLogsIn struct {
	App          string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"publicación cuyo build leer; si falta, la misma que mostraría get_deployment"`
	After        *int64 `json:"after,omitempty" jsonschema:"seguir leyendo después de este cursor (el next de la respuesta anterior); si falta, devuelve las últimas líneas"`
	Limit        int    `json:"limit,omitempty" jsonschema:"cuántas líneas devolver (por defecto 200, máximo 1000)"`
}

// GetBuildLogsOut is get_build_logs's result.
type GetBuildLogsOut struct {
	App         string   `json:"app" jsonschema:"slug de la app"`
	BuildID     string   `json:"build_id"`
	BuildStatus string   `json:"build_status" jsonschema:"queued, running, succeeded, failed o cancelled"`
	Log         string   `json:"log" jsonschema:"las líneas del log, una por renglón"`
	Next        int64    `json:"next" jsonschema:"cursor para pasar como after y leer lo que sigue"`
	Finished    bool     `json:"finished" jsonschema:"true si el build terminó y el log llega a su final"`
	Truncated   bool     `json:"truncated" jsonschema:"true si el log era tan largo que no se leyó completo: lo que falta está después de next"`
	Failure     *Failure `json:"failure,omitempty" jsonschema:"por qué falló el build"`
}

func (t tools) getBuildLogs(ctx context.Context, caller mcpx.Principal, in GetBuildLogsIn) (GetBuildLogsOut, error) {
	if in.Limit < 0 || (in.After != nil && *in.After < 0) {
		return GetBuildLogsOut{}, errx.InvalidField("limit", "limit and after cannot be negative")
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultLogs
	}
	limit = min(limit, maxLogs)
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return GetBuildLogsOut{}, err
	}
	d, err := t.current(ctx, caller, p, env, in.DeploymentID)
	if err != nil {
		return GetBuildLogsOut{}, err
	}
	if d.Build == nil {
		return GetBuildLogsOut{}, mcpx.NextStep(errx.Business("this version was published from an image: it has no build"),
			"Esta versión se publicó desde una imagen y no tiene build: get_deployment muestra su estado.")
	}
	b, err := t.builds.Find(ctx, caller.Organization, *d.Build)
	if err != nil {
		return GetBuildLogsOut{}, err
	}
	if b.Project != p.ID {
		return GetBuildLogsOut{}, errx.NotFound("build not found")
	}
	out := GetBuildLogsOut{App: p.Slug, BuildID: b.ID.String(), BuildStatus: string(b.Status)}
	if b.Error != nil {
		out.Failure = &Failure{Code: b.Error.Code, Message: b.Error.Message, Detail: b.Error.Detail}
	}
	var lines []string
	if in.After != nil {
		page, err := t.builds.Logs(ctx, caller.Organization, b.ID, build.LogQuery{After: *in.After, Limit: limit})
		if err != nil {
			return GetBuildLogsOut{}, err
		}
		for _, l := range page.Lines {
			lines = append(lines, l.Text)
		}
		out.Next, out.Finished = page.Next, page.Finished
	} else {
		tail := newRing(limit)
		var after int64
		for pages := 0; ; pages++ {
			if pages == maxTailPages {
				out.Truncated = true
				break
			}
			page, err := t.builds.Logs(ctx, caller.Organization, b.ID, build.LogQuery{After: after, Limit: maxLogs})
			if err != nil {
				return GetBuildLogsOut{}, err
			}
			for _, l := range page.Lines {
				tail.add(l.Text)
			}
			if page.Next > after {
				after = page.Next
			}
			out.Finished = page.Finished
			if page.Finished || len(page.Lines) == 0 {
				break
			}
		}
		lines, out.Next = tail.lines(), after
	}
	out.Log = strings.Join(lines, "\n")
	return out, nil
}

// ring keeps the last n lines added.
type ring struct {
	buf   []string
	start int
	n     int
}

func newRing(n int) *ring { return &ring{buf: make([]string, n)} }

func (r *ring) add(s string) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = s
		r.n++
		return
	}
	r.buf[r.start] = s
	r.start = (r.start + 1) % len(r.buf)
}

func (r *ring) lines() []string {
	out := make([]string, 0, r.n)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.start+i)%len(r.buf)])
	}
	return out
}

// RollbackIn is rollback's input.
type RollbackIn struct {
	App          string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"versión a la que volver (list_versions); si falta, la última que estuvo en línea antes de la actual"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"true si el usuario ya confirmó volver atrás; en una app de producción se le pregunta antes"`
}

func (t tools) rollback(ctx context.Context, caller mcpx.Principal, in RollbackIn) (DeployOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return DeployOut{}, err
	}
	// Going back in a production app replaces what the company is using now:
	// the user says yes first (the other tiers go straight through).
	if p.Tier == project.TierProduction {
		if err := mcpx.Confirm(ctx, in.Confirm, "«"+p.Name+"» es una app de producción: volver a una versión anterior reemplaza la que usa la empresa ahora. ¿Quieres seguir?"); err != nil {
			return DeployOut{}, err
		}
	}
	var target release.DeploymentID
	if id := strings.TrimSpace(in.DeploymentID); id != "" {
		if target, err = release.ParseDeploymentID(id); err != nil {
			return DeployOut{}, errx.InvalidField("deployment_id", "deployment_id must be an id from list_versions")
		}
	} else if target, err = t.previous(ctx, caller, p, env); err != nil {
		return DeployOut{}, err
	}
	d, err := t.commands.Rollback(ctx, caller.Organization, p.ID, env.ID, target, release.Rollback{RequestedBy: caller.User})
	if err != nil {
		var e *errx.Error
		if errors.As(err, &e) {
			switch e.Code {
			case release.CodeAlreadyCurrent, release.CodeNotRollbackable:
				return DeployOut{}, mcpx.NextStep(e, "Usa list_versions (app="+p.Slug+") para elegir una versión que estuvo en línea y no es la actual.")
			}
			if e.Type == errx.TypeNotFound {
				return DeployOut{}, mcpx.NextStep(e, "Usa list_versions (app="+p.Slug+") para ver los deployment_id de esta app.")
			}
		}
		return DeployOut{}, err
	}
	return DeployOut{App: p.Slug, Version: version(d, nil), NextStep: followStep(p, d)}, nil
}

// previous is the newest ready deployment whose release is not the one
// serving: the version to go back to.
func (t tools) previous(ctx context.Context, caller mcpx.Principal, p project.Project, env project.Environment) (release.DeploymentID, error) {
	cur, err := t.queries.Current(ctx, caller.Organization, p.ID, env.ID)
	if err != nil {
		return release.DeploymentID{}, err
	}
	var serving *release.ID
	switch {
	case cur.Release != nil:
		serving = &cur.Release.ID
	case cur.Deployment != nil:
		serving = cur.Deployment.Release
	}
	filter := release.DeploymentFilter{Status: []release.Status{release.StatusReady}}
	for page := 0; page < rollbackScanPages; page++ {
		items, err := t.queries.ListDeployments(ctx, caller.Organization, p.ID, env.ID, filter,
			query.Pagination{Limit: query.MaxLimit, Offset: page * query.MaxLimit}.Normalize())
		if err != nil {
			return release.DeploymentID{}, err
		}
		for _, d := range items.Items {
			if d.Release == nil || (serving != nil && *d.Release == *serving) {
				continue
			}
			return d.ID, nil
		}
		if (page+1)*query.MaxLimit >= items.Page.Total || len(items.Items) == 0 {
			break
		}
	}
	return release.DeploymentID{}, mcpx.NextStep(errx.NotFound("there is no earlier version to go back to"),
		"No hay una versión anterior que haya estado en línea: usa list_versions (app="+p.Slug+") para ver el historial o publica una nueva con deploy_folder.")
}
