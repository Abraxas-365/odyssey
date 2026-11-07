package projectmcp

import (
	"context"
	"errors"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// An app's parts (MS-7): list_services reads them and set_services replaces
// them through the same project Commands as PUT …/services (IT's rules,
// the cluster capability and idempotence included).

// ServiceOut is one part of an app.
type ServiceOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind" jsonschema:"web (responde en la dirección de la app) o worker (corre sin puerto)"`
	Root        string `json:"root" jsonschema:"carpeta de su código, relativa a la carpeta que se publica (vacía: toda)"`
	Route       string `json:"route" jsonschema:"ruta de la dirección que atiende (/ el sitio, /api todo lo que empieza con /api); vacía en un worker o en una parte interna"`
	StripPrefix bool   `json:"strip_prefix" jsonschema:"true si la ruta se quita antes de que le llegue la petición"`
}

func serviceOut(s project.Service) ServiceOut {
	return ServiceOut{Name: s.Name, Kind: string(s.Kind), Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix}
}

func servicesOut(services []project.Service) []ServiceOut {
	out := make([]ServiceOut, len(services))
	for i, s := range services {
		out[i] = serviceOut(s)
	}
	return out
}

// ListServicesIn is list_services's input.
type ListServicesIn struct {
	App string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
}

// ListServicesOut is list_services's result.
type ListServicesOut struct {
	App      string       `json:"app" jsonschema:"slug de la app"`
	Items    []ServiceOut `json:"items" jsonschema:"las partes, en orden"`
	Single   bool         `json:"single" jsonschema:"true si la app es una sola parte web en / con todo el código (lo normal)"`
	NextStep string       `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) listServices(ctx context.Context, caller mcpx.Principal, in ListServicesIn) (ListServicesOut, error) {
	p, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return ListServicesOut{}, err
	}
	services, err := t.queries.Services(ctx, caller.Organization, p.ID)
	if err != nil {
		return ListServicesOut{}, err
	}
	out := ListServicesOut{App: p.Slug, Items: servicesOut(services), Single: project.Simple(services)}
	if out.Single {
		out.NextStep = "La app es una sola parte con todo el código. Si el código tiene varias apps (por ejemplo un frontend y una API en carpetas distintas), " +
			"propón sus partes con detect_services y guárdalas con set_services después de confirmarlas con el usuario."
	} else {
		out.NextStep = "Publica la carpeta que contiene todas las partes (no la de una sola). Usa estos nombres en services de set_env, request_secret, " +
			"create_database o enable_ai para darle algo solo a algunas partes."
	}
	return out, nil
}

// ServiceIn is one part as asked for.
type ServiceIn struct {
	Name        string `json:"name" jsonschema:"minúsculas y números, empieza con letra, hasta 20 (por ejemplo web, api, jobs)"`
	Kind        string `json:"kind,omitempty" jsonschema:"web (por defecto) o worker (sin puerto ni ruta)"`
	Root        string `json:"root,omitempty" jsonschema:"carpeta de su código, relativa a la carpeta que se publica (por ejemplo apps/api); vacía: toda"`
	Route       string `json:"route,omitempty" jsonschema:"ruta de la dirección que atiende: / para el sitio, /api para la API; vacía para un worker o una parte interna"`
	StripPrefix bool   `json:"strip_prefix,omitempty" jsonschema:"true para quitar la ruta antes de que llegue la petición (la parte ve /users en vez de /api/users)"`
}

// SetServicesIn is set_services's input.
type SetServicesIn struct {
	App   string      `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Items []ServiceIn `json:"items" jsonschema:"la lista completa de partes, en orden (de 1 a 8)"`
}

// SetServicesOut is set_services's result.
type SetServicesOut struct {
	// Pending: IT's rules hold the change for approval (status, request_id);
	// nothing changed yet.
	mcpx.Pending
	App      string       `json:"app" jsonschema:"slug de la app"`
	Items    []ServiceOut `json:"items" jsonschema:"las partes como quedaron (o como siguen, si quedó esperando aprobación)"`
	NextStep string       `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) setServices(ctx context.Context, caller mcpx.Principal, in SetServicesIn) (SetServicesOut, error) {
	p, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return SetServicesOut{}, err
	}
	items := make([]project.ServiceSpec, len(in.Items))
	for i, s := range in.Items {
		items[i] = project.ServiceSpec{Name: s.Name, Kind: project.ServiceKind(strings.TrimSpace(s.Kind)), Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix}
	}
	change, err := t.commands.SetServices(ctx, caller.Organization, p.ID, project.SetServices{Items: items, UpdatedBy: caller.User})
	if err != nil {
		var e *errx.Error
		if errors.As(err, &e) && e.Code == project.CodeServicesUnavailable {
			return SetServicesOut{}, mcpx.NextStep(e, "Muestra el mensaje al usuario tal cual: hasta que TI actualice el agente del clúster, la app sigue siendo una sola parte. "+
				"Mientras tanto se puede publicar cada parte como una app aparte, si el usuario quiere.")
		}
		if errors.As(err, &e) && errx.IsType(err, errx.TypeValidation) && !gate.Refusal(e) {
			return SetServicesOut{}, mcpx.NextStep(e, "Corrige la lista y vuelve a llamar (list_services muestra la actual).")
		}
		return SetServicesOut{}, err
	}
	out := SetServicesOut{App: p.Slug, Items: servicesOut(change.Items)}
	if change.Held() {
		out.Pending, out.NextStep = mcpx.PendingOf(change.Pending), mcpx.PendingStep(change.Pending)
		return out, nil
	}
	out.NextStep = "Guardado. La próxima versión usa estas partes: publica la carpeta que las contiene a todas con deploy_folder " +
		"(o con publish_now si la app tiene un repositorio conectado) y sigue la publicación con get_deployment."
	return out, nil
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
		p, err = t.queries.Find(ctx, org, id)
	} else {
		p, err = t.queries.FindBySlug(ctx, org, strings.ToLower(app))
	}
	if errx.IsType(err, errx.TypeNotFound) {
		return project.Project{}, mcpx.NextStep(errx.NotFound("app not found").WithField("app"), "Usa list_apps para ver los slugs de tu organización.")
	}
	return p, err
}
