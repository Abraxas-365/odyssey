package releasemcp

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// maxBuildsShown bounds the builds a deployment reports (an app has at most
// project.MaxServices services, one build each).
const maxBuildsShown = project.MaxServices

// ServiceOut is one service of an app: how it is configured and how the
// version that is online runs it.
type ServiceOut struct {
	Name          string `json:"name" jsonschema:"nombre de la parte de la app"`
	Kind          string `json:"kind" jsonschema:"web (atiende peticiones) o worker (corre en segundo plano, sin dirección)"`
	Route         string `json:"route" jsonschema:"ruta de la dirección de la app que atiende (/ es toda; /api todo lo que cuelga de /api); vacía si es interna o un worker"`
	Root          string `json:"root" jsonschema:"carpeta de la parte dentro del código de la app; vacía si es todo el código"`
	StripPrefix   bool   `json:"strip_prefix" jsonschema:"true si la parte recibe la ruta sin su prefijo (ve /users para /api/users)"`
	State         string `json:"state" jsonschema:"online (responde), starting (arrancando), failed (no arranca o no responde), unknown (no se pudo consultar ahora) o not_published (la versión en línea no la incluye o aún no hay versión en línea)"`
	ReadyReplicas int    `json:"ready_replicas" jsonschema:"copias que responden ahora"`
	Replicas      int    `json:"replicas" jsonschema:"copias que debería haber"`
	Message       string `json:"message,omitempty" jsonschema:"lo que dijo el clúster de esta parte, por ejemplo por qué falló"`
}

// FolderService is a part of the app a folder upload has to contain.
type FolderService struct {
	Name string `json:"name" jsonschema:"nombre de la parte"`
	Root string `json:"root" jsonschema:"carpeta de la parte, relativa a la carpeta que se sube; vacía si es todo el código"`
}

// BuildOut is the build of one service of a deployment from source.
type BuildOut struct {
	Service string   `json:"service" jsonschema:"parte de la app que construye"`
	BuildID string   `json:"build_id"`
	Status  string   `json:"status" jsonschema:"queued, running, succeeded, failed, cancelled o unknown (no se pudo leer el build)"`
	Reused  bool     `json:"reused" jsonschema:"true si reutilizó la imagen de un build anterior del mismo código en vez de construir"`
	Failure *Failure `json:"failure,omitempty" jsonschema:"por qué falló este build"`
}

// servicesOf joins the app's configured services with what is online. A
// failing state read leaves every state unknown (it never fails the caller).
func (t tools) servicesOf(ctx context.Context, org identity.OrganizationID, p project.Project, env project.Environment, configured []project.Service) []ServiceOut {
	states, err := t.queries.ServiceStates(ctx, org, p.ID, env.ID)
	if err != nil {
		slog.Warn("get_app: service states unavailable", "app", p.ID, "error", err)
	}
	out := make([]ServiceOut, 0, len(configured))
	for _, s := range configured {
		o := ServiceOut{Name: s.Name, Kind: string(s.Kind), Route: s.Route, Root: s.Root, StripPrefix: s.StripPrefix, State: "not_published"}
		if err != nil {
			o.State = "unknown"
		} else if i := slices.IndexFunc(states, func(st release.ServiceState) bool { return st.Name == s.Name }); i >= 0 {
			st := states[i]
			o.State, o.ReadyReplicas, o.Replicas, o.Message = serviceState(st.Phase), st.ReadyReplicas, st.Replicas, st.Message
		}
		out = append(out, o)
	}
	return out
}

func serviceState(p release.Phase) string {
	switch p {
	case release.PhaseReady:
		return "online"
	case release.PhaseProgressing:
		return "starting"
	case release.PhaseFailed:
		return "failed"
	}
	return "unknown"
}

// anyFailed reports whether a service is failing.
func anyFailed(services []ServiceOut) bool {
	return slices.ContainsFunc(services, func(s ServiceOut) bool { return s.State == "failed" })
}

// folderRoots lists where the parts of a multi-service app live, for the
// agent to know what folder to upload.
func folderRoots(services []project.Service) ([]FolderService, string) {
	out := make([]FolderService, 0, len(services))
	var roots []string
	for _, s := range services {
		out = append(out, FolderService{Name: s.Name, Root: s.Root})
		r := s.Root
		if r == "" {
			r = "."
		}
		if !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	return out, strings.Join(roots, ", ")
}

// buildsOut reads the builds of a deployment (at most maxBuildsShown). A
// build that cannot be read is still listed, with status unknown.
func (t tools) buildsOut(ctx context.Context, org identity.OrganizationID, p project.Project, d release.Deployment) []BuildOut {
	out := make([]BuildOut, 0, min(len(d.Builds), maxBuildsShown))
	for _, sb := range d.Builds[:min(len(d.Builds), maxBuildsShown)] {
		o := BuildOut{Service: sb.Name, BuildID: sb.Build.String(), Status: "unknown"}
		b, err := t.builds.Find(ctx, org, sb.Build)
		switch {
		case err != nil:
			if !errx.IsType(err, errx.TypeNotFound) {
				slog.Warn("get_deployment: build unavailable", "app", p.ID, "build", sb.Build, "error", err)
			}
		case b.Project != p.ID:
			// another app's build: not ours to describe
		default:
			o.Status, o.Reused = string(b.Status), b.ReusedFrom != nil
			if b.Error != nil {
				o.Failure = &Failure{Code: b.Error.Code, Message: b.Error.Message, Detail: b.Error.Detail}
			}
		}
		out = append(out, o)
	}
	return out
}

// failedServices are the services whose build failed.
func failedServices(builds []BuildOut) []string {
	var names []string
	for _, b := range builds {
		if b.Status == string(build.StatusFailed) {
			names = append(names, b.Service)
		}
	}
	return names
}

// logsBuild picks the build get_build_logs reads: the one of the named
// service, else (no name) the first that failed, else the first. A deployment
// without a service list (made before services) has only its Build.
func (t tools) logsBuild(ctx context.Context, org identity.OrganizationID, d release.Deployment, service string) (string, build.Build, error) {
	service = strings.TrimSpace(service)
	if len(d.Builds) == 0 {
		if d.Build == nil {
			return "", build.Build{}, mcpx.NextStep(errx.Business("this version was published from an image: it has no build"),
				"Esta versión se publicó desde una imagen y no tiene build: get_deployment muestra su estado.")
		}
		b, err := t.builds.Find(ctx, org, *d.Build)
		return "", b, err
	}
	if service != "" {
		for _, sb := range d.Builds {
			if sb.Name == service {
				b, err := t.builds.Find(ctx, org, sb.Build)
				return sb.Name, b, err
			}
		}
		names := make([]string, len(d.Builds))
		for i, sb := range d.Builds {
			names[i] = sb.Name
		}
		return "", build.Build{}, mcpx.NextStep(errx.InvalidField("service", "this version has no service named "+service),
			"Las partes de esta versión son: "+strings.Join(names, ", ")+". Vuelve a llamar a get_build_logs con service igual a una de ellas.")
	}
	// No name: the first build that failed, else the first.
	pick := d.Builds[0]
	for _, sb := range d.Builds[:min(len(d.Builds), maxBuildsShown)] {
		if b, err := t.builds.Find(ctx, org, sb.Build); err == nil && b.Status == build.StatusFailed {
			pick = sb
			break
		}
	}
	b, err := t.builds.Find(ctx, org, pick.Build)
	return pick.Name, b, err
}
