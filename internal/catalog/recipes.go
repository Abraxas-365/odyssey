package catalog

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// Factory recipe ids.
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

// factory is every version that ships in the binary. A version is never
// changed or removed once released: an update adds a new one.
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

// Versions are every recipe version this binary knows, by recipe then
// version.
func Versions() []RecipeVersion { return slices.Clone(factory) }

// Version is recipe@version (false: this binary does not know it).
func Version(recipe string, version int) (RecipeVersion, bool) {
	i := slices.IndexFunc(factory, func(v RecipeVersion) bool { return v.Recipe == recipe && v.Version == version })
	if i < 0 {
		return RecipeVersion{}, false
	}
	return factory[i], true
}

// Latest are the newest version of each recipe, in factory order.
func Latest() []RecipeVersion {
	var out []RecipeVersion
	for _, v := range factory {
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

// BasicRecipe is the recipe a resource created before the catalog is
// pinned to (its kind's basic recipe, version 1).
func BasicRecipe(k resourcekind.Kind) string { return string(k) + "-basico" }
