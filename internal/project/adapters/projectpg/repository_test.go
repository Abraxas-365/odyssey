package projectpg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectpg"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

func errType(err error) errx.Type {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Type
	}
	return ""
}

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: projectpg.Schema, Files: projectpg.Migrations()})
	repo := projectpg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

	id := project.NewID()
	if err := repo.Create(ctx, org, id, project.Create{Name: " Portal ", Slug: "portal", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Find(ctx, org, id)
	if err != nil || got.Name != "Portal" || got.Status != project.StatusActive || got.UpdatedBy != user {
		t.Fatalf("find %+v %v", got, err)
	}

	// same slug: conflict in the org, fine in another org
	if err := repo.Create(ctx, org, project.NewID(), project.Create{Name: "x", Slug: "portal", CreatedBy: user}); errType(err) != errx.TypeConflict {
		t.Fatalf("duplicate slug: %v", err)
	}
	if err := repo.Create(ctx, other, project.NewID(), project.Create{Name: "x", Slug: "portal", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}

	// tenant isolation on every statement
	name := "Hack"
	if _, err := repo.Find(ctx, other, id); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant find: %v", err)
	}
	if err := repo.Update(ctx, other, id, project.Update{Name: &name, UpdatedBy: user}); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant update: %v", err)
	}
	if err := repo.Archive(ctx, other, id, project.Archive{ArchivedBy: user}); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant archive: %v", err)
	}

	// FindForUpdate needs a transaction
	if _, err := repo.FindForUpdate(ctx, org, id); err == nil {
		t.Fatal("FindForUpdate outside a transaction")
	}
	err = txn.Postgres{DB: db}.Do(ctx, func(ctx context.Context) error {
		_, err := repo.FindForUpdate(ctx, org, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// list: search, status filter, pagination
	if err := repo.Create(ctx, org, project.NewID(), project.Create{Name: "Cotizador", Slug: "cotizador", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	active := project.Filter{Status: []project.Status{project.StatusActive}}
	page, err := repo.List(ctx, org, active, query.Pagination{Limit: 1})
	if err != nil || page.Page.Total != 2 || len(page.Items) != 1 || page.Items[0].Name != "Cotizador" {
		t.Fatalf("list %+v %v", page, err)
	}
	page, _ = repo.List(ctx, org, active, query.Pagination{Limit: 10, Search: "port"})
	if page.Page.Total != 1 {
		t.Fatalf("search %+v", page)
	}

	// update + archive
	if err := repo.Update(ctx, org, id, project.Update{Name: &name, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Archive(ctx, org, id, project.Archive{ArchivedBy: user}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.Find(ctx, org, id)
	if got.Name != "Hack" || !got.Archived() || got.ArchivedAt == nil {
		t.Fatalf("after archive %+v", got)
	}
	page, _ = repo.List(ctx, org, active, query.Pagination{Limit: 10})
	if page.Page.Total != 1 {
		t.Fatalf("archived still listed as active: %+v", page.Page)
	}
}

func TestEnvironments(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: projectpg.Schema, Files: projectpg.Migrations()})
	repo := projectpg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	id := project.NewID()
	if err := repo.Create(ctx, org, id, project.Create{Name: "Portal", Slug: "portal", CreatedBy: user}); err != nil {
		t.Fatal(err)
	}
	prod := project.Environment{ID: project.NewEnvironmentID(), Organization: org, Project: id, Slug: "production", Kind: project.KindProduction}
	stg := project.Environment{ID: project.NewEnvironmentID(), Organization: org, Project: id, Slug: "staging", Kind: project.KindStaging}
	for _, e := range []project.Environment{stg, prod} {
		if err := repo.CreateEnvironment(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	dup := prod
	dup.ID, dup.Slug = project.NewEnvironmentID(), "prod-2"
	if err := repo.CreateEnvironment(ctx, dup); errType(err) != errx.TypeConflict {
		t.Fatalf("second production: %v", err)
	}
	envs, err := repo.ListEnvironments(ctx, org, id, project.EnvironmentFilter{})
	if err != nil || len(envs) != 2 || envs[0].ID != prod.ID || envs[1].ID != stg.ID {
		t.Fatalf("list %+v %v (production first)", envs, err)
	}
	envs, err = repo.ListEnvironments(ctx, org, id, project.EnvironmentFilter{Kind: []project.EnvironmentKind{project.KindStaging}})
	if err != nil || len(envs) != 1 || envs[0].ID != stg.ID {
		t.Fatalf("filtered %+v %v", envs, err)
	}
	if e, err := repo.FindEnvironmentBySlug(ctx, org, id, "production"); err != nil || e.ID != prod.ID || e.CreatedAt.IsZero() {
		t.Fatalf("by slug %+v %v", e, err)
	}
	if _, err := repo.FindEnvironment(ctx, other, id, prod.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant: %v", err)
	}
	if _, err := repo.FindEnvironment(ctx, org, project.NewID(), prod.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("other project: %v", err)
	}
	if envs, err := repo.ListEnvironments(ctx, other, id, project.EnvironmentFilter{}); err != nil || len(envs) != 0 {
		t.Fatalf("cross-tenant list %+v %v", envs, err)
	}
}
