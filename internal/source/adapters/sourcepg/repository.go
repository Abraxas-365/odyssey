// Package sourcepg is the Postgres implementation of source.Repository.
package sourcepg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "source"

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

var _ source.Repository = (*Repository)(nil)

var messages = pgerr.Messages{
	Unique:     "this already exists",
	ForeignKey: "the installation is not linked",
}

func needTx(ctx context.Context, op string) error {
	if _, ok := txn.Tx(ctx); !ok {
		return errx.Internal(op + " needs a transaction")
	}
	return nil
}

// ---- installations ----

const installationColumns = `id, organization_id, external_id, account, account_type, all_repositories,
	suspended, linked_by, created_at, updated_at`

// UpsertInstallation links an installation, or refreshes it when the same
// organization has it. Another organization's → Conflict (CodeTaken).
func (r *Repository) UpsertInstallation(ctx context.Context, i source.Installation) (source.Installation, error) {
	var out source.Installation
	err := txn.Q(ctx, r.db).GetContext(ctx, &out,
		`INSERT INTO source.installations (id, organization_id, external_id, account, account_type, all_repositories, suspended, linked_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (external_id) DO UPDATE SET
		     account = EXCLUDED.account, account_type = EXCLUDED.account_type,
		     all_repositories = EXCLUDED.all_repositories, suspended = EXCLUDED.suspended,
		     linked_by = COALESCE(EXCLUDED.linked_by, installations.linked_by), updated_at = now()
		 WHERE installations.organization_id = EXCLUDED.organization_id
		 RETURNING `+installationColumns,
		i.ID, i.Organization, i.ExternalID, text.Clean(i.Account), i.AccountType, i.AllRepositories, i.Suspended, i.LinkedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Installation{}, errx.Conflict(source.Message(source.CodeTaken)).WithCode(source.CodeTaken)
	}
	if err != nil {
		return source.Installation{}, pgerr.MapWith(err, "upsert installation", messages)
	}
	return out, nil
}

func (r *Repository) FindInstallation(ctx context.Context, organization identity.OrganizationID, id source.InstallationID) (source.Installation, error) {
	return r.findInstallation(ctx, `organization_id = $1 AND id = $2`, organization, id)
}

func (r *Repository) FindInstallationByExternal(ctx context.Context, external int64) (source.Installation, error) {
	return r.findInstallation(ctx, `external_id = $1`, external)
}

func (r *Repository) findInstallation(ctx context.Context, where string, args ...any) (source.Installation, error) {
	var out source.Installation
	err := txn.Q(ctx, r.db).GetContext(ctx, &out, `SELECT `+installationColumns+` FROM source.installations WHERE `+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Installation{}, errx.NotFound("installation not found")
	}
	return out, pgerr.MapWith(err, "find installation", messages)
}

func (r *Repository) ListInstallations(ctx context.Context, organization identity.OrganizationID) ([]source.Installation, error) {
	out := []source.Installation{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+installationColumns+`
		FROM source.installations WHERE organization_id = $1 ORDER BY lower(account), id`, organization)
	return out, pgerr.MapWith(err, "list installations", messages)
}

// DeleteInstallation deletes it; its connections must be gone first
// (DeleteConnectionsOfInstallation in the same transaction).
func (r *Repository) DeleteInstallation(ctx context.Context, organization identity.OrganizationID, id source.InstallationID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`DELETE FROM source.installations WHERE organization_id = $1 AND id = $2`, organization, id)
	return affected(res, err, "delete installation", "installation not found")
}

// ---- connections ----

const connectionColumns = `id, organization_id, project_id, installation_id, repository_id, repository, branch, root,
	config, last_commit, etag, connected_by, created_at, updated_at`

type connectionRow struct {
	source.Connection
	RawConfig []byte `db:"config"`
}

func (c connectionRow) connection() (source.Connection, error) {
	out := c.Connection
	if err := json.Unmarshal(c.RawConfig, &out.Config); err != nil {
		return source.Connection{}, errx.Wrap(err, "decode connection config", errx.TypeInternal)
	}
	return out, nil
}

