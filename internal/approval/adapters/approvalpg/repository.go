// Package approvalpg is the Postgres implementation of approval.Repository.
package approvalpg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "approval"

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

var _ approval.Repository = (*Repository)(nil)

const columns = `id, organization_id, action, project_id, environment_id, summary, payload, requested_by, requested_system,
	requester_permissions, reason, rules, approvers, status, decided_by, decided_at, decision_note, expires_at,
	consumed_at, execution_error, run_id, run_step, key, created_at`

// open is what holds a key: a pending request, or any request of a workflow step.
const open = `(status = 'pending' OR run_id <> '')`

type row struct {
	ID                   approval.ID             `db:"id"`
	Organization         identity.OrganizationID `db:"organization_id"`
	Action               string                  `db:"action"`
	Project              project.ID              `db:"project_id"`
	Environment          project.EnvironmentID   `db:"environment_id"`
	Summary              string                  `db:"summary"`
	Payload              []byte                  `db:"payload"`
	RequestedBy          identity.UserID         `db:"requested_by"`
	RequestedSystem      string                  `db:"requested_system"`
	RequesterPermissions pq.StringArray          `db:"requester_permissions"`
	Reason               string                  `db:"reason"`
	Rules                []byte                  `db:"rules"`
	Approvers            pq.StringArray          `db:"approvers"`
	Status               approval.Status         `db:"status"`
	DecidedBy            *identity.UserID        `db:"decided_by"`
	DecidedAt            *time.Time              `db:"decided_at"`
	DecisionNote         string                  `db:"decision_note"`
	ExpiresAt            time.Time               `db:"expires_at"`
	ConsumedAt           *time.Time              `db:"consumed_at"`
	ExecutionError       string                  `db:"execution_error"`
	Run                  string                  `db:"run_id"`
	RunStep              string                  `db:"run_step"`
	Key                  string                  `db:"key"`
	CreatedAt            time.Time               `db:"created_at"`
}

func (r row) request() (approval.Request, error) {
	out := approval.Request{ID: r.ID, Organization: r.Organization, Action: r.Action, Project: r.Project,
		Environment: r.Environment, Summary: r.Summary, Payload: json.RawMessage(r.Payload), RequestedBy: r.RequestedBy,
		RequestedSystem: r.RequestedSystem, RequesterPermissions: []string(r.RequesterPermissions), Reason: r.Reason,
		Rules: []approval.Rule{}, Approvers: []string(r.Approvers), Status: r.Status, DecidedBy: r.DecidedBy,
		DecidedAt: r.DecidedAt, DecisionNote: r.DecisionNote, ExpiresAt: r.ExpiresAt, ConsumedAt: r.ConsumedAt,
		ExecutionError: r.ExecutionError, Run: r.Run, RunStep: r.RunStep, CreatedAt: r.CreatedAt, Key: r.Key}
	if len(r.Rules) > 0 {
		if err := json.Unmarshal(r.Rules, &out.Rules); err != nil {
			return approval.Request{}, errx.Wrap(err, "decode approval rules", errx.TypeInternal)
		}
	}
	return out, nil
}

func notFound() error { return errx.NotFound("approval not found") }

