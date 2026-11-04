package releasemcp

import (
	"context"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// AccessOut is who can open an app.
type AccessOut struct {
	Mode string `json:"mode" jsonschema:"team, organization o public"`
	// Pending: the mode is stored but the app has not been given it yet.
	Pending bool `json:"pending" jsonschema:"true mientras el cambio se aplica a la app en línea"`
}

// accessOf reads the mode in force and whether the serving app still has to
// receive it (a config deployment is on its way).
func accessOf(s release.Settings, cur release.Current) *AccessOut {
	pending := false
	if cur.Release != nil {
		applied := int64(-1) // the cluster was never told a mode
		if cur.AppliedSettingsRevision != nil {
			applied = *cur.AppliedSettingsRevision
		}
		pending = s.Revision > applied
	}
	return &AccessOut{Mode: string(s.Access.Mode), Pending: pending}
}

// SetAccessIn is set_access's input.
type SetAccessIn struct {
	App  string `json:"app" jsonschema:"slug o id de la app (list_apps los muestra)"`
	Mode string `json:"mode" jsonschema:"team (solo el equipo), organization (toda la empresa) o public (cualquiera con el enlace; exige odyssey:access:public)"`
}

// SetAccessOut is set_access's result.
type SetAccessOut struct {
	App    string    `json:"app" jsonschema:"slug de la app"`
	Access AccessOut `json:"access"`
	// Version is the publication that applies the change; absent when it was
	// only stored (nothing online yet, or another publication will carry it).
	Version  *Version `json:"version,omitempty" jsonschema:"la publicación que aplica el cambio, si empezó una"`
	NextStep string   `json:"next_step" jsonschema:"qué hacer ahora"`
}

func (t tools) setAccess(ctx context.Context, caller mcpx.Principal, in SetAccessIn) (SetAccessOut, error) {
	mode := release.AccessMode(strings.ToLower(strings.TrimSpace(in.Mode)))
	if !mode.Valid() {
		return SetAccessOut{}, mcpx.NextStep(errx.InvalidField("mode", release.AccessMessage),
			"Usa team (solo el equipo), organization (toda la empresa) o public (cualquiera con el enlace).")
	}
	// The tool only lists projects:write; making an app public is the one
	// change that needs a permission of its own, as in the REST API.
	if mode == release.AccessPublic && !caller.Can(permission.AccessPublic) {
		return SetAccessOut{}, mcpx.NextStep(errx.Forbidden("making an app public needs the permission "+permission.AccessPublic),
			"Dile al usuario que pida a TI que la deje pública, o usa team u organization.")
	}
	p, env, err := t.resolve(ctx, caller.Organization, in.App)
	if err != nil {
		return SetAccessOut{}, err
	}
	out, err := t.commands.SetAccess(ctx, caller.Organization, p.ID, env.ID, release.SetAccess{Mode: mode, RequestedBy: caller.User})
	if err != nil {
		return SetAccessOut{}, err
	}
	res := SetAccessOut{App: p.Slug, Access: AccessOut{Mode: string(out.Settings.Access.Mode)}}
	switch {
	case out.Deployment != nil:
		v := version(*out.Deployment, nil)
		res.Version, res.Access.Pending = &v, true
		res.NextStep = followStep(p, *out.Deployment)
	default:
		res.NextStep = "Listo: el acceso quedó guardado. Si la app aún no está en línea o se está publicando, lo recibirá con esa publicación; get_app muestra el estado."
	}
	return res, nil
}
