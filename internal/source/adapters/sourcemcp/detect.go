package sourcemcp

import (
	"context"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// detect_services (MS-7): the proposal of an app's parts, from a folder's
// file names (pure: project.Detect, as POST /services/detect) or from a
// repository's tree (source.Queries.DetectServices, as the GitHub route). It
// writes nothing; set_services saves what the user confirmed.

// DetectServicesIn is detect_services's input: paths or repository.
type DetectServicesIn struct {
	Paths      []string `json:"paths,omitempty" jsonschema:"rutas de los archivos de la carpeta, relativas a la carpeta que se publica (por ejemplo apps/web/package.json); basta con las que terminan en los nombres que dice la descripción. Hasta 20000"`
	Repository string   `json:"repository,omitempty" jsonschema:"en vez de paths: owner/nombre del repositorio de GitHub (o su id numérico de list_repositories)"`
	Branch     string   `json:"branch,omitempty" jsonschema:"rama del repositorio (por defecto la principal)"`
	Root       string   `json:"root,omitempty" jsonschema:"carpeta donde buscar, dentro de la carpeta o del repositorio (vacía: todo); las carpetas propuestas quedan relativas a ella"`
}

// DetectedServiceOut is one proposed part.
type DetectedServiceOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind" jsonschema:"web o worker"`
	Root        string `json:"root" jsonschema:"su carpeta"`
	Route       string `json:"route" jsonschema:"ruta de la dirección que atendería (vacía en un worker)"`
	StripPrefix bool   `json:"strip_prefix"`
	Evidence    string `json:"evidence" jsonschema:"el archivo que hizo pensar que la carpeta es una app"`
}

// DetectServicesOut is detect_services's result.
type DetectServicesOut struct {
	Items     []DetectedServiceOut `json:"items" jsonschema:"las partes propuestas, en el orden en que set_services las guardaría; vacía si no se encontró ninguna app"`
	Notes     []string             `json:"notes" jsonschema:"lo que la propuesta dejó fuera o supuso, en español: muéstralo al usuario"`
	Truncated bool                 `json:"truncated" jsonschema:"true si no se leyeron todos los archivos: puede faltar alguna parte"`
	NextStep  string               `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) detectServices(ctx context.Context, caller mcpx.Principal, in DetectServicesIn) (DetectServicesOut, error) {
	repository := strings.TrimSpace(in.Repository)
	var (
		found project.Detected
		err   error
	)
	switch {
	case repository != "" && len(in.Paths) > 0:
		return DetectServicesOut{}, errx.InvalidField("paths", "pass paths (a folder) or repository (GitHub), not both")
	case repository != "":
		if err := t.enabled(ctx); err != nil {
			return DetectServicesOut{}, err
		}
		repo, err := t.findRepository(ctx, caller.Organization, repository)
		if err != nil {
			return DetectServicesOut{}, err
		}
		if found, err = t.queries.DetectServices(ctx, caller.Organization, repo.Installation, repo.ID, strings.TrimSpace(in.Branch), in.Root); err != nil {
			return DetectServicesOut{}, err
		}
	default:
		if len(in.Paths) == 0 {
			return DetectServicesOut{}, mcpx.NextStep(errx.InvalidField("paths", "pass paths (a folder's files) or repository (GitHub)"),
				"Lista los archivos de la carpeta del usuario (por ejemplo con git ls-files, o find sin node_modules ni .git) y pásalos en paths.")
		}
		d := project.DetectServices{Paths: in.Paths, Root: in.Root}.Normalize()
		if err = d.Validate(); err != nil {
			return DetectServicesOut{}, err
		}
		found = project.Detect(d)
	}
	out := DetectServicesOut{Items: make([]DetectedServiceOut, 0, len(found.Items)), Notes: found.Notes, Truncated: found.Truncated}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	for _, s := range found.Items {
		out.Items = append(out.Items, DetectedServiceOut{Name: s.Name, Kind: string(s.Kind), Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix, Evidence: s.Evidence})
	}
	switch {
	case len(out.Items) == 0:
		out.NextStep = "No se encontró ninguna app en ese código: revisa que sea la carpeta correcta (o root)."
	case len(out.Items) == 1 && out.Items[0].Root == "" && out.Items[0].Route == "/":
		out.NextStep = "El código es una sola app: no hace falta set_services, publícala como siempre."
	default:
		out.NextStep = "Muestra al usuario estas partes (nombre, carpeta y ruta) y las notas; ajusta lo que pida y guárdalas con set_services (app, items) antes de publicar. " +
			"Después publica la carpeta que las contiene a todas (o conecta el repositorio sin root)."
	}
	return out, nil
}
