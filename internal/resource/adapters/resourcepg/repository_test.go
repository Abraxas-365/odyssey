package resourcepg_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func errType(err error) errx.Type {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Type
	}
	return ""
}

func sized(r resource.Resource, gi int) resource.Resource {
	r.StorageGi, r.Params = gi, map[string]int{"storage_gi": gi}
	return r
}

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: resourcepg.Schema, Files: resourcepg.Migrations()})
	repo := resourcepg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	proj, env := project.NewID(), project.NewEnvironmentID()
	mk := func(org identity.OrganizationID, env project.EnvironmentID, name string, kind resourcekind.Kind) resource.Resource {
		return resource.Resource{ID: resource.NewID(), Organization: org, Project: proj, Environment: env, Name: name, Kind: kind,
			StorageGi: 2, Recipe: string(kind) + "-basico", RecipeVersion: 1, Params: map[string]int{"storage_gi": 2},
			Generation: 1, Status: resource.StatusActive, CreatedBy: user}
	}

	db1 := mk(org, env, "db", resourcekind.Postgres)
	if err := repo.Create(ctx, db1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Find(ctx, org, env, "db")
	if err != nil || got.ID != db1.ID || got.Kind != resourcekind.Postgres || got.StorageGi != 2 || got.Status != resource.StatusActive || got.CreatedAt.IsZero() ||
		got.Recipe != "postgres-basico" || got.RecipeVersion != 1 || got.Params["storage_gi"] != 2 || len(got.Params) != 1 {
		t.Fatalf("find %+v %v", got, err)
	}

	// names are unique per environment; another environment or tenant may reuse them
	if err := repo.Create(ctx, mk(org, env, "db", resourcekind.Redis)); errType(err) != errx.TypeConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	env2 := project.NewEnvironmentID()
	if err := repo.Create(ctx, mk(org, env2, "db", resourcekind.Postgres)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, mk(other, env, "db", resourcekind.Postgres)); err != nil {
		t.Fatal(err)
	}
	// a bad row is refused by the table
	bad := mk(org, env, "Bad Name", resourcekind.Postgres)
	if err := repo.Create(ctx, bad); errType(err) != errx.TypeValidation {
		t.Fatalf("bad name: %v", err)
	}
	if err := repo.Create(ctx, sized(mk(org, env, "big", resourcekind.Files), 101)); errType(err) != errx.TypeValidation {
		t.Fatalf("size: %v", err)
	}
	// params.storage_gi and storage_gi never disagree; a recipe is named
	drift := mk(org, env, "drift", resourcekind.Files)
	drift.Params = map[string]int{"storage_gi": 3}
	if err := repo.Create(ctx, drift); errType(err) != errx.TypeValidation {
		t.Fatalf("params out of step: %v", err)
	}
	unnamed := mk(org, env, "unnamed", resourcekind.Files)
	unnamed.Recipe = ""
	if err := repo.Create(ctx, unnamed); errType(err) != errx.TypeValidation {
		t.Fatalf("no recipe: %v", err)
	}
	// a resource the company already has: no size, and then no storage_gi in
	// params; a size of 0 with params.storage_gi, or a size without it, is
	// refused; object storage is a kind (in a place of its own, out of the
	// lists below)
	envCompany := project.NewEnvironmentID()
	company := mk(org, envCompany, "bucket", resourcekind.ObjectStorage)
	company.Project = project.NewID()
	company.Recipe, company.StorageGi, company.Params = "org-s3", 0, map[string]int{}
	if err := repo.Create(ctx, company); err != nil {
		t.Fatalf("existing without size: %v", err)
	}
	if got, err := repo.Find(ctx, org, envCompany, "bucket"); err != nil || got.StorageGi != 0 || len(got.Params) != 0 || got.Kind != resourcekind.ObjectStorage {
		t.Fatalf("existing round trip %+v %v", got, err)
	}
	zeroInParams := company
	zeroInParams.ID, zeroInParams.Name, zeroInParams.Params = resource.NewID(), "zero", map[string]int{"storage_gi": 0}
	if err := repo.Create(ctx, zeroInParams); errType(err) != errx.TypeValidation {
		t.Fatalf("size 0 in params: %v", err)
	}
	sizeWithout := mk(org, env, "nosize", resourcekind.Files)
	sizeWithout.Params = map[string]int{}
	if err := repo.Create(ctx, sizeWithout); errType(err) != errx.TypeValidation {
		t.Fatalf("size without params: %v", err)
	}

	cache := mk(org, env, "cache", resourcekind.Redis)
	if err := repo.Create(ctx, cache); err != nil {
		t.Fatal(err)
	}

	// Find / FindForUpdate: tenant isolation
	if _, err := repo.Find(ctx, other, env2, "db"); errType(err) != errx.TypeNotFound {
		t.Fatalf("find across tenants: %v", err)
	}
	if _, err := repo.Find(ctx, org, env, "ghost"); errType(err) != errx.TypeNotFound {
		t.Fatalf("find ghost: %v", err)
	}
	tx := txn.Postgres{DB: db}
	err = tx.Do(ctx, func(ctx context.Context) error {
		if _, err := repo.FindForUpdate(ctx, org, env, "db"); err != nil {
			return err
		}
		if _, err := repo.FindForUpdate(ctx, other, env2, "db"); errType(err) != errx.TypeNotFound {
			t.Errorf("find for update across tenants: %v", err)
		}
		return repo.LockEnvironment(ctx, org, env)
	})
	if err != nil {
		t.Fatal(err)
	}

	// List and Count: only the tenant's environment, oldest first
	list, err := repo.List(ctx, org, env)
	if err != nil || len(list) != 2 || list[0].Name != "db" || list[1].Name != "cache" {
		t.Fatalf("list %+v %v", list, err)
	}
	if list, _ := repo.List(ctx, other, env2); len(list) != 0 {
		t.Fatalf("list across tenants: %+v", list)
	}
	if n, err := repo.Count(ctx, org, env); err != nil || n != 2 {
		t.Fatalf("count %d %v", n, err)
	}
	if n, _ := repo.Count(ctx, other, env2); n != 0 {
		t.Fatalf("count across tenants: %d", n)
	}

	// ListProject: every environment of the project, only the tenant's
	if list, err := repo.ListProject(ctx, org, proj); err != nil || len(list) != 3 {
		t.Fatalf("list project %+v %v", list, err)
	}
	if list, _ := repo.ListProject(ctx, org, project.NewID()); len(list) != 0 {
		t.Fatalf("list another project: %+v", list)
	}
	if list, _ := repo.ListProject(ctx, identity.NewOrganizationID(), proj); len(list) != 0 {
		t.Fatalf("list project across tenants: %+v", list)
	}

	// Change stores params whole, storage_gi only grows, bumps the
	// generation, and is tenant-scoped
	if err := repo.Change(ctx, org, env, "db", map[string]int{"storage_gi": 5}, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Find(ctx, org, env, "db"); got.StorageGi != 5 || got.Params["storage_gi"] != 5 || got.Generation != 2 || !got.UpdatedAt.After(got.CreatedAt.Add(-1)) {
		t.Fatalf("changed %+v", got)
	}
	if err := repo.Change(ctx, org, env, "db", map[string]int{"storage_gi": 3}, 3); errType(err) != errx.TypeNotFound {
		t.Fatalf("shrink: %v", err)
	}
	if err := repo.Change(ctx, org, env, "db", map[string]int{"max_connections": 3}, 3); errType(err) != errx.TypeNotFound {
		t.Fatalf("a sized resource losing its size: %v", err)
	}
	if err := repo.Change(ctx, org, envCompany, "bucket", map[string]int{"storage_gi": 4}, 2); errType(err) != errx.TypeNotFound {
		t.Fatalf("a resource with no size gaining one: %v", err)
	}
	if err := repo.Change(ctx, org, envCompany, "bucket", map[string]int{}, 2); err != nil {
		t.Fatalf("a resource with no size stays without one: %v", err)
	}
	if err := repo.Change(ctx, other, env, "db", map[string]int{"storage_gi": 9}, 9); err != nil {
		t.Fatal(err) // the other tenant's own "db"
	}
	if got, _ := repo.Find(ctx, org, env, "db"); got.StorageGi != 5 {
		t.Fatalf("another tenant changed mine: %+v", got)
	}
	if err := repo.Change(ctx, other, env2, "db", map[string]int{"storage_gi": 9}, 9); errType(err) != errx.TypeNotFound {
		t.Fatalf("change across tenants: %v", err)
	}

	// Forget only drops a deleting resource
	if _, ok, err := repo.Forget(ctx, org, env, "db"); err != nil || ok {
		t.Fatalf("forget an active resource: %v %v", ok, err)
	}
	if err := repo.MarkDeleting(ctx, other, env2, "db", 5); errType(err) != errx.TypeNotFound {
		t.Fatalf("mark across tenants: %v", err)
	}
	if err := repo.MarkDeleting(ctx, org, env, "db", 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Find(ctx, org, env, "db"); got.Status != resource.StatusDeleting || got.Generation != 3 {
		t.Fatalf("deleting %+v", got)
	}
	if err := repo.MarkDeleting(ctx, org, env, "db", 4); errType(err) != errx.TypeNotFound {
		t.Fatalf("mark twice: %v", err)
	}
	if err := repo.Change(ctx, org, env, "db", map[string]int{"storage_gi": 50}, 4); errType(err) != errx.TypeNotFound {
		t.Fatalf("change deleting: %v", err)
	}
	if n, _ := repo.Count(ctx, org, env); n != 2 {
		t.Fatalf("deleting still counts: %d", n)
	}
	if _, ok, err := repo.Forget(ctx, other, env2, "db"); err != nil || ok {
		t.Fatalf("forget across tenants: %v %v", ok, err)
	}
	if _, err := repo.Find(ctx, org, env, "db"); err != nil {
		t.Fatal(err)
	}
	gone, ok, err := repo.Forget(ctx, org, env, "db")
	if err != nil || !ok || gone.ID != db1.ID || gone.Recipe != "postgres-basico" || gone.Params["storage_gi"] != 5 {
		t.Fatalf("forget %+v %v %v", gone, ok, err)
	}
	if _, err := repo.Find(ctx, org, env, "db"); errType(err) != errx.TypeNotFound {
		t.Fatalf("forgotten: %v", err)
	}
	if _, ok, _ := repo.Forget(ctx, org, env, "db"); ok {
		t.Fatal("forgot twice")
	}
	// the name is free again, the other tenant's rows are intact
	if err := repo.Create(ctx, mk(org, env, "db", resourcekind.Postgres)); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Find(ctx, other, env, "db"); err != nil || got.StorageGi != 9 {
		t.Fatalf("other tenant %+v %v", got, err)
	}
}

// Migration 002 on a database that already holds resources: each is pinned
// to its kind's basic recipe, version 1, with its size as its parameters.
func TestMigrationPinsEveryResourceToItsBasicRecipe(t *testing.T) {
	all := resourcepg.Migrations()
	first, err := fs.ReadFile(all, "001_resources.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	db := pgtest.DB(t, migrate.Set{Owner: resourcepg.Schema, Files: fstest.MapFS{"001_resources.up.sql": {Data: first}}})
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	env := project.NewEnvironmentID()
	for i, x := range []struct {
		org  identity.OrganizationID
		name string
		kind resourcekind.Kind
		gi   int
	}{{org, "db", resourcekind.Postgres, 7}, {org, "cache", resourcekind.Redis, 1}, {other, "files", resourcekind.Files, 100}} {
		_, err := db.ExecContext(ctx, `INSERT INTO resource.resources (id, organization_id, project_id, environment_id, name, kind, storage_gi, generation, status, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 1, 'active', '6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55')`,
			resource.NewID(), x.org, project.NewID(), env, x.name, x.kind, x.gi)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	if err := migrate.Apply(ctx, db, migrate.Set{Owner: resourcepg.Schema, Files: all}); err != nil {
		t.Fatal(err)
	}
	repo := resourcepg.New(db)
	for o, want := range map[identity.OrganizationID]map[string]int{org: {"db": 7, "cache": 1}, other: {"files": 100}} {
		list, err := repo.List(ctx, o, env)
		if err != nil || len(list) != len(want) {
			t.Fatalf("list %+v %v", list, err)
		}
		for _, r := range list {
			if r.Recipe != string(r.Kind)+"-basico" || r.RecipeVersion != 1 || len(r.Params) != 1 || r.Params["storage_gi"] != want[r.Name] {
				t.Fatalf("pinned %+v", r)
			}
		}
	}
}
