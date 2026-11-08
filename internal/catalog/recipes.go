package catalog

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// Factory recipe ids. None starts with OrgPrefix: the organization's own
// recipes and the factory's never collide.
const (
	PostgresBasic      = "postgres-basico"
	RedisBasic         = "redis-basico"
	FilesBasic         = "files-basico"
	PostgresProduction = "postgres-produccion"
)

var allTiers = []project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction}

func storage(def int) Param {
	return Param{Name: ParamStorage, Label: "Tamaño", Unit: "GiB", Min: resourcekind.MinStorageGi, Max: resourcekind.MaxStorageGi,
		Default: def, Mutability: Grows}
}

// basic is the recipe of what every resource was before the catalog: the
// zero spec, any size of today's range, offered everywhere as the default
// and created without asking (so nothing changes for anyone).
func basic(recipe string, k resourcekind.Kind, title, description string) RecipeVersion {
	return RecipeVersion{Recipe: recipe, Version: 1, Kind: k, Title: title, Description: description, Engine: resourcespec.EngineBuiltin,
		Params: []Param{storage(resourcekind.DefaultStorageGi)}, Requires: resourcespec.Spec{}.Requires(k),
		Factory: OfferingDraft{Version: 1, Active: true, Tiers: allTiers, Defaults: allTiers}}
}

// factory is every version that ships in the binary (Factory).
var factory = func() []RecipeVersion {
	pg := resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17, CPU: "1", Memory: "2Gi"}}
	return []RecipeVersion{
		basic(PostgresBasic, resourcekind.Postgres, "PostgreSQL básico",
			"Una base de datos PostgreSQL de una sola instancia. Para empezar y para apps que pueden esperar si se reinicia."),
		basic(RedisBasic, resourcekind.Redis, "Redis",
			"Una caché Redis con contraseña."),
		basic(FilesBasic, resourcekind.Files, "Espacio de archivos",
			"Una carpeta persistente que monta una parte de la app."),
		{
			Recipe: PostgresProduction, Version: 1, Kind: resourcekind.Postgres, Title: "PostgreSQL de producción",
			Description: "PostgreSQL 17 con una réplica en espera (2 instancias) y CPU y memoria reservadas. Sin respaldos automáticos todavía.",
			Engine:      resourcespec.EngineBuiltin, Params: []Param{storage(10)}, Spec: pg, Requires: pg.Requires(resourcekind.Postgres),
			Factory: OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction},
				Limits: map[string]Range{ParamStorage: {Min: 5}}, Preapproved: map[string]Range{ParamStorage: {Max: 50}}},
		},
	}
}()

// Known are the recipe versions an organization can use: the factory's, the
// same for everyone, then the organization's own (OrgRecipe), by recipe then
// version. Whoever resolves a resource's version needs the organization's:
// outside the catalog it is asked through Queries.Version, never Factory
// alone (an architecture test holds that).
type Known []RecipeVersion

// Factory are the versions that ship in this binary. A version is never
// changed or removed once released: an update adds a new one.
func Factory() Known { return Known(slices.Clone(factory)) }

// With are k and the organization's published versions.
func (k Known) With(own []OrgRecipe) Known {
	out := slices.Clone(k)
	for _, o := range own {
		out = append(out, o.RecipeVersion())
	}
	return out
}

// VersionsOf are the versions of recipe k holds, oldest first.
func (k Known) VersionsOf(recipe string) []int {
	out := []int{}
	for _, v := range k {
		if v.Recipe == recipe {
			out = append(out, v.Version)
		}
	}
	slices.Sort(out)
	return out
}

// Version is recipe@version (false: k does not hold it).
func (k Known) Version(recipe string, version int) (RecipeVersion, bool) {
	i := slices.IndexFunc(k, func(v RecipeVersion) bool { return v.Recipe == recipe && v.Version == version })
	if i < 0 {
		return RecipeVersion{}, false
	}
	return k[i], true
}

// Latest are the newest version of each recipe, in k's order (the
// factory's first).
func (k Known) Latest() []RecipeVersion {
	var out []RecipeVersion
	for _, v := range k {
		i := slices.IndexFunc(out, func(o RecipeVersion) bool { return o.Recipe == v.Recipe })
		switch {
		case i < 0:
			out = append(out, v)
		case v.Version > out[i].Version:
			out[i] = v
		}
	}
	return out
}

// RecipeVersion is the version IT published, written here like the
// factory's (a RecipeVersion is made in this file only). It has no
// parameters and no factory offering: IT offers it after publishing.
func (o OrgRecipe) RecipeVersion() RecipeVersion {
	return RecipeVersion{Recipe: o.Recipe, Version: o.Version, Kind: o.Kind, Title: o.Title, Description: o.Description,
		Engine: o.Engine, Params: []Param{}, Spec: o.Spec, Requires: o.Spec.Requires(o.Kind)}
}

// BasicRecipe is the recipe a resource created before the catalog is
// pinned to (its kind's basic recipe, version 1).
func BasicRecipe(k resourcekind.Kind) string { return string(k) + "-basico" }