func (r *Repository) Insert(ctx context.Context, a approval.Request) (bool, error) {
	rules, err := json.Marshal(nonNilRules(a.Rules))
	if err != nil {
		return false, errx.Wrap(err, "encode approval rules", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO approval.requests (id, organization_id, action, project_id, environment_id, summary, payload,
		        requested_by, requested_system, requester_permissions, reason, rules, approvers, status, expires_at,
		        run_id, run_step, key, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'pending', $14, $15, $16, $17, $18)
		 ON CONFLICT (organization_id, key) WHERE status = 'pending' OR run_id <> '' DO NOTHING`,
		a.ID, a.Organization, a.Action, a.Project, a.Environment, a.Summary, []byte(a.Payload), a.RequestedBy,
		a.RequestedSystem, pq.Array(nonNil(a.RequesterPermissions)), a.Reason, rules, pq.Array(nonNil(a.Approvers)),
		a.ExpiresAt, a.Run, a.RunStep, a.Key, a.CreatedAt)
	if err != nil {
		return false, pgerr.Map(err, "create approval")
	}
	n, err := res.RowsAffected()
	return n == 1, pgerr.Map(err, "create approval")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilRules(s []approval.Rule) []approval.Rule {
	if s == nil {
		return []approval.Rule{}
	}
	return s
}

func (r *Repository) one(ctx context.Context, where, lock string, args ...any) (approval.Request, error) {
	var got row
	err := txn.Q(ctx, r.db).GetContext(ctx, &got, `SELECT `+columns+` FROM approval.requests WHERE `+where+` ORDER BY created_at DESC LIMIT 1`+lock, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Request{}, notFound()
	}
	if err != nil {
		return approval.Request{}, pgerr.Map(err, "find approval")
	}
	return got.request()
}

func (r *Repository) FindOpen(ctx context.Context, org identity.OrganizationID, key string) (approval.Request, error) {
	return r.one(ctx, `organization_id = $1 AND key = $2 AND `+open, "", org, key)
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id approval.ID) (approval.Request, error) {
	return r.one(ctx, `organization_id = $1 AND id = $2`, "", org, id)
}

func (r *Repository) FindForUpdate(ctx context.Context, org identity.OrganizationID, id approval.ID) (approval.Request, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return approval.Request{}, errx.Internal("FindForUpdate needs a transaction")
	}
	return r.one(ctx, `organization_id = $1 AND id = $2`, " FOR UPDATE", org, id)
}

func (r *Repository) FindByRun(ctx context.Context, org identity.OrganizationID, run string) (approval.Request, error) {
	return r.one(ctx, `organization_id = $1 AND run_id = $2`, "", org, run)
}

// filterSQL is the WHERE of List: the organization, what the viewer may see
// and the filter.
func filterSQL(org identity.OrganizationID, v approval.Viewer, f approval.Filter) (string, []any) {
	args := []any{org, v.User, pq.Array(nonNil(v.Permissions))}
	where := `organization_id = $1 AND (requested_by = $2 OR approvers && $3::text[])`
	switch {
	case f.Mine:
		where += ` AND requested_by = $2`
	case f.Decidable:
		where += ` AND requested_by IS DISTINCT FROM $2 AND approvers && $3::text[]`
	}
	if len(f.Status) > 0 {
		statuses := make([]string, len(f.Status))
		for i, s := range f.Status {
			statuses[i] = string(s)
		}
		args = append(args, pq.Array(statuses))
		where += ` AND status = ANY($` + strconv.Itoa(len(args)) + `::text[])`
	}
	return where, args
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, v approval.Viewer, f approval.Filter, page query.Pagination) (query.Paginated[approval.Request], error) {
	page = page.Normalize()
	where, args := filterSQL(org, v, f)
	var total int
	if err := txn.Q(ctx, r.db).GetContext(ctx, &total, `SELECT count(*) FROM approval.requests WHERE `+where, args...); err != nil {
		return query.Paginated[approval.Request]{}, pgerr.Map(err, "count approvals")
	}
	args = append(args, page.Limit, page.Offset)
	rows := []row{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+columns+` FROM approval.requests WHERE `+where+
		` ORDER BY created_at DESC, id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return query.Paginated[approval.Request]{}, pgerr.Map(err, "list approvals")
	}
	items := make([]approval.Request, len(rows))
	for i, got := range rows {
		if items[i], err = got.request(); err != nil {
			return query.Paginated[approval.Request]{}, err
		}
	}
	return query.NewPaginated(items, total, page), nil
}

func (r *Repository) ListExpired(ctx context.Context, now time.Time, limit int) ([]approval.Request, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return nil, errx.Internal("ListExpired needs a transaction")
	}
	rows := []row{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+columns+` FROM approval.requests
		WHERE status = 'pending' AND expires_at <= $1 ORDER BY expires_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, pgerr.Map(err, "list expired approvals")
	}
	out := make([]approval.Request, len(rows))
	for i, got := range rows {
		if out[i], err = got.request(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Repository) Decide(ctx context.Context, org identity.OrganizationID, id approval.ID, status approval.Status, by *identity.UserID, note string, at time.Time) (bool, error) {
	var who any
	if by != nil && !by.IsZero() {
		who = *by
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE approval.requests
		SET status = $3, decided_by = $4, decided_at = $5, decision_note = $6
		WHERE organization_id = $1 AND id = $2 AND status = 'pending'`, org, id, status, who, at, note)
	if err != nil {
		return false, pgerr.Map(err, "decide approval")
	}
	n, err := res.RowsAffected()
	return n == 1, pgerr.Map(err, "decide approval")
}

func (r *Repository) Consume(ctx context.Context, org identity.OrganizationID, id approval.ID, at time.Time) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE approval.requests SET consumed_at = $3
		WHERE organization_id = $1 AND id = $2 AND status = 'approved' AND consumed_at IS NULL`, org, id, at)
	if err != nil {
		return false, pgerr.Map(err, "consume approval")
	}
	n, err := res.RowsAffected()
	return n == 1, pgerr.Map(err, "consume approval")
}

func (r *Repository) SetExecutionError(ctx context.Context, org identity.OrganizationID, id approval.ID, message string) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE approval.requests SET execution_error = $3
		WHERE organization_id = $1 AND id = $2`, org, id, message)
	if err != nil {
		return pgerr.Map(err, "record approval error")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound()
	}
	return nil
}
