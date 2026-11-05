// Package aipg is the Postgres implementation of ai.Repository.
package aipg

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "ai"

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

var _ ai.Repository = (*Repository)(nil)

var messages = pgerr.Messages{Unique: "the environment already has an active AI key"}

const columns = `id, organization_id, project_id, environment_id, provider_key_id, name, status, created_by, created_at, revoke_error, revoke_attempts, next_retry_at`

// LockEnvironment takes a transaction-scoped advisory lock on the
// environment, so two changes of its AI cannot interleave. It is another key
// than the resources' and release's locks.
func (r *Repository) LockEnvironment(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('ai/' || $1 || '/' || $2, 0))`, organization.String(), environment.String())
	return pgerr.Map(err, "lock environment ai")
}

func (r *Repository) Create(ctx context.Context, k ai.Key) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO ai.keys (id, organization_id, project_id, environment_id, provider_key_id, name, status, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		k.ID, k.Organization, k.Project, k.Environment, k.ProviderKeyID, k.Name, k.Status, k.CreatedBy)
	return pgerr.MapWith(err, "create ai key", messages)
}

func (r *Repository) Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id ai.KeyID) (ai.Key, error) {
	var k ai.Key
	err := txn.Q(ctx, r.db).GetContext(ctx, &k, `SELECT `+columns+` FROM ai.keys
		WHERE organization_id = $1 AND environment_id = $2 AND id = $3`, organization, environment, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Key{}, ai.NotFound()
	}
	if err != nil {
		return ai.Key{}, pgerr.Map(err, "find ai key")
	}
	return k, nil
}

func (r *Repository) List(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]ai.Key, error) {
	out := []ai.Key{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+columns+` FROM ai.keys
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY created_at, id`, organization, environment)
	if err != nil {
		return nil, pgerr.Map(err, "list ai keys")
	}
	return out, nil
}

func (r *Repository) Activate(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id ai.KeyID, providerKeyID string) (bool, bool, error) {
	k, err := r.Find(ctx, organization, environment, id)
	if err != nil {
		return false, false, err
	}
	if k.Status == ai.StatusActive {
		return false, false, nil
	}
	if k.Status != ai.StatusCreating {
		return false, false, ai.NotFound()
	}
	// The old key steps down first: the table allows one active key.
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE ai.keys SET status = 'revoking'
		WHERE organization_id = $1 AND environment_id = $2 AND status = 'active'`, organization, environment)
	if err != nil {
		return false, false, pgerr.Map(err, "retire ai key")
	}
	n, _ := res.RowsAffected()
	if _, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE ai.keys SET status = 'active', provider_key_id = $4
		WHERE organization_id = $1 AND environment_id = $2 AND id = $3`, organization, environment, id, providerKeyID); err != nil {
		return false, false, pgerr.MapWith(err, "activate ai key", messages)
	}
	return true, n > 0, nil
}

func (r *Repository) MarkRevoking(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]ai.Key, error) {
	out := []ai.Key{}
	// A person asking starts the backoff over.
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `UPDATE ai.keys SET status = 'revoking', revoke_error = '', revoke_attempts = 0, next_retry_at = NULL
		WHERE organization_id = $1 AND environment_id = $2 RETURNING `+columns, organization, environment)
	if err != nil {
		return nil, pgerr.Map(err, "mark ai keys revoking")
	}
	return out, nil
}

// failed is what a key that gave up becomes: one more attempt and the next try
// after min(5 minutes × 2^(attempts-1), 6 hours). The SET expressions read the
// row as it was, so the exponent is the number of earlier failures (capped
// before power() so it cannot overflow).
const failed = `status = 'revoke_failed', revoke_error = $4, revoke_attempts = revoke_attempts + 1,
	next_retry_at = now() + LEAST(interval '5 minutes' * power(2, LEAST(revoke_attempts, 16)), interval '6 hours')`

func (r *Repository) FailRevocation(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, ids []ai.KeyID, reason string) ([]ai.Key, error) {
	raw := make([]string, len(ids))
	for i, id := range ids {
		raw[i] = id.String()
	}
	out := []ai.Key{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `UPDATE ai.keys SET `+failed+`
		WHERE organization_id = $1 AND environment_id = $2 AND status = 'revoking' AND id = ANY($3::uuid[]) RETURNING `+columns,
		organization, environment, pq.Array(raw), reason)
	if err != nil {
		return nil, pgerr.Map(err, "fail ai key revocation")
	}
	return out, nil
}

func (r *Repository) FailKey(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id ai.KeyID, reason string) (ai.Key, bool, error) {
	var k ai.Key
	err := txn.Q(ctx, r.db).GetContext(ctx, &k, `UPDATE ai.keys SET `+failed+`
		WHERE organization_id = $1 AND environment_id = $2 AND id = $3 AND status IN ('creating', 'revoking') RETURNING `+columns,
		organization, environment, id, reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Key{}, false, nil
	}
	if err != nil {
		return ai.Key{}, false, pgerr.Map(err, "fail ai key")
	}
	return k, true, nil
}

// due is what the sweep may take: never waited, or the wait is over.
const due = `status = 'revoke_failed' AND (next_retry_at IS NULL OR next_retry_at <= now())`

func (r *Repository) RetryRevocation(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]ai.Key, error) {
	out := []ai.Key{}
	// The attempts and the wait stay: another failure waits longer.
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `UPDATE ai.keys SET status = 'revoking', revoke_error = ''
		WHERE organization_id = $1 AND environment_id = $2 AND `+due+` RETURNING `+columns, organization, environment)
	if err != nil {
		return nil, pgerr.Map(err, "retry ai key revocation")
	}
	return out, nil
}

// ListRevokeFailed spans organizations: it is the sweep's.
func (r *Repository) ListRevokeFailed(ctx context.Context, limit int) ([]ai.Key, error) {
	out := []ai.Key{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+columns+` FROM (
			SELECT DISTINCT ON (environment_id) `+columns+` FROM ai.keys
			WHERE `+due+` ORDER BY environment_id, next_retry_at NULLS FIRST, created_at, id) k
		ORDER BY next_retry_at NULLS FIRST, created_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, pgerr.Map(err, "list revoke-failed ai keys")
	}
	return out, nil
}

func (r *Repository) Delete(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id ai.KeyID) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `DELETE FROM ai.keys
		WHERE organization_id = $1 AND environment_id = $2 AND id = $3`, organization, environment, id)
	if err != nil {
		return false, pgerr.Map(err, "delete ai key")
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (r *Repository) DeleteCreating(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id ai.KeyID) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `DELETE FROM ai.keys
		WHERE organization_id = $1 AND environment_id = $2 AND id = $3 AND status = 'creating'`, organization, environment, id)
	if err != nil {
		return false, pgerr.Map(err, "delete creating ai key")
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
