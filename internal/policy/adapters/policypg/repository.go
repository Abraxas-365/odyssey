// Package policypg is the Postgres implementation of policy.Repository.
package policypg

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "policy"

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

var _ policy.Repository = (*Repository)(nil)

const columns = `id, organization_id, name, description, expression, effect, approvers, enabled, builtin,
	version, created_by, updated_by, created_at, updated_at`

func notFound() error { return errx.NotFound("rule not found") }

// order lists built-ins first, then the rules IT wrote, oldest first.
const order = ` ORDER BY (builtin IS NULL), created_at, id`

func (r *Repository) List(ctx context.Context, organization identity.OrganizationID) ([]policy.Rule, error) {
	rules := []policy.Rule{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rules,
		`SELECT `+columns+` FROM policy.rules WHERE organization_id = $1`+order, organization)
	return rules, pgerr.Map(err, "list policy rules")
}

func (r *Repository) Find(ctx context.Context, organization identity.OrganizationID, id policy.RuleID) (policy.Rule, error) {
	return r.find(ctx, organization, id, "")
}

func (r *Repository) FindForUpdate(ctx context.Context, organization identity.OrganizationID, id policy.RuleID) (policy.Rule, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return policy.Rule{}, errx.Internal("FindForUpdate needs a transaction")
	}
	return r.find(ctx, organization, id, " FOR UPDATE")
}

func (r *Repository) find(ctx context.Context, organization identity.OrganizationID, id policy.RuleID, lock string) (policy.Rule, error) {
	var rule policy.Rule
	err := txn.Q(ctx, r.db).GetContext(ctx, &rule,
		`SELECT `+columns+` FROM policy.rules WHERE organization_id = $1 AND id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Rule{}, notFound()
	}
	return rule, pgerr.Map(err, "find policy rule")
}

func (r *Repository) Count(ctx context.Context, organization identity.OrganizationID) (int, error) {
	var n int
	err := txn.Q(ctx, r.db).GetContext(ctx, &n, `SELECT count(*) FROM policy.rules WHERE organization_id = $1`, organization)
	return n, pgerr.Map(err, "count policy rules")
}

func (r *Repository) LockRules(ctx context.Context, organization identity.OrganizationID) error {
	if _, ok := txn.Tx(ctx); !ok {
		return errx.Internal("LockRules needs a transaction")
	}
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('policy/' || $1, 0))`, organization.String())
	return pgerr.Map(err, "lock policy rules")
}

func (r *Repository) Create(ctx context.Context, rule policy.Rule) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO policy.rules (id, organization_id, name, description, expression, effect, approvers, enabled,
		                           builtin, version, created_by, updated_by, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12, $12)`,
		rule.ID, rule.Organization, rule.Name, rule.Description, rule.Expression, rule.Effect, rule.Approvers,
		rule.Enabled, rule.Builtin, rule.Version, rule.CreatedBy, rule.CreatedAt)
	return pgerr.Map(err, "create policy rule")
}

func (r *Repository) Update(ctx context.Context, rule policy.Rule) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE policy.rules SET name = $3, description = $4, expression = $5, effect = $6, approvers = $7,
		        enabled = $8, version = version + 1, updated_by = $9, updated_at = $10
		 WHERE organization_id = $1 AND id = $2`,
		rule.Organization, rule.ID, rule.Name, rule.Description, rule.Expression, rule.Effect, rule.Approvers,
		rule.Enabled, rule.UpdatedBy, rule.UpdatedAt)
	return affected(res, err, "update policy rule")
}

func (r *Repository) Delete(ctx context.Context, organization identity.OrganizationID, id policy.RuleID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`DELETE FROM policy.rules WHERE organization_id = $1 AND id = $2 AND builtin IS NULL`, organization, id)
	return affected(res, err, "delete policy rule")
}

func (r *Repository) SeedBuiltins(ctx context.Context, rules []policy.Rule) (int, error) {
	added := 0
	for _, rule := range rules {
		res, err := txn.Q(ctx, r.db).ExecContext(ctx,
			`INSERT INTO policy.rules (id, organization_id, name, description, expression, effect, approvers, enabled,
			                           builtin, version, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $10)
			 ON CONFLICT (organization_id, builtin) WHERE builtin IS NOT NULL DO NOTHING`,
			rule.ID, rule.Organization, rule.Name, rule.Description, rule.Expression, rule.Effect, rule.Approvers,
			rule.Enabled, rule.Builtin, rule.CreatedAt)
		if err != nil {
			return added, pgerr.Map(err, "seed policy rules")
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return added, nil
}

func affected(res sql.Result, err error, op string) error {
	if err != nil {
		return pgerr.Map(err, op)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound()
	}
	return nil
}
