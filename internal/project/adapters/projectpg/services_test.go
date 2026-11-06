package projectpg_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectpg"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

func TestServices(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: projectpg.Schema, Files: projectpg.Migrations()})
	repo := projectpg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	id := project.NewID()
	if err := repo.Create(ctx, org, id, project.Create{Name: "Portal", Slug: "portal", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	replace := func(o identity.OrganizationID, s []project.Service) error {
		return tx.Do(ctx, func(ctx context.Context) error { return repo.ReplaceServices(ctx, o, id, s) })
	}
	if got, err := repo.ListServices(ctx, org, id); err != nil || len(got) != 0 {
		t.Fatalf("a project created after the migration has services before the service adds them: %+v %v", got, err)
	}
	if err := repo.ReplaceServices(ctx, other, id, project.DefaultServices(other, id)); errType(err) != errx.TypeNotFound {
		t.Fatalf("outside a transaction, another organization replaced them: %v", err)
	}

	monorepo := project.SetServices{Items: []project.ServiceSpec{
		{Name: "web", Kind: project.ServiceWeb, Root: "apps/web", Route: "/"},
		{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true},
		{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"},
		{Name: "auth", Kind: project.ServiceWeb},
	}}.Services(org, id)
	if err := replace(org, monorepo); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListServices(ctx, org, id)
	if err != nil || !project.SameServices(got, monorepo) {
		t.Fatalf("stored %+v %v", got, err)
	}
	for i, s := range got {
		if s.Position != i || s.Organization != org || s.Project != id || s.UpdatedAt.IsZero() {
			t.Fatalf("service %d: %+v", i, s)
		}
	}

	// Replacing reorders and drops: the list is the whole truth.
	two := []project.Service{monorepo[2], monorepo[0]}
	if err := replace(org, two); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ListServices(ctx, org, id); !project.SameServices(got, two) {
		t.Fatalf("after replace %+v", got)
	}

	// Tenant isolation: another organization neither reads nor writes them.
	if got, err := repo.ListServices(ctx, other, id); err != nil || len(got) != 0 {
		t.Fatalf("another organization reads %+v %v", got, err)
	}
	if err := replace(other, project.DefaultServices(other, id)); errType(err) != errx.TypeNotFound {
		t.Fatalf("another organization replaced them: %v", err)
	}
	if got, _ := repo.ListServices(ctx, org, id); !project.SameServices(got, two) {
		t.Fatalf("changed by another organization: %+v", got)
	}

	// The table guards what validation guards (a bug never stores nonsense);
	// a refused replace keeps the old list (one transaction).
	for name, s := range map[string][]project.Service{
		"same name":    {{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "web", Kind: project.ServiceWeb, Route: "/x"}},
		"same route":   {{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "site", Kind: project.ServiceWeb, Route: "/"}},
		"bad name":     {{Name: "My-Web", Kind: project.ServiceWeb, Route: "/"}},
		"bad kind":     {{Name: "web", Kind: "cron"}},
		"worker route": {{Name: "jobs", Kind: project.ServiceWorker, Route: "/jobs"}},
		"nine":         make9(),
	} {
		if err := replace(org, s); err == nil {
			t.Errorf("%s: stored", name)
		}
		if got, _ := repo.ListServices(ctx, org, id); !project.SameServices(got, two) {
			t.Fatalf("%s: a refused replace changed the list to %+v", name, got)
		}
	}
	if err := replace(org, monorepo[:2]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Do(ctx, func(ctx context.Context) error {
		return repo.ReplaceServices(ctx, org, project.NewID(), project.DefaultServices(org, id))
	}); errType(err) != errx.TypeNotFound {
		t.Fatalf("unknown project: %v", err)
	}
}

func make9() []project.Service {
	out := make([]project.Service, 9)
	for i := range out {
		out[i] = project.Service{Name: "s" + string(rune('a'+i)), Kind: project.ServiceWorker}
	}
	return out
}

// Migration 004 on a database that already holds apps: each becomes one web
// service with all of its code at the whole address.
func TestMigrationGivesEveryAppItsWebService(t *testing.T) {
	before := fstest.MapFS{}
	all := projectpg.Migrations()
	names, _ := fs.Glob(all, "*.up.sql")
	for _, n := range names {
		if n < "004" {
			b, _ := fs.ReadFile(all, n)
			before[n] = &fstest.MapFile{Data: b}
		}
	}
	if len(before) != 3 {
		t.Fatalf("migrations before 004: %d", len(before))
	}
	db := pgtest.DB(t, migrate.Set{Owner: projectpg.Schema, Files: before})
	repo := projectpg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	a, b := project.NewID(), project.NewID()
	if err := repo.Create(ctx, org, a, project.Create{Name: "A", Slug: "app-a", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, other, b, project.Create{Name: "B", Slug: "app-b", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Apply(ctx, db, migrate.Set{Owner: projectpg.Schema, Files: projectpg.Migrations()}); err != nil {
		t.Fatal(err)
	}
	for o, id := range map[identity.OrganizationID]project.ID{org: a, other: b} {
		got, err := repo.ListServices(ctx, o, id)
		if err != nil || !project.SameServices(got, project.DefaultServices(o, id)) || got[0].Organization != o {
			t.Fatalf("backfilled %+v %v", got, err)
		}
	}
}
