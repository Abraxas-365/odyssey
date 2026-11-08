package catalogpg_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/catalogpg"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

func setup(t *testing.T) (*catalogpg.Repository, txn.Runner, context.Context) {
	db := pgtest.DB(t, migrate.Set{Owner: catalogpg.Schema, Files: catalogpg.Migrations()})
	return catalogpg.New(db), txn.Postgres{DB: db}, context.Background()
}

func factory(org identity.OrganizationID) []catalog.Offering {
	now := time.Now().UTC().Truncate(time.Microsecond)
	var out []catalog.Offering
	for _, v := range catalog.Factory().Latest() {
		out = append(out, catalog.Offering{Organization: org, Recipe: v.Recipe, OfferingDraft: v.Factory.Normalize(),
			Revision: 1, CreatedAt: now, UpdatedAt: now})
	}
	return out
}

func TestSeedListFindAndTenantIsolation(t *testing.T) {
	repo, _, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	n, err := repo.SeedFactory(ctx, factory(org))
	if err != nil || n != 4 {
		t.Fatalf("seed: %d %v", n, err)
	}
	// seeding again (another replica, a restart) adds nothing
	if n, err := repo.SeedFactory(ctx, factory(org)); err != nil || n != 0 {
		t.Fatalf("reseed: %d %v", n, err)
	}
	list, err := repo.List(ctx, org)
	if err != nil || len(list) != 4 || list[0].Recipe != catalog.FilesBasic {
		t.Fatalf("list: %+v %v", list, err)
	}
	got, err := repo.Find(ctx, org, catalog.PostgresProduction)
	if err != nil || !got.Active || got.Revision != 1 || !slices.Equal(got.Tiers, []project.Tier{project.TierProduction}) ||
		got.Limits[catalog.ParamStorage] != (catalog.Range{Min: 5}) || got.Preapproved[catalog.ParamStorage] != (catalog.Range{Max: 50}) ||
		len(got.Defaults) != 0 || !got.UpdatedBy.IsZero() {
		t.Fatalf("find: %+v %v", got, err)
	}
	if list, _ := repo.List(ctx, other); len(list) != 0 {
		t.Fatalf("cross-tenant list: %d", len(list))
	}
	if _, err := repo.Find(ctx, other, catalog.PostgresProduction); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant find: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	repo, tx, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	if _, err := repo.SeedFactory(ctx, factory(org)); err != nil {
		t.Fatal(err)
	}
	o, err := repo.Find(ctx, org, catalog.RedisBasic)
	if err != nil {
		t.Fatal(err)
	}
	o.Active, o.Tiers, o.Defaults = true, []project.Tier{project.TierInternal}, []project.Tier{project.TierInternal}
	o.Limits = map[string]catalog.Range{catalog.ParamStorage: {Max: 8}}
	o.Teams = []string{"grp_datos", "grp_ventas"}
	o.UpdatedBy, o.UpdatedAt = user, time.Now().UTC().Truncate(time.Microsecond)
	err = tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.LockCatalog(ctx, org); err != nil {
			return err
		}
		return repo.Update(ctx, o)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Find(ctx, org, catalog.RedisBasic)
	if err != nil || got.Revision != 2 || got.UpdatedBy != user || got.Limits[catalog.ParamStorage].Max != 8 ||
		!slices.Equal(got.Tiers, []project.Tier{project.TierInternal}) || !slices.Equal(got.Teams, []string{"grp_datos", "grp_ventas"}) {
		t.Fatalf("updated: %+v %v", got, err)
	}
	// no teams (everyone) is stored as empty, never NULL
	o.Teams = nil
	if err := repo.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Find(ctx, org, catalog.RedisBasic); err != nil || got.Teams == nil || len(got.Teams) != 0 {
		t.Fatalf("no teams: %#v %v", got.Teams, err)
	}
	// the other organization's offering is untouched
	if theirs, err := repo.Find(ctx, other, catalog.RedisBasic); err == nil && len(theirs.Teams) != 0 {
		t.Fatalf("leaked teams: %+v", theirs)
	}
	// another organization's offering is not reachable
	o.Organization = other
	if err := repo.Update(ctx, o); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant update: %v", err)
	}
	// a default outside the tiers is refused by the table too
	o.Organization, o.Defaults = org, []project.Tier{project.TierProduction}
	if err := repo.Update(ctx, o); err == nil {
		t.Fatal("default outside tiers stored")
	}
}