func connections(rows []connectionRow) ([]source.Connection, error) {
	out := make([]source.Connection, 0, len(rows))
	for _, row := range rows {
		c, err := row.connection()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *Repository) UpsertConnection(ctx context.Context, c source.Connection) (source.Connection, error) {
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return source.Connection{}, errx.Wrap(err, "marshal connection config", errx.TypeInternal)
	}
	var row connectionRow
	err = txn.Q(ctx, r.db).GetContext(ctx, &row,
		`INSERT INTO source.connections (id, organization_id, project_id, installation_id, repository_id, repository, branch, root, config, connected_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (organization_id, project_id) DO UPDATE SET
		     installation_id = EXCLUDED.installation_id, repository_id = EXCLUDED.repository_id,
		     repository = EXCLUDED.repository, branch = EXCLUDED.branch, root = EXCLUDED.root,
		     config = EXCLUDED.config, connected_by = EXCLUDED.connected_by, updated_at = now(),
		     last_commit = CASE WHEN connections.repository_id = EXCLUDED.repository_id AND connections.branch = EXCLUDED.branch
		                        THEN connections.last_commit ELSE '' END,
		     etag = CASE WHEN connections.repository_id = EXCLUDED.repository_id AND connections.branch = EXCLUDED.branch
		                 THEN connections.etag ELSE '' END,
		     checked_at = NULL
		 RETURNING `+connectionColumns,
		c.ID, c.Organization, c.Project, c.Installation, c.RepositoryID, text.Clean(c.Repository), c.Branch, c.Root, cfg, c.ConnectedBy)
	if err != nil {
		return source.Connection{}, pgerr.MapWith(err, "upsert connection", messages)
	}
	return row.connection()
}

func (r *Repository) FindConnection(ctx context.Context, organization identity.OrganizationID, id project.ID) (source.Connection, error) {
	return r.findConnection(ctx, "", organization, id)
}

func (r *Repository) LockConnection(ctx context.Context, organization identity.OrganizationID, id project.ID) (source.Connection, error) {
	if err := needTx(ctx, "LockConnection"); err != nil {
		return source.Connection{}, err
	}
	return r.findConnection(ctx, " FOR UPDATE", organization, id)
}

func (r *Repository) findConnection(ctx context.Context, lock string, organization identity.OrganizationID, id project.ID) (source.Connection, error) {
	var row connectionRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+connectionColumns+`
		FROM source.connections WHERE organization_id = $1 AND project_id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Connection{}, errx.NotFound("the app has no repository connected")
	}
	if err != nil {
		return source.Connection{}, pgerr.MapWith(err, "find connection", messages)
	}
	return row.connection()
}

func (r *Repository) ListConnectionsByRepository(ctx context.Context, installation source.InstallationID, repository int64) ([]source.Connection, error) {
	var rows []connectionRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+connectionColumns+`
		FROM source.connections WHERE installation_id = $1 AND repository_id = $2 ORDER BY id`, installation, repository)
	if err != nil {
		return nil, pgerr.MapWith(err, "list connections", messages)
	}
	return connections(rows)
}

func (r *Repository) ListConnectionsToReconcile(ctx context.Context, before time.Time, limit int) ([]source.Connection, error) {
	var rows []connectionRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+prefixed("c", connectionColumns)+`
		FROM source.connections c JOIN source.installations i ON i.id = c.installation_id
		WHERE (c.checked_at IS NULL OR c.checked_at < $1) AND NOT i.suspended
		ORDER BY c.checked_at NULLS FIRST, c.id LIMIT $2`, before, limit)
	if err != nil {
		return nil, pgerr.MapWith(err, "list connections to reconcile", messages)
	}
	return connections(rows)
}

func (r *Repository) DeleteConnection(ctx context.Context, organization identity.OrganizationID, id project.ID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`DELETE FROM source.connections WHERE organization_id = $1 AND project_id = $2`, organization, id)
	return affected(res, err, "delete connection", "the app has no repository connected")
}

func (r *Repository) DeleteConnectionsOfInstallation(ctx context.Context, organization identity.OrganizationID, installation source.InstallationID, repositories []int64) ([]source.Connection, error) {
	where, args := `organization_id = $1 AND installation_id = $2`, []any{organization, installation}
	if repositories != nil {
		args = append(args, pq.Array(repositories))
		where += ` AND repository_id = ANY($3)`
	}
	var rows []connectionRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `DELETE FROM source.connections WHERE `+where+` RETURNING `+connectionColumns, args...)
	if err != nil {
		return nil, pgerr.MapWith(err, "delete connections", messages)
	}
	return connections(rows)
}

func (r *Repository) ObserveHead(ctx context.Context, id source.ConnectionID, etag string, at time.Time) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE source.connections SET etag = $2, checked_at = $3 WHERE id = $1`, id, etag, at)
	return pgerr.MapWith(err, "observe head", messages)
}

