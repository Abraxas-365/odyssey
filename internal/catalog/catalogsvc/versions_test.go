package catalogsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

func published(org identity.OrganizationID, version int, title string) catalog.OrgRecipe {
	return catalog.OrgRecipe{Organization: org, Recipe: "org-reportes", Version: version, Kind: resourcekind.Postgres,
		Title: title, Engine: resourcespec.EngineExisting, Spec: resourcespec.ExamplePostgres("vault"), CreatedBy: user, CreatedAt: time.Now().UTC()}
}

// A resource's pinned version is read through Version: the factory's and
// the organization's own, never another organization's.
func TestVersionKnowsTheOrganizationsOwnRecipes(t *testing.T) {
	svc, _, repo := setup()
	ctx, org, other := context.Background(), identity.NewOrganizationID(), identity.NewOrganizationID()
	for _, o := range []catalog.OrgRecipe{published(org, 1, "Reportes"), published(org, 2, "Reportes (réplica)")} {
		if err := repo.Publish(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	v, ok, err := svc.Version(ctx, org, "org-reportes", 1)
	if err != nil || !ok || v.Title != "Reportes" || v.Engine != resourcespec.EngineExisting ||
		len(v.Params) != 0 || len(v.Requires) != 2 || v.Requires[0] != resourcespec.ConnectExisting || v.Requires[1] != resourcespec.SecretStore("vault") {
		t.Fatalf("own v1: %+v %v %v", v, ok, err)
	}
	if v, ok, _ := svc.Version(ctx, org, catalog.PostgresProduction, 1); !ok || v.Recipe != catalog.PostgresProduction {
		t.Fatalf("factory: %+v %v", v, ok)
	}
	if _, ok, err := svc.Version(ctx, other, "org-reportes", 1); ok || err != nil {
		t.Fatalf("another organization found it: %v %v", ok, err)
	}
	if _, ok, _ := svc.Version(ctx, org, "org-reportes", 3); ok {
		t.Fatal("an unpublished version was found")
	}
}

// The catalog lists the organization's recipe at its latest version, after
// the factory's; another organization does not see it.
func TestListShowsTheOrganizationsOwnRecipes(t *testing.T) {
	svc, _, repo := setup()
	ctx, org, other := context.Background(), identity.NewOrganizationID(), identity.NewOrganizationID()
	for _, o := range []catalog.OrgRecipe{published(org, 1, "Reportes"), published(org, 2, "Reportes (réplica)")} {
		if err := repo.Publish(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := svc.List(ctx, org)
	factory := len(catalog.Factory().Latest())
	if err != nil || len(entries) != factory+1 {
		t.Fatalf("list: %d %v", len(entries), err)
	}
	e := entries[factory]
	if e.Recipe != "org-reportes" || e.Version != 2 || e.Title != "Reportes (réplica)" || len(e.Versions) != 2 || e.Offering.Active {
		t.Fatalf("own entry: %+v", e)
	}
	if entries, _ := svc.List(ctx, other); len(entries) != factory {
		t.Fatalf("another organization lists %d", len(entries))
	}
}

func TestKnownAddsTheOrganizationsVersionsAfterTheFactorys(t *testing.T) {
	org := identity.NewOrganizationID()
	k := catalog.Factory().With([]catalog.OrgRecipe{published(org, 2, "dos"), published(org, 1, "uno")})
	if got := k.VersionsOf("org-reportes"); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions %v", got)
	}
	latest := k.Latest()
	if last := latest[len(latest)-1]; last.Recipe != "org-reportes" || last.Version != 2 {
		t.Fatalf("latest %+v", last)
	}
	if latest[0].Recipe != catalog.Factory().Latest()[0].Recipe {
		t.Fatalf("the factory's come first: %s", latest[0].Recipe)
	}
	if len(catalog.Factory().VersionsOf("org-reportes")) != 0 {
		t.Fatal("With changed the factory")
	}
}
