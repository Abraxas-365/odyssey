// Package catalogpg is the Postgres implementation of catalog.Repository.
package catalogpg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "catalog"

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

var _ catalog.Repository = (*Repository)(nil)

const columns = `organization_id, recipe, version, active, tiers, defaults, limits, preapproved, teams, revision, updated_by,
	created_at, updated_at`

type row struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Recipe       string                  `db:"recipe"`
	Version      int                     `db:"version"`
	Active       bool                    `db:"active"`
	Tiers        pq.StringArray          `db:"tiers"`
	Defaults     pq.StringArray          `db:"defaults"`
	Limits       []byte                  `db:"limits"`
	Preapproved  []byte                  `db:"preapproved"`
	Teams        pq.StringArray          `db:"teams"`
	Revision     int                     `db:"revision"`
	UpdatedBy    identity.UserID         `db:"updated_by"`
	CreatedAt    time.Time               `db:"created_at"`
	UpdatedAt    time.Time               `db:"updated_at"`
}

func tiers(s []string) []project.Tier {
	out := make([]project.Tier, len(s))
	for i, t := range s {
		out[i] = project.Tier(t)
	}
	return out
}

func strs(ts []project.Tier) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}

// teams is never nil: pq writes a nil slice as NULL, which the column refuses.
func teams(ts []string) []string {
	if ts == nil {
		return []string{}
	}
	return ts
}

func (r row) offering() (catalog.Offering, error) {
	o := catalog.Offering{Organization: r.Organization, Recipe: r.Recipe, Revision: r.Revision, UpdatedBy: r.UpdatedBy,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		OfferingDraft: catalog.OfferingDraft{Version: r.Version, Active: r.Active, Tiers: tiers(r.Tiers), Defaults: tiers(r.Defaults),
			Teams: []string(r.Teams)}}
	if err := json.Unmarshal(r.Limits, &o.Limits); err != nil {
		return o, pgerr.Map(err, "decode catalog limits")
	}
	if err := json.Unmarshal(r.Preapproved, &o.Preapproved); err != nil {
		return o, pgerr.Map(err, "decode catalog pre-approved range")
	}
	o.OfferingDraft = o.OfferingDraft.Normalize()
	return o, nil
}

func ranges(m map[string]catalog.Range) ([]byte, error) {
	if m == nil {
		m = map[string]catalog.Range{}
	}
	return json.Marshal(m)
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID) ([]catalog.Offering, error) {
	var rows []row
	if err := txn.Q(ctx, r.db).SelectContext(ctx, &rows,
		`SELECT `+columns+` FROM catalog.offerings WHERE organization_id = $1 ORDER BY recipe`, org); err != nil {
		return nil, pgerr.Map(err, "list catalog offerings")
	}
	out := make([]catalog.Offering, 0, len(rows))
	for _, row := range rows {
		o, err := row.offering()
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, recipe string) (catalog.Offering, error) {
	var row row
	err := txn.Q(ctx, r.db).GetContext(ctx, &row,
		`SELECT `+columns+` FROM catalog.offerings WHERE organization_id = $1 AND recipe = $2`, org, recipe)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Offering{}, catalog.NotFound(recipe)
	}
	if err != nil {
		return catalog.Offering{}, pgerr.Map(err, "find catalog offering")
	}
	return row.offering()
}

// LockCatalog takes a transaction-scoped advisory lock on the
// organization's catalog.
func (r *Repository) LockCatalog(ctx context.Context, org identity.OrganizationID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('catalog/' || $1, 0))`, org.String())
	return pgerr.Map(err, "lock catalog")
}

func (r *Repository) Update(ctx context.Context, o catalog.Offering) error {
	limits, err := ranges(o.Limits)
	if err != nil {
		return pgerr.Map(err, "encode catalog limits")
	}
	pre, err := ranges(o.Preapproved)
	if err != nil {
		return pgerr.Map(err, "encode catalog pre-approved range")
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE catalog.offerings SET version = $3, active = $4, tiers = $5, defaults = $6, limits = $7, preapproved = $8,
		        teams = $11, revision = revision + 1, updated_by = $9, updated_at = $10
		 WHERE organization_id = $1 AND recipe = $2`,
		o.Organization, o.Recipe, o.Version, o.Active, pq.Array(strs(o.Tiers)), pq.Array(strs(o.Defaults)), limits, pre,
		o.UpdatedBy, o.UpdatedAt, pq.Array(teams(o.Teams)))
	if err != nil {
		return pgerr.Map(err, "update catalog offering")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return catalog.NotFound(o.Recipe)
	}
	return nil
}

func (r *Repository) SeedFactory(ctx context.Context, offerings []catalog.Offering) (int, error) {
	added := 0
	for _, o := range offerings {
		limits, err := ranges(o.Limits)
		if err != nil {
			return added, pgerr.Map(err, "encode catalog limits")
		}
		pre, err := ranges(o.Preapproved)
		if err != nil {
			return added, pgerr.Map(err, "encode catalog pre-approved range")
		}
		res, err := txn.Q(ctx, r.db).ExecContext(ctx,
			`INSERT INTO catalog.offerings (organization_id, recipe, version, active, tiers, defaults, limits, preapproved,
			                                teams, revision, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $10, 1, $9, $9)
			 ON CONFLICT (organization_id, recipe) DO NOTHING`,
			o.Organization, o.Recipe, o.Version, o.Active, pq.Array(strs(o.Tiers)), pq.Array(strs(o.Defaults)), limits, pre, o.CreatedAt,
			pq.Array(teams(o.Teams)))
		if err != nil {
			return added, pgerr.Map(err, "seed catalog offerings")
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return added, nil
}