// ---- pushes ----

const pushColumns = `id, organization_id, project_id, connection_id, git, trigger, status, deployment_id, error,
	requested_by, requested_client, rebuild, created_at, updated_at`

type pushRow struct {
	source.Push
	RawGit   []byte `db:"git"`
	RawError []byte `db:"error"`
}

func (p pushRow) push() (source.Push, error) {
	out := p.Push
	if err := json.Unmarshal(p.RawGit, &out.Git); err != nil {
		return source.Push{}, errx.Wrap(err, "decode push git", errx.TypeInternal)
	}
	if len(p.RawError) > 0 {
		out.Error = &source.Failure{}
		if err := json.Unmarshal(p.RawError, out.Error); err != nil {
			return source.Push{}, errx.Wrap(err, "decode push error", errx.TypeInternal)
		}
	}
	return out, nil
}

func pushes(rows []pushRow) ([]source.Push, error) {
	out := make([]source.Push, 0, len(rows))
	for _, row := range rows {
		p, err := row.push()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func pushError(p source.Push) ([]byte, error) {
	if p.Error == nil {
		return nil, nil
	}
	b, err := json.Marshal(p.Error)
	if err != nil {
		return nil, errx.Wrap(err, "marshal push error", errx.TypeInternal)
	}
	return b, nil
}

func (r *Repository) CreatePush(ctx context.Context, p source.Push) error {
	git, err := json.Marshal(p.Git)
	if err != nil {
		return errx.Wrap(err, "marshal push git", errx.TypeInternal)
	}
	perr, err := pushError(p)
	if err != nil {
		return err
	}
	_, err = txn.Q(ctx, r.db).ExecContext(ctx,
		`WITH push AS (
		     INSERT INTO source.pushes (id, organization_id, project_id, connection_id, git, trigger, status, deployment_id, error, requested_by, requested_client, rebuild)
		     VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $12, $13)
		 )
		 UPDATE source.connections SET last_commit = $11
		 WHERE organization_id = $2 AND id = $4`,
		p.ID, p.Organization, p.Project, p.Connection, git, p.Trigger, p.Status, p.Deployment, perr, p.RequestedBy, p.Git.Commit, p.RequestedClient, p.Rebuild)
	return pgerr.MapWith(err, "create push", messages)
}

func (r *Repository) FindPush(ctx context.Context, organization identity.OrganizationID, id source.PushID) (source.Push, error) {
	return r.findPush(ctx, `organization_id = $1 AND id = $2`, "", organization, id)
}

func (r *Repository) FindPushByDeployment(ctx context.Context, organization identity.OrganizationID, deployment release.DeploymentID) (source.Push, error) {
	return r.findPush(ctx, `organization_id = $1 AND deployment_id = $2`, "", organization, deployment)
}

func (r *Repository) LockPush(ctx context.Context, organization identity.OrganizationID, id source.PushID) (source.Push, error) {
	if err := needTx(ctx, "LockPush"); err != nil {
		return source.Push{}, err
	}
	return r.findPush(ctx, `organization_id = $1 AND id = $2`, " FOR UPDATE", organization, id)
}

func (r *Repository) findPush(ctx context.Context, where, lock string, args ...any) (source.Push, error) {
	var row pushRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+pushColumns+` FROM source.pushes WHERE `+where+lock, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return source.Push{}, errx.NotFound("push not found")
	}
	if err != nil {
		return source.Push{}, pgerr.MapWith(err, "find push", messages)
	}
	return row.push()
}

func (r *Repository) ListPushes(ctx context.Context, organization identity.OrganizationID, id project.ID, filter source.PushFilter, page query.Pagination) (query.Paginated[source.Push], error) {
	where := []string{"organization_id = $1", "project_id = $2"}
	args := []any{organization, id}
	if len(filter.Status) > 0 {
		values := make([]string, len(filter.Status))
		for i, s := range filter.Status {
			values[i] = string(s)
		}
		args = append(args, pq.Array(values))
		where = append(where, "status = ANY($"+strconv.Itoa(len(args))+")")
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := txn.Q(ctx, r.db).GetContext(ctx, &total, `SELECT count(*) FROM source.pushes WHERE `+clause, args...); err != nil {
		return query.Paginated[source.Push]{}, pgerr.MapWith(err, "count pushes", messages)
	}
	args = append(args, page.Limit, page.Offset)
	var rows []pushRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+pushColumns+` FROM source.pushes WHERE `+clause+
		` ORDER BY seq DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return query.Paginated[source.Push]{}, pgerr.MapWith(err, "list pushes", messages)
	}
	items, err := pushes(rows)
	if err != nil {
		return query.Paginated[source.Push]{}, err
	}
	return query.NewPaginated(items, total, page), nil
}

func (r *Repository) SkipPushes(ctx context.Context, organization identity.OrganizationID, connection source.ConnectionID, newer source.PushID) ([]source.Push, error) {
	var rows []pushRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows,
		`UPDATE source.pushes SET status = 'skipped', updated_at = now()
		 WHERE organization_id = $1 AND connection_id = $2 AND status IN ('queued', 'importing')
		   AND seq < (SELECT seq FROM source.pushes WHERE organization_id = $1 AND id = $3)
		 RETURNING `+pushColumns, organization, connection, newer)
	if err != nil {
		return nil, pgerr.MapWith(err, "skip pushes", messages)
	}
	return pushes(rows)
}

func (r *Repository) UpdatePush(ctx context.Context, p source.Push) error {
	perr, err := pushError(p)
	if err != nil {
		return err
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE source.pushes SET status = $3, deployment_id = $4, error = $5, updated_at = now()
		 WHERE organization_id = $1 AND id = $2`,
		p.Organization, p.ID, p.Status, p.Deployment, perr)
	return affected(res, err, "update push", "push not found")
}

// ---- deliveries ----

// ObserveDelivery records a delivery id (false when seen before) and, in the
// same statement, forgets up to 1000 ids seen before forgetBefore. The id
// itself is never among the forgotten.
func (r *Repository) ObserveDelivery(ctx context.Context, delivery string, forgetBefore time.Time) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`WITH forgotten AS (
		     DELETE FROM source.deliveries WHERE id IN (
		         SELECT id FROM source.deliveries WHERE seen_at < $2 AND id <> $1 ORDER BY seen_at LIMIT 1000)
		 )
		 INSERT INTO source.deliveries (id) VALUES ($1) ON CONFLICT DO NOTHING`, delivery, forgetBefore)
	if err != nil {
		return false, pgerr.MapWith(err, "observe delivery", messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, pgerr.MapWith(err, "observe delivery", messages)
	}
	return n == 1, nil
}

// ---- helpers ----

func affected(res sql.Result, err error, op, missing string) error {
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	if n == 0 {
		return errx.NotFound(missing)
	}
	return nil
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
