// Package buildpg is the Postgres implementation of build.Repository.
package buildpg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "build"

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

var _ build.Repository = (*Repository)(nil)

var messages = pgerr.Messages{
	Unique:     "the build or source already exists",
	ForeignKey: "source not found",
}

// ---- sources ----

const sourceColumns = `id, organization_id, project_id, name, size, sha256, files, unpacked_size, blob_key,
	uploaded_by, created_at, expired_at, git`

// sourceRow is a source with its git reference still JSON.
type sourceRow struct {
	build.Source
	RawGit []byte `db:"git"`
}

func (s sourceRow) source() (build.Source, error) {
	out := s.Source
	if s.RawGit != nil {
		var g build.GitRef
		if err := json.Unmarshal(s.RawGit, &g); err != nil {
			return build.Source{}, errx.Wrap(err, "decode source git", errx.TypeInternal)
		}
		out.Git = &g
	}
	return out, nil
}

func sources(rows []sourceRow) ([]build.Source, error) {
	out := make([]build.Source, 0, len(rows))
	for _, row := range rows {
		s, err := row.source()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *Repository) CreateSource(ctx context.Context, s build.Source) error {
	var git []byte // NULL when nil
	if s.Git != nil {
		var err error
		if git, err = json.Marshal(s.Git); err != nil {
			return errx.Wrap(err, "encode source git", errx.TypeInternal)
		}
	}
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO build.sources
		(id, organization_id, project_id, name, size, sha256, files, unpacked_size, blob_key, uploaded_by, git)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.ID, s.Organization, s.Project, s.Name, s.Size, s.SHA256, s.Files, s.UnpackedSize, s.Key, s.UploadedBy, git)
	return pgerr.MapWith(err, "create source", messages)
}

func (r *Repository) FindSource(ctx context.Context, organization identity.OrganizationID, id build.SourceID) (build.Source, error) {
	return r.findSource(ctx, "", organization, id)
}

func (r *Repository) LockSource(ctx context.Context, organization identity.OrganizationID, id build.SourceID) (build.Source, error) {
	return r.findSource(ctx, " FOR SHARE", organization, id)
}

func (r *Repository) findSource(ctx context.Context, lock string, organization identity.OrganizationID, id build.SourceID) (build.Source, error) {
	var row sourceRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+sourceColumns+` FROM build.sources
		WHERE organization_id = $1 AND id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return build.Source{}, errx.NotFound("source not found")
	}
	if err != nil {
		return build.Source{}, pgerr.MapWith(err, "find source", messages)
	}
	return row.source()
}

// LockExpirable spans organizations: it is the sweep's, and each source it
// returns is then expired within its own organization.
func (r *Repository) LockExpirable(ctx context.Context, cutoff, orphanCutoff time.Time, limit int) ([]build.Source, error) {
	var rows []sourceRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+sourceColumns+` FROM build.sources s
		WHERE s.expired_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM build.builds b WHERE b.source_id = s.id AND b.finished_at IS NULL)
		  AND (s.created_at < $1
		       OR (s.created_at < $2 AND NOT EXISTS (SELECT 1 FROM build.builds b WHERE b.source_id = s.id)))
		ORDER BY s.created_at
		LIMIT $3
		FOR UPDATE SKIP LOCKED`, cutoff, orphanCutoff, limit)
	if err != nil {
		return nil, pgerr.MapWith(err, "list expirable sources", messages)
	}
	return sources(rows)
}

func (r *Repository) Expire(ctx context.Context, organization identity.OrganizationID, id build.SourceID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE build.sources SET expired_at = now()
		WHERE organization_id = $1 AND id = $2 AND expired_at IS NULL`, organization, id)
	return affected(res, err, "expire source", "source not found or expired")
}

// ListUndeleted spans organizations (the sweep's).
func (r *Repository) ListUndeleted(ctx context.Context, limit int) ([]build.Source, error) {
	var rows []sourceRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+sourceColumns+` FROM build.sources
		WHERE expired_at IS NOT NULL AND bytes_deleted_at IS NULL ORDER BY expired_at LIMIT $1`, limit)
	if err != nil {
		return nil, pgerr.MapWith(err, "list undeleted sources", messages)
	}
	return sources(rows)
}

func (r *Repository) ForgetSourceBytes(ctx context.Context, organization identity.OrganizationID, id build.SourceID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE build.sources SET bytes_deleted_at = now()
		WHERE organization_id = $1 AND id = $2 AND expired_at IS NOT NULL AND bytes_deleted_at IS NULL`, organization, id)
	return pgerr.MapWith(err, "forget source bytes", messages)
}

// ExpireUnused locks the source first, so a build being created of it (which
// holds it FOR SHARE) is either seen or never created: LockSource then finds
// it expired.
func (r *Repository) ExpireUnused(ctx context.Context, organization identity.OrganizationID, id build.SourceID) (build.Source, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return build.Source{}, errx.Internal("ExpireUnused needs a transaction")
	}
	s, err := r.findSource(ctx, " FOR UPDATE", organization, id)
	if err != nil {
		return build.Source{}, err
	}
	var used bool
	err = txn.Q(ctx, r.db).GetContext(ctx, &used, `SELECT EXISTS (SELECT 1 FROM build.builds WHERE source_id = $1)`, id)
	if err != nil {
		return build.Source{}, pgerr.MapWith(err, "find builds of source", messages)
	}
	if used || s.Expired() {
		return build.Source{}, errx.Conflict("the source was built or already expired")
	}
	if err := r.Expire(ctx, organization, id); err != nil {
		return build.Source{}, err
	}
	return r.findSource(ctx, "", organization, id)
}

// LockQuota takes a transaction-scoped advisory lock per organization.
func (r *Repository) LockQuota(ctx context.Context, organization identity.OrganizationID) error {
	if _, ok := txn.Tx(ctx); !ok {
		return errx.Internal("LockQuota needs a transaction")
	}
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('build.quota:' || $1::text, 0))`,
		organization)
	return pgerr.MapWith(err, "lock source quota", messages)
}

