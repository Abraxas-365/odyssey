// Package auditpg is the Postgres implementation of audit.Repository.
package auditpg

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "audit"

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

var _ audit.Repository = (*Repository)(nil)

const columns = `id, organization_id, seq, type, version, actor_id, client, subject, summary, payload,
	correlation_id, causation_id, occurred_at, recorded_at, prev_hash, hash`

func (r *Repository) LockHead(ctx context.Context, organization identity.OrganizationID) (audit.Head, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return audit.Head{}, errx.Internal("LockHead needs a transaction")
	}
	q := txn.Q(ctx, r.db)
	// create the head on first use, then lock it (the insert also locks)
	if _, err := q.ExecContext(ctx,
		`INSERT INTO audit.heads (organization_id, seq, hash) VALUES ($1, 0, '') ON CONFLICT DO NOTHING`, organization); err != nil {
		return audit.Head{}, pgerr.Map(err, "create audit head")
	}
	var h audit.Head
	err := q.GetContext(ctx, &h, `SELECT seq, hash FROM audit.heads WHERE organization_id = $1 FOR UPDATE`, organization)
	return h, pgerr.Map(err, "lock audit head")
}

func (r *Repository) Head(ctx context.Context, organization identity.OrganizationID) (audit.Head, error) {
	var h audit.Head
	err := txn.Q(ctx, r.db).GetContext(ctx, &h, `SELECT seq, hash FROM audit.heads WHERE organization_id = $1`, organization)
	if errors.Is(err, sql.ErrNoRows) {
		return audit.Head{}, nil
	}
	return h, pgerr.Map(err, "read audit head")
}

func (r *Repository) Exists(ctx context.Context, organization identity.OrganizationID, id uuid.UUID) (bool, error) {
	var ok bool
	err := txn.Q(ctx, r.db).GetContext(ctx, &ok,
		`SELECT EXISTS (SELECT 1 FROM audit.entries WHERE organization_id = $1 AND id = $2)`, organization, id)
	return ok, pgerr.Map(err, "check audit entry")
}

func (r *Repository) Append(ctx context.Context, e audit.Entry) error {
	q := txn.Q(ctx, r.db)
	if _, err := q.ExecContext(ctx, `INSERT INTO audit.entries (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		e.ID, e.Organization, e.Seq, e.Type, e.Version, e.Actor, e.Client, e.Subject, e.Summary, []byte(e.Payload),
		e.CorrelationID, e.CausationID, e.OccurredAt, e.RecordedAt, e.PrevHash, e.Hash); err != nil {
		return pgerr.Map(err, "append audit entry")
	}
	res, err := q.ExecContext(ctx, `UPDATE audit.heads SET seq = $2, hash = $3 WHERE organization_id = $1 AND seq = $2 - 1`,
		e.Organization, e.Seq, e.Hash)
	if err != nil {
		return pgerr.Map(err, "move audit head")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errx.Internal("audit head moved concurrently")
	}
	return nil
}

func (r *Repository) List(ctx context.Context, organization identity.OrganizationID, f audit.Filter, page query.Pagination) (query.Paginated[audit.Entry], error) {
	where := []string{"organization_id = $1"}
	args := []any{organization}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(clause, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Subject != "" {
		add("subject = ?", f.Subject)
	}
	if len(f.Types) > 0 {
		add("type = ANY(?)", pq.Array(f.Types))
	}
	if !f.Actor.IsZero() {
		add("actor_id = ?", f.Actor)
	}
	if f.CorrelationID != "" {
		add("correlation_id = ?", f.CorrelationID)
	}
	if f.Project != uuid.Nil {
		add("payload->>'project_id' = ?", f.Project.String())
	}
	if m := f.Clients; m != nil {
		if m.Clients != nil {
			add("client = ANY(?)", pq.Array(m.Clients))
		}
		if m.Prefix != "" {
			add("starts_with(client, ?)", m.Prefix)
		}
		if len(m.Except) > 0 {
			add("NOT (client = ANY(?))", pq.Array(m.Except))
		}
		if m.ExceptPrefix != "" {
			add("NOT starts_with(client, ?)", m.ExceptPrefix)
		}
	}
	if !f.From.IsZero() {
		add("occurred_at >= ?", f.From)
	}
	if !f.Before.IsZero() {
		add("occurred_at < ?", f.Before)
	}
	if s := query.EscapeLike(page.Search); s != "" {
		add("summary ILIKE ?", s)
	}
	cond := strings.Join(where, " AND ")
	q := txn.Q(ctx, r.db)
	var total int
	if err := q.GetContext(ctx, &total, `SELECT count(*) FROM audit.entries WHERE `+cond, args...); err != nil {
		return query.Paginated[audit.Entry]{}, pgerr.Map(err, "count audit entries")
	}
	var items []audit.Entry
	args = append(args, page.Limit, page.Offset)
	n := len(args)
	if err := q.SelectContext(ctx, &items, `SELECT `+columns+` FROM audit.entries WHERE `+cond+
		` ORDER BY seq DESC LIMIT $`+strconv.Itoa(n-1)+` OFFSET $`+strconv.Itoa(n), args...); err != nil {
		return query.Paginated[audit.Entry]{}, pgerr.Map(err, "list audit entries")
	}
	return query.NewPaginated(items, total, page), nil
}

func (r *Repository) Tally(ctx context.Context, organization identity.OrganizationID, from time.Time) ([]audit.Tally, int64, error) {
	q := txn.Q(ctx, r.db)
	var tallies []audit.Tally
	if err := q.SelectContext(ctx, &tallies, `SELECT type, client, count(*) AS entries FROM audit.entries
		WHERE organization_id = $1 AND occurred_at >= $2 GROUP BY type, client ORDER BY type, client`, organization, from); err != nil {
		return nil, 0, pgerr.Map(err, "tally audit entries")
	}
	var people int64
	if err := q.GetContext(ctx, &people, `SELECT count(DISTINCT actor_id) FROM audit.entries
		WHERE organization_id = $1 AND occurred_at >= $2 AND actor_id IS NOT NULL`, organization, from); err != nil {
		return nil, 0, pgerr.Map(err, "count audit people")
	}
	return tallies, people, nil
}

func (r *Repository) People(ctx context.Context, organization identity.OrganizationID, limit int) ([]audit.Person, error) {
	people := []audit.Person{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &people, `SELECT actor_id, count(*) AS entries, max(occurred_at) AS last_at
		FROM audit.entries WHERE organization_id = $1 AND actor_id IS NOT NULL
		GROUP BY actor_id ORDER BY last_at DESC, actor_id LIMIT $2`, organization, limit)
	return people, pgerr.Map(err, "list audit people")
}

func (r *Repository) Walk(ctx context.Context, organization identity.OrganizationID, fn func(audit.Entry) error) error {
	const batch = 500
	after := int64(0)
	for {
		var items []audit.Entry
		err := txn.Q(ctx, r.db).SelectContext(ctx, &items, `SELECT `+columns+` FROM audit.entries
			WHERE organization_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, organization, after, batch)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && len(items) == 0) {
			return nil
		}
		if err != nil {
			return pgerr.Map(err, "walk audit entries")
		}
		for _, e := range items {
			if err := fn(e); err != nil {
				return err
			}
		}
		after = items[len(items)-1].Seq
	}
}
