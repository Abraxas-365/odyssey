package releasepg_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
)

// Migration 018 on a database that already holds versions: each ran one web
// service at "/" with all of the code, with the version's image and settings
// (the defaults where an old version stored none).
func TestMigrationGivesEveryVersionItsWebService(t *testing.T) {
	before := fstest.MapFS{}
	all := releasepg.Migrations()
	names, _ := fs.Glob(all, "*.up.sql")
	for _, n := range names {
		if n < "018" {
			b, _ := fs.ReadFile(all, n)
			before[n] = &fstest.MapFile{Data: b}
		}
	}
	if len(before) != 17 {
		t.Fatalf("migrations before 018: %d", len(before))
	}
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: before})
	ctx := context.Background()
	org, proj := identity.NewOrganizationID(), project.NewID()
	full, empty := release.NewID(), release.NewID()
	for id, cfg := range map[release.ID]string{
		full:  `{"port": 3000, "health_path": "/health", "replicas": 3, "env": {}}`,
		empty: `{}`,
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO release.releases (id, organization_id, project_id, image, config) VALUES ($1, $2, $3, $4, $5)`,
			id, org, proj, "r.local/app"+digest, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate.Apply(ctx, db, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()}); err != nil {
		t.Fatal(err)
	}
	repo := releasepg.New(db)
	for id, want := range map[release.ID]release.Service{
		full:  {Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r.local/app" + digest, Port: 3000, HealthPath: "/health", Replicas: 3},
		empty: {Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r.local/app" + digest, Port: 8080, HealthPath: "/", Replicas: 1},
	} {
		got, err := repo.FindRelease(ctx, org, id)
		if err != nil || len(got.Services) != 1 || got.Services[0] != want {
			t.Fatalf("backfilled %+v %v, want %+v", got.Services, err, want)
		}
	}
}