// evictable: built, and no unfinished build needs it.
const evictable = `EXISTS (SELECT 1 FROM build.builds b WHERE b.source_id = s.id)
	AND NOT EXISTS (SELECT 1 FROM build.builds b WHERE b.source_id = s.id AND b.finished_at IS NULL)`

func (r *Repository) Usage(ctx context.Context, organization identity.OrganizationID) (build.Usage, error) {
	var u build.Usage
	err := txn.Q(ctx, r.db).GetContext(ctx, &u, `SELECT
		COALESCE(sum(s.size), 0)::bigint AS stored,
		COALESCE(sum(s.size) FILTER (WHERE `+evictable+`), 0)::bigint AS evictable
		FROM build.sources s WHERE s.organization_id = $1 AND s.expired_at IS NULL`, organization)
	return u, pgerr.MapWith(err, "measure sources", messages)
}

func (r *Repository) LockEvictable(ctx context.Context, organization identity.OrganizationID, limit int) ([]build.Source, error) {
	var rows []sourceRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+sourceColumns+` FROM build.sources s
		WHERE s.organization_id = $1 AND s.expired_at IS NULL AND `+evictable+`
		ORDER BY s.created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, organization, limit)
	if err != nil {
		return nil, pgerr.MapWith(err, "list evictable sources", messages)
	}
	return sources(rows)
}

// ---- builds ----

const buildColumns = `id, organization_id, project_id, environment_id, source_id, status, image, error_code, error_message,
	error_detail, run_id, requested_by, logs_key, logs_index, created_at, updated_at, started_at, finished_at`

type buildRow struct {
	build.Build
	ErrorCode    *string `db:"error_code"`
	ErrorMessage *string `db:"error_message"`
	ErrorDetail  string  `db:"error_detail"`
	RawLogsIndex []byte  `db:"logs_index"`
}

func (b buildRow) build() (build.Build, error) {
	out := b.Build
	if b.ErrorCode != nil {
		out.Error = &build.Failure{Code: *b.ErrorCode, Detail: b.ErrorDetail}
		if b.ErrorMessage != nil {
			out.Error.Message = *b.ErrorMessage
		}
	}
	if err := json.Unmarshal(b.RawLogsIndex, &out.LogsIndex); err != nil {
		return build.Build{}, errx.Wrap(err, "decode build log index", errx.TypeInternal)
	}
	return out, nil
}

func (r *Repository) CreateBuild(ctx context.Context, b build.Build) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO build.builds
		(id, organization_id, project_id, environment_id, source_id, status, run_id, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		b.ID, b.Organization, b.Project, b.Environment, b.Source, b.Status, b.Run, b.RequestedBy)
	return pgerr.MapWith(err, "create build", messages)
}

func (r *Repository) FindBuild(ctx context.Context, organization identity.OrganizationID, id build.ID) (build.Build, error) {
	return r.findBuild(ctx, "", organization, id)
}

func (r *Repository) FindBuildForUpdate(ctx context.Context, organization identity.OrganizationID, id build.ID) (build.Build, error) {
	return r.findBuild(ctx, " FOR UPDATE", organization, id)
}

func (r *Repository) findBuild(ctx context.Context, lock string, organization identity.OrganizationID, id build.ID) (build.Build, error) {
	var row buildRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+buildColumns+` FROM build.builds
		WHERE organization_id = $1 AND id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return build.Build{}, errx.NotFound("build not found")
	}
	if err != nil {
		return build.Build{}, pgerr.MapWith(err, "find build", messages)
	}
	return row.build()
}

func (r *Repository) MarkRunning(ctx context.Context, organization identity.OrganizationID, id build.ID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE build.builds SET status = 'running', started_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = 'queued'`, organization, id)
	return pgerr.MapWith(err, "mark build running", messages)
}

func (r *Repository) Finish(ctx context.Context, organization identity.OrganizationID, id build.ID, status build.Status, image string, failure *build.Failure) error {
	var code, msg *string
	var detail string
	if failure != nil {
		code, msg, detail = &failure.Code, &failure.Message, failure.Detail
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE build.builds
		SET status = $3, image = $4, error_code = $5, error_message = $6, error_detail = $7, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NULL`, organization, id, status, image, code, msg, detail)
	return affected(res, err, "finish build", "build not found or finished")
}

func (r *Repository) SetLogs(ctx context.Context, organization identity.OrganizationID, id build.ID, key string, index build.LogIndex) error {
	if index == nil {
		index = build.LogIndex{}
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return errx.Wrap(err, "encode build log index", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE build.builds SET logs_key = $3, logs_index = $4, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NOT NULL`, organization, id, key, raw)
	return affected(res, err, "set build logs", "build not found or unfinished")
}

func affected(res sql.Result, err error, op, notFound string) error {
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errx.Wrap(err, op, errx.TypeInternal)
	}
	if n == 0 {
		return errx.NotFound(notFound)
	}
	return nil
}
