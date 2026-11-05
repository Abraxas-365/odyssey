package releasemcp

import "github.com/Abraxas-365/odyssey/internal/release"

// UsesOut is what get_app says an app depends on and what is missing (the
// release.Uses read model, with descriptions for the assistant).
type UsesOut struct {
	Access      UsesAccessOut     `json:"access" jsonschema:"quién puede abrir la app (mode: team, organization o public) y si el cambio todavía se está aplicando (pending)"`
	Identity    UsesIdentityOut   `json:"identity" jsonschema:"verifiable es true cuando la app recibe la cabecera firmada X-Odicea-Assertion con la identidad de quien la abre (modos team y organization)"`
	Resources   []UsedResourceOut `json:"resources" jsonschema:"bases de datos, cachés y archivos conectados a la app"`
	Secrets     *UsedSecretsOut   `json:"secrets" jsonschema:"secretos de la app: total, nombres que faltan por cargar (missing), cuántos vienen de la bóveda de la empresa (external) y cuántos no pueden llegar a la app por ahora (unavailable); null si no se pudo leer (mira unavailable)"`
	Variables   int               `json:"variables" jsonschema:"cuántas variables de entorno normales (no secretas) define la app"`
	AI          *UsedAIOut        `json:"ai" jsonschema:"uso de la IA: available (se puede activar aquí), enabled (la app la usa) y status (off, enabling, enabled, rotating, disabling o revoke_failed); null si no se pudo leer (mira unavailable)"`
	Egress      *UsedEgressOut    `json:"egress" jsonschema:"salida a internet que permite el clúster: mode es internet o restricted; enforced es true cuando el clúster confirmó que la aplicó; null si no se pudo leer (mira unavailable)"`
	Unavailable []string          `json:"unavailable" jsonschema:"secciones que no se pudieron leer ahora (resources, secrets, ai o egress); su contenido es desconocido, no que esté bien: vuelve a llamar get_app en un momento"`
	Attention   []AttentionOut    `json:"attention" jsonschema:"lo que falta, bloquea o está en curso; vacío si no hay nada. Mira who de cada uno: owner (la gente de la app puede resolverlo), it (solo TI) o wait (en curso, no hay nada que hacer)"`
}

type UsesAccessOut struct {
	Mode    string `json:"mode" jsonschema:"team, organization o public"`
	Pending bool   `json:"pending" jsonschema:"true mientras el cambio se aplica a la app en línea"`
	Failed  bool   `json:"failed" jsonschema:"true si el cambio no se pudo aplicar y nada lo está reintentando: guardar el cambio otra vez lo reintenta"`
}

type UsesIdentityOut struct {
	Verifiable bool `json:"verifiable" jsonschema:"true si la app recibe X-Odicea-Assertion"`
}

type UsedResourceOut struct {
	Name      string   `json:"name" jsonschema:"nombre del recurso"`
	Kind      string   `json:"kind" jsonschema:"postgres (base de datos), redis (caché) o files (archivos)"`
	Prefix    string   `json:"prefix,omitempty" jsonschema:"prefijo de sus variables, si lo tiene"`
	Status    string   `json:"status" jsonschema:"ready (lista), preparing (preparándose), failed (falló), deleting (eliminándose) o unknown (no se pudo consultar ahora)"`
	Detail    string   `json:"detail,omitempty" jsonschema:"lo que dijo el clúster, por ejemplo por qué falló"`
	Variables []string `json:"variables" jsonschema:"nombres de las variables de entorno con las que la app lee el recurso"`
}

type UsedSecretsOut struct {
	Total       int      `json:"total" jsonschema:"secretos de la app"`
	Missing     []string `json:"missing" jsonschema:"nombres de los secretos que la app aún no recibe: nadie cargó el valor o hay que volver a cargarlo (la consola, o request_secret, lo resuelve)"`
	External    int      `json:"external" jsonschema:"cuántos se leen de la bóveda de la empresa"`
	Unavailable int      `json:"unavailable" jsonschema:"cuántos no pueden llegar a la app por ahora aunque la gente haga algo: el clúster no recibe secretos o la bóveda no se pudo leer; es cosa de TI"`
}

type UsedAIOut struct {
	Available bool   `json:"available" jsonschema:"la IA se puede activar en esta app"`
	Enabled   bool   `json:"enabled" jsonschema:"la app usa la IA"`
	Status    string `json:"status" jsonschema:"off, enabling, enabled, rotating, disabling o revoke_failed (no se pudo retirar una llave anterior)"`
}

type UsedEgressOut struct {
	Mode     string `json:"mode" jsonschema:"internet (sin límite) o restricted (solo lo permitido); vacío si la app no tiene clúster"`
	State    string `json:"state,omitempty" jsonschema:"applied, pending, failed o unsupported: cómo recibió el clúster la política"`
	Enforced bool   `json:"enforced" jsonschema:"true cuando el clúster confirmó que la aplicó"`
}

// AttentionOut is one thing to know about.
type AttentionOut struct {
	Kind    string `json:"kind" jsonschema:"secret_missing, secrets_unavailable, resource_not_ready, resource_failed, ai_error, ai_pending, config_pending, config_failed o egress_not_applied"`
	Who     string `json:"who" jsonschema:"owner (la gente de la app puede resolverlo: cargar un secreto, guardar de nuevo un cambio), it (solo TI puede: el clúster, la IA, un recurso que falló) o wait (en curso: no hay nada que hacer, solo esperar)"`
	Message string `json:"message" jsonschema:"qué pasa, en palabras simples"`
}

// usesUnavailable is what get_app says when the read model could not be made
// at all (release's own data failed): every section unknown, nothing claimed.
func usesUnavailable() UsesOut {
	out := usesOf(release.Uses{Resources: []release.UsedResource{}, Unavailable: release.UsesSections})
	out.Access.Mode = ""
	return out
}

func usesOf(u release.Uses) UsesOut {
	out := UsesOut{
		Access:      UsesAccessOut{Mode: string(u.Access.Mode), Pending: u.Access.Pending, Failed: u.Access.Failed},
		Identity:    UsesIdentityOut{Verifiable: u.Identity.Verifiable},
		Resources:   make([]UsedResourceOut, 0, len(u.Resources)),
		Variables:   u.Variables,
		Unavailable: make([]string, 0, len(u.Unavailable)),
		Attention:   make([]AttentionOut, 0, len(u.Attention)),
	}
	if s := u.Secrets; s != nil {
		out.Secrets = &UsedSecretsOut{Total: s.Total, Missing: append([]string{}, s.Missing...), External: s.External, Unavailable: s.Unavailable}
	}
	if a := u.AI; a != nil {
		out.AI = &UsedAIOut{Available: a.Available, Enabled: a.Enabled, Status: a.Status}
	}
	if e := u.Egress; e != nil {
		out.Egress = &UsedEgressOut{Mode: e.Mode, State: string(e.State), Enforced: e.Enforced}
	}
	for _, s := range u.Unavailable {
		out.Unavailable = append(out.Unavailable, string(s))
	}
	for _, r := range u.Resources {
		out.Resources = append(out.Resources, UsedResourceOut{Name: r.Name, Kind: string(r.Kind), Prefix: r.Prefix, Status: string(r.Status),
			Detail: r.Detail, Variables: append([]string{}, r.Variables...)})
	}
	for _, a := range u.Attention {
		out.Attention = append(out.Attention, AttentionOut{Kind: string(a.Kind), Who: string(a.Who), Message: a.Message})
	}
	return out
}
