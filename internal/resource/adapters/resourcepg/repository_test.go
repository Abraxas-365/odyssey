package resourcepg_test

import (
	"context"
	"errors"
	"testing"

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

func sized(r resource.Resource, gi int) resource.Resource { r.StorageGi = gi; return r }

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: resourcepg.Schema, Files: resourcepg.Migrations()})
	repo := resourcepg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	proj, env := project.NewID(), project.NewEnvironmentID()
	mk := func(org identity.OrganizationID, env project.EnvironmentID, name string, kind resourcekind.Kind) resource.Resource {
		return resource.Resource{ID: resource.NewID(), Organization: org, Project: proj, Environment: env, Name: name, Kind: kind,
			StorageGi: 2, Generation: 1, Status: resource.StatusActive, CreatedBy: user}
	}

	db1 := mk(org, env, "db", resourcekind.Postgres)
	if err := repo.Create(ctx, db1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Find(ctx, org, env, "db")
	if err != nil || got.ID != db1.ID || got.Kind != resourcekind.Postgres || got.StorageGi != 2 || got.Status != resource.StatusActive || got.CreatedAt.IsZero() {
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

	// Resize only grows, bumps the generation, and is tenant-scoped
	if err := repo.Resize(ctx, org, env, "db", 5, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Find(ctx, org, env, "db"); got.StorageGi != 5 || got.Generation != 2 || !got.UpdatedAt.After(got.CreatedAt.Add(-1)) {
		t.Fatalf("resized %+v", got)
	}
	if err := repo.Resize(ctx, org, env, "db", 3, 3); errType(err) != errx.TypeNotFound {
		t.Fatalf("shrink: %v", err)
	}
	if err := repo.Resize(ctx, other, env, "db", 9, 9); err != nil {
		t.Fatal(err) // the other tenant's own "db"
	}
	if got, _ := repo.Find(ctx, org, env, "db"); got.StorageGi != 5 {
		t.Fatalf("another tenant resized mine: %+v", got)
	}
	if err := repo.Resize(ctx, other, env2, "db", 9, 9); errType(err) != errx.TypeNotFound {
		t.Fatalf("resize across tenants: %v", err)
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
	if err := repo.Resize(ctx, org, env, "db", 50, 4); errType(err) != errx.TypeNotFound {
		t.Fatalf("resize deleting: %v", err)
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
	if err != nil || !ok || gone.ID != db1.ID {
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
