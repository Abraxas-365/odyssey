package catalogpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/catalogpg"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

func orgRecipe(org identity.OrganizationID, recipe string, version int) catalog.OrgRecipe {
	return catalog.OrgRecipe{Organization: org, Recipe: recipe, Version: version, Kind: resourcekind.Postgres,
		Title: "Base de reportes", Description: "La RDS de reportes de la empresa", Engine: resourcespec.EngineExisting,
		CreatedBy: user, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
}

func TestPublishedAndTenantIsolation(t *testing.T) {
	repo, _, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	want := orgRecipe(org, "org-reportes", 1)
	// a spec round-trips through jsonb (the existing engine's arrives in a
	// later cut; any typed spec is stored as is)
	want.Spec = resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17}}
	// stored Clean, as validated
	want.Title = "  Base de reportes \n"
	for _, o := range []catalog.OrgRecipe{orgRecipe(org, "org-reportes", 2), want,
		orgRecipe(org, "org-cache", 1), orgRecipe(other, "org-reportes", 1)} {
		if err := repo.Publish(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Published(ctx, org)
	if err != nil || len(got) != 3 {
		t.Fatalf("published: %+v %v", got, err)
	}
	// by recipe, then version
	if got[0].Recipe != "org-cache" || got[1].Version != 1 || got[2].Version != 2 {
		t.Fatalf("order: %+v", got)
	}
	if g := got[1]; g.Organization != org || g.Kind != want.Kind || g.Title != "Base de reportes" || g.Description != want.Description ||
		g.Engine != want.Engine || g.Spec.Postgres == nil || *g.Spec.Postgres != *want.Spec.Postgres || g.CreatedBy != user ||
		!g.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("round trip: %+v", g)
	}
	if got, _ := repo.Published(ctx, other); len(got) != 1 || got[0].Organization != other {
		t.Fatalf("other organization sees %+v", got)
	}
	if got, err := repo.Published(ctx, identity.NewOrganizationID()); err != nil || len(got) != 0 {
		t.Fatalf("a third organization sees %+v %v", got, err)
	}
}

// A published version never changes: publishing it again is a conflict,
// and the table refuses updates and deletes even from code that forgets.
func TestPublishedVersionsNeverChange(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: catalogpg.Schema, Files: catalogpg.Migrations()})
	repo, ctx := catalogpg.New(db), context.Background()
	org := identity.NewOrganizationID()
	if err := repo.Publish(ctx, orgRecipe(org, "org-reportes", 1)); err != nil {
		t.Fatal(err)
	}
	again := orgRecipe(org, "org-reportes", 1)
	again.Title = "Otra"
	if err := repo.Publish(ctx, again); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("republish: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE catalog.recipe_versions SET title = 'x' WHERE organization_id = $1`, org); err == nil {
		t.Fatal("a published version was updated")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM catalog.recipe_versions WHERE organization_id = $1`, org); err == nil {
		t.Fatal("a published version was deleted")
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE catalog.recipe_versions`); err == nil {
		t.Fatal("the published versions were truncated")
	}
	if got, _ := repo.Published(ctx, org); len(got) != 1 || got[0].Title != "Base de reportes" {
		t.Fatalf("after: %+v", got)
	}
}

// The table holds what the domain holds: an id outside the organizations'
// prefix, or another engine, is refused by Postgres too.
func TestPublishedRowsAreChecked(t *testing.T) {
	repo, _, ctx := setup(t)
	org := identity.NewOrganizationID()
	bad := []catalog.OrgRecipe{orgRecipe(org, "postgres-basico", 1), orgRecipe(org, "org-x", 0)}
	builtin := orgRecipe(org, "org-y", 1)
	builtin.Engine = resourcespec.EngineBuiltin
	bad = append(bad, builtin)
	for _, o := range bad {
		if err := repo.Publish(ctx, o); err == nil {
			t.Errorf("%s@%d (%s) was stored", o.Recipe, o.Version, o.Engine)
		}
	}
}
