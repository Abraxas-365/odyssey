// Package resourcepg is the Postgres implementation of resource.Repository.
package resourcepg

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "resource"

//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations are this repository's *.up.sql files.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ resource.Repository = (*Repository)(nil)

var messages = pgerr.Messages{Unique: "a resource with this name already exists in the environment"}

const columns = `id, organization_id, project_id, environment_id, name, kind, storage_gi, generation, status, created_by, created_at, updated_at`

// LockEnvironment takes a transaction-scoped advisory lock on the
// environment, so two creations cannot both pass the count of resources.
// The lock is per organization and environment.
func (r *Repository) LockEnvironment(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || '/' || $2, 0))`, organization.String(), environment.String())
	return pgerr.Map(err, "lock environment resources")
}

func (r *Repository) Create(ctx context.Context, x resource.Resource) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO resource.resources (id, organization_id, project_id, environment_id, name, kind, storage_gi, generation, status, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		x.ID, x.Organization, x.Project, x.Environment, x.Name, x.Kind, x.StorageGi, x.Generation, x.Status, x.CreatedBy)
	return pgerr.MapWith(err, "create resource", messages)
}

func (r *Repository) Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (resource.Resource, error) {
	return r.find(ctx, organization, environment, name, "")
}

func (r *Repository) FindForUpdate(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (resource.Resource, error) {
	return r.find(ctx, organization, environment, name, " FOR UPDATE")
}

func (r *Repository) find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name, lock string) (resource.Resource, error) {
	var x resource.Resource
	err := txn.Q(ctx, r.db).GetContext(ctx, &x, `SELECT `+columns+` FROM resource.resources
		WHERE organization_id = $1 AND environment_id = $2 AND name = $3`+lock, organization, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.Resource{}, resource.NotFound()
	}
	if err != nil {
		return resource.Resource{}, pgerr.MapWith(err, "find resource", messages)
	}
	return x, nil
}

func (r *Repository) List(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]resource.Resource, error) {
	out := []resource.Resource{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+columns+` FROM resource.resources
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY created_at, name`, organization, environment)
	if err != nil {
		return nil, pgerr.Map(err, "list resources")
	}
	return out, nil
}

func (r *Repository) Count(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (int, error) {
	var n int
	err := txn.Q(ctx, r.db).GetContext(ctx, &n, `SELECT count(*) FROM resource.resources
		WHERE organization_id = $1 AND environment_id = $2`, organization, environment)
	return n, pgerr.Map(err, "count resources")
}

func (r *Repository) Resize(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string, storageGi int, generation int64) error {
	return r.update(ctx, `UPDATE resource.resources SET storage_gi = $4, generation = $5, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND name = $3 AND status = 'active' AND storage_gi < $4`,
		organization, environment, name, storageGi, generation)
}

func (r *Repository) MarkDeleting(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string, generation int64) error {
	return r.update(ctx, `UPDATE resource.resources SET status = 'deleting', generation = $4, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND name = $3 AND status = 'active'`,
		organization, environment, name, generation)
}

func (r *Repository) update(ctx context.Context, stmt string, args ...any) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, stmt, args...)
	if err != nil {
		return pgerr.MapWith(err, "update resource", messages)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return resource.NotFound()
	}
	return nil
}

func (r *Repository) Forget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (resource.Resource, bool, error) {
	var x resource.Resource
	err := txn.Q(ctx, r.db).GetContext(ctx, &x, `DELETE FROM resource.resources
		WHERE organization_id = $1 AND environment_id = $2 AND name = $3 AND status = 'deleting'
		RETURNING `+columns, organization, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.Resource{}, false, nil
	}
	if err != nil {
		return resource.Resource{}, false, pgerr.Map(err, "forget resource")
	}
	return x, true, nil
}
