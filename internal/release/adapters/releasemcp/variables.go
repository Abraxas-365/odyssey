package releasemcp

import (
	"context"
	"net/url"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Variables and secrets for assistants. An assistant may set plain variables
// and ask for a secret, never see or set a secret's value: a person types it
// in the console, where it is sealed in the browser to the cluster's key.

// SetEnvIn is set_env's input.
type SetEnvIn struct {
	App   string            `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Set   map[string]string `json:"set,omitempty" jsonschema:"variables a crear o cambiar: nombre → valor (texto, sin secretos)"`
	Unset []string          `json:"unset,omitempty" jsonschema:"nombres de variables a quitar"`
}

// SetEnvOut is set_env's result.
type SetEnvOut struct {
	App string `json:"app" jsonschema:"slug de la app"`
	// Variables are the names in force after the change (values are not
	// echoed back).
	Variables []string `json:"variables" jsonschema:"nombres de las variables del entorno tras el cambio"`
	Secrets   []string `json:"secrets" jsonschema:"nombres de los secretos del entorno (sus valores nunca se muestran)"`
	Version   *Version `json:"version,omitempty" jsonschema:"la publicación que aplica el cambio, si empezó una"`
	NextStep  string   `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) setEnv(ctx context.Context, caller mcpx.Principal, in SetEnvIn) (SetEnvOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return SetEnvOut{}, err
	}
	for k := range in.Set {
		if looksSecret(k) {
			return SetEnvOut{}, mcpx.NextStep(errx.InvalidField("set", k+" looks like a secret: secrets are never set by an assistant"),
				"Usa request_secret (name="+k+") para que una persona cargue el valor en la consola, cifrado. No pidas el valor por el chat.")
		}
	}
	out, err := t.commands.SetEnv(ctx, caller.Organization, p.ID, env.ID, release.SetEnv{Set: in.Set, Unset: in.Unset, RequestedBy: caller.User})
	if err != nil {
		return SetEnvOut{}, err
	}
	res := SetEnvOut{App: p.Slug, Variables: out.Settings.EnvNames(), Secrets: out.Settings.SecretNames()}
	switch {
	case out.Deployment != nil:
		v := version(*out.Deployment, nil)
		res.Version = &v
		res.NextStep = followStep(p, *out.Deployment)
	default:
		res.NextStep = "Listo: las variables quedaron guardadas. Si la app aún no está en línea o se está publicando, las recibirá con esa publicación."
	}
	return res, nil
}

// looksSecret refuses the obvious: an assistant pasting a password as a plain
// variable. It is a guard for honest mistakes, not a boundary (a variable is
// visible to anyone who can read the app).
func looksSecret(name string) bool {
	up := strings.ToUpper(name)
	for _, w := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "PRIVATE_KEY", "API_KEY", "APIKEY", "CREDENTIAL"} {
		if strings.Contains(up, w) {
			return true
		}
	}
	return false
}

// RequestSecretIn is request_secret's input.
type RequestSecretIn struct {
	App  string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Name string `json:"name" jsonschema:"nombre de la variable secreta, p. ej. STRIPE_API_KEY"`
	Note string `json:"note,omitempty" jsonschema:"para qué es, en una frase (lo ve la persona que lo carga)"`
}

// RequestSecretOut is request_secret's result.
type RequestSecretOut struct {
	App    string `json:"app" jsonschema:"slug de la app"`
	Name   string `json:"name"`
	Status string `json:"status" jsonschema:"waiting_for_value (falta que una persona lo cargue), loaded (ya tiene valor), reload (hay que volver a cargarlo), unavailable (el clúster aún no puede guardarlo: TI) o external (viene del almacén de la empresa)"`
	// ConsoleURL is where a person loads the value.
	ConsoleURL string `json:"console_url" jsonschema:"enlace para que el usuario cargue el valor en la consola"`
	NextStep   string `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) requestSecret(ctx context.Context, caller mcpx.Principal, in RequestSecretIn) (RequestSecretOut, error) {
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return RequestSecretOut{}, err
	}
	v, err := t.commands.RequestSecret(ctx, caller.Organization, p.ID, env.ID, release.RequestSecret{Name: in.Name, Note: in.Note,
		RequestedBy: caller.User})
	if err != nil {
		return RequestSecretOut{}, err
	}
	link := t.opts.ConsoleURL + "/apps/" + p.ID.String() + "?secret=" + url.QueryEscape(v.Name)
	res := RequestSecretOut{App: p.Slug, Name: v.Name, ConsoleURL: link}
	switch v.State {
	case release.SecretStateMissing:
		res.Status = "waiting_for_value"
		res.NextStep = "Pásale al usuario el enlace console_url para que cargue el valor allí (se cifra en su navegador). " +
			"No le pidas el valor por el chat. Cuando lo cargue, la app se vuelve a publicar sola con el secreto; get_app lo muestra."
	case release.SecretStateReload:
		res.Status = "reload"
		res.NextStep = "El secreto existe pero el clúster cambió su llave: pásale al usuario console_url para que lo vuelva a cargar."
	case release.SecretStateUnavailable:
		res.Status = "unavailable"
		res.NextStep = "El clúster de esta app todavía no puede guardar este tipo de secreto. Dile al usuario que lo pida a TI; no lo pongas como variable."
	case release.SecretStateExternal:
		res.Status = "external"
		res.NextStep = "El secreto viene del almacén de secretos de la empresa; si falla, TI debe revisarlo."
	default:
		res.Status = "loaded"
		res.NextStep = "El secreto ya tiene valor. Para cambiarlo, el usuario lo vuelve a cargar en console_url."
	}
	return res, nil
}
