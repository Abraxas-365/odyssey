// Package releasepg is the Postgres implementation of release.Repository.
package releasepg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "release"

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

var _ release.Repository = (*Repository)(nil)

var messages = pgerr.Messages{
	Unique:     "another deployment is in progress in this environment",
	ForeignKey: "release not found",
}

// ---- releases ----

type releaseRow struct {
	release.Release
	RawConfig []byte `db:"config"`
}

func (r *Repository) CreateRelease(ctx context.Context, rel release.Release) error {
	cfg, err := json.Marshal(rel.Config)
	if err != nil {
		return errx.Wrap(err, "marshal release config", errx.TypeInternal)
	}
	_, err = txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO release.releases (id, organization_id, project_id, image, config, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
		rel.ID, rel.Organization, rel.Project, rel.Image, cfg, rel.CreatedBy)
	return pgerr.MapWith(err, "create release", messages)
}

func (r *Repository) FindRelease(ctx context.Context, organization identity.OrganizationID, id release.ID) (release.Release, error) {
	var row releaseRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT id, organization_id, project_id, image, config, created_by, created_at
		FROM release.releases WHERE organization_id = $1 AND id = $2`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Release{}, errx.NotFound("release not found")
	}
	if err != nil {
		return release.Release{}, pgerr.MapWith(err, "find release", messages)
	}
	if err := json.Unmarshal(row.RawConfig, &row.Release.Config); err != nil {
		return release.Release{}, errx.Wrap(err, "decode release config", errx.TypeInternal)
	}
	return row.Release, nil
}

// ---- deployments ----

const deploymentColumns = `id, organization_id, project_id, environment_id, release_id, image, build_id, source_id, source_name,
	source_size, source_git, config, status, reason, step, revision, previous_release_id, rollback_of, error_code, error_message, error_detail,
	run_id, requested_by, created_at, updated_at, finished_at`

type deploymentRow struct {
	release.Deployment
	SourceID     *build.SourceID `db:"source_id"`
	SourceName   string          `db:"source_name"`
	SourceSize   int64           `db:"source_size"`
	RawSourceGit []byte          `db:"source_git"`
	RawConfig    []byte          `db:"config"`
	ErrorCode    *string         `db:"error_code"`
	ErrorMessage *string         `db:"error_message"`
	ErrorDetail  string          `db:"error_detail"`
}

func (d deploymentRow) deployment() (release.Deployment, error) {
	out := d.Deployment
	if d.SourceID != nil {
		out.Source = &release.DeploymentSource{ID: *d.SourceID, Name: d.SourceName, Size: d.SourceSize}
		if d.RawSourceGit != nil {
			out.Source.Git = &build.GitRef{}
			if err := json.Unmarshal(d.RawSourceGit, out.Source.Git); err != nil {
				return release.Deployment{}, errx.Wrap(err, "decode deployment source git", errx.TypeInternal)
			}
		}
	}
	if d.RawConfig != nil {
		out.Config = &release.Config{}
		if err := json.Unmarshal(d.RawConfig, out.Config); err != nil {
			return release.Deployment{}, errx.Wrap(err, "decode deployment config", errx.TypeInternal)
		}
	}
	if d.ErrorCode != nil {
		out.Error = &release.Failure{Code: *d.ErrorCode, Detail: d.ErrorDetail}
		if d.ErrorMessage != nil {
			out.Error.Message = *d.ErrorMessage
		}
	}
	return out, nil
}

func (r *Repository) CreateDeployment(ctx context.Context, d release.Deployment) error {
	var source *build.SourceID
	var name string
	var size int64
	var git []byte // NULL when the source has no git reference
	if d.Source != nil {
		source, name, size = &d.Source.ID, d.Source.Name, d.Source.Size
		if d.Source.Git != nil {
			var err error
			if git, err = json.Marshal(d.Source.Git); err != nil {
				return errx.Wrap(err, "marshal deployment source git", errx.TypeInternal)
			}
		}
	}
	var cfg []byte
	if d.Config != nil {
		var err error
		if cfg, err = json.Marshal(d.Config); err != nil {
			return errx.Wrap(err, "marshal deployment config", errx.TypeInternal)
		}
	}
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO release.deployments
		(id, organization_id, project_id, environment_id, release_id, image, build_id, source_id, source_name, source_size, source_git, config,
		 status, reason, step, revision, previous_release_id, rollback_of, run_id, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		d.ID, d.Organization, d.Project, d.Environment, d.Release, d.Image, d.Build, source, name, size, git, cfg,
		d.Status, d.Reason, d.Step, d.Revision, d.Previous, d.RollbackOf, d.Run, d.RequestedBy)
	return pgerr.MapWith(err, "create deployment", messages)
}

func (r *Repository) FindDeployment(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID) (release.Deployment, error) {
	return r.findDeployment(ctx, "", organization, id)
}

func (r *Repository) FindDeploymentForUpdate(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID) (release.Deployment, error) {
	return r.findDeployment(ctx, " FOR UPDATE", organization, id)
}

func (r *Repository) findDeployment(ctx context.Context, lock string, organization identity.OrganizationID, id release.DeploymentID) (release.Deployment, error) {
	var row deploymentRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+deploymentColumns+`
		FROM release.deployments WHERE organization_id = $1 AND id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Deployment{}, errx.NotFound("deployment not found")
	}
	if err != nil {
		return release.Deployment{}, pgerr.MapWith(err, "find deployment", messages)
	}
	return row.deployment()
}

// ListDeployments orders newest first.
func (r *Repository) ListDeployments(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID,
	filter release.DeploymentFilter, page query.Pagination) (query.Paginated[release.Deployment], error) {
	where, args := `organization_id = $1 AND environment_id = $2`, []any{organization, env}
	if len(filter.Status) > 0 {
		statuses := make([]string, len(filter.Status))
		for i, s := range filter.Status {
			statuses[i] = string(s)
		}
		args = append(args, pq.Array(statuses))
		where += ` AND status = ANY($3)`
	}
	var total int
	if err := txn.Q(ctx, r.db).GetContext(ctx, &total, `SELECT count(*) FROM release.deployments WHERE `+where, args...); err != nil {
		return query.Paginated[release.Deployment]{}, pgerr.MapWith(err, "count deployments", messages)
	}
	rows := []deploymentRow{}
	args = append(args, page.Limit, page.Offset)
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+deploymentColumns+` FROM release.deployments WHERE `+where+
		` ORDER BY created_at DESC, revision DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return query.Paginated[release.Deployment]{}, pgerr.MapWith(err, "list deployments", messages)
	}
	items := make([]release.Deployment, len(rows))
	for i, row := range rows {
		if items[i], err = row.deployment(); err != nil {
			return query.Paginated[release.Deployment]{}, err
		}
	}
	return query.NewPaginated(items, total, page), nil
}

func (r *Repository) SetBuilt(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID, rel release.Release) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.deployments
		SET release_id = $3, image = $4, status = $5, step = $6, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NULL AND release_id IS NULL AND build_id IS NOT NULL`,
		organization, id, rel.ID, rel.Image, release.StatusPending, release.StepApply)
	return affected(res, err, "record built release", "deployment not found, finished or not building")
}

func (r *Repository) SetProgress(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID, status release.Status, step string) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.deployments SET status = $3, step = $4, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NULL`, organization, id, status, step)
	return affected(res, err, "set deployment progress", "deployment not found or finished")
}

func (r *Repository) Finish(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID, status release.Status, failure *release.Failure) error {
	var code, msg *string
	var detail string
	if failure != nil {
		code, msg, detail = &failure.Code, &failure.Message, failure.Detail
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.deployments
		SET status = $3, error_code = $4, error_message = $5, error_detail = $6, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NULL`, organization, id, status, code, msg, detail)
	return affected(res, err, "finish deployment", "deployment not found or finished")
}

// ---- environment state ----

const stateColumns = `environment_id, organization_id, project_id, app, revision, current_release_id,
	current_deployment_id, active_deployment_id, url, updated_at`

func (r *Repository) FindState(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID) (release.EnvironmentState, error) {
	var s release.EnvironmentState
	err := txn.Q(ctx, r.db).GetContext(ctx, &s, `SELECT `+stateColumns+` FROM release.environment_states
		WHERE organization_id = $1 AND environment_id = $2`, organization, env)
	if errors.Is(err, sql.ErrNoRows) {
		return release.EnvironmentState{Environment: env, Organization: organization}, nil
	}
	if err != nil {
		return release.EnvironmentState{}, pgerr.MapWith(err, "find environment state", messages)
	}
	return s, nil
}

func (r *Repository) LockState(ctx context.Context, s release.EnvironmentState) (release.EnvironmentState, error) {
	q := txn.Q(ctx, r.db)
	if _, err := q.ExecContext(ctx, `INSERT INTO release.environment_states (environment_id, organization_id, project_id, app)
		VALUES ($1, $2, $3, $4) ON CONFLICT (environment_id) DO NOTHING`, s.Environment, s.Organization, s.Project, s.App); err != nil {
		return release.EnvironmentState{}, pgerr.MapWith(err, "create environment state", messages)
	}
	var out release.EnvironmentState
	err := q.GetContext(ctx, &out, `SELECT `+stateColumns+` FROM release.environment_states
		WHERE organization_id = $1 AND environment_id = $2 FOR UPDATE`, s.Organization, s.Environment)
	if errors.Is(err, sql.ErrNoRows) { // the environment id exists in another organization
		return release.EnvironmentState{}, errx.NotFound("environment not found")
	}
	if err != nil {
		return release.EnvironmentState{}, pgerr.MapWith(err, "lock environment state", messages)
	}
	return out, nil
}

func (r *Repository) Begin(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, d release.DeploymentID, revision int64) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.environment_states
		SET active_deployment_id = $3, revision = $4, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND revision < $4`, organization, env, d, revision)
	return affected(res, err, "begin deployment", "environment not found")
}

func (r *Repository) Settle(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, d release.DeploymentID) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.environment_states
		SET active_deployment_id = NULL, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND active_deployment_id = $3`, organization, env, d)
	return pgerr.MapWith(err, "settle deployment", messages)
}

func (r *Repository) Promote(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, d release.Deployment, url string) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.environment_states
		SET current_release_id = $3, current_deployment_id = $4, url = $5, updated_at = now(),
		    active_deployment_id = CASE WHEN active_deployment_id = $4 THEN NULL ELSE active_deployment_id END
		WHERE organization_id = $1 AND environment_id = $2`, organization, env, d.Release, d.ID, url)
	return affected(res, err, "promote release", "environment not found")
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

// ---- upload tickets ----

const ticketColumns = `id, organization_id, project_id, environment_id, token_hash, name, config, client, requested_by,
	created_at, expires_at, used_at, deployment_id`

type ticketRow struct {
	release.UploadTicket
	RawConfig []byte `db:"config"`
}

func (t ticketRow) ticket() (release.UploadTicket, error) {
	out := t.UploadTicket
	if err := json.Unmarshal(t.RawConfig, &out.Config); err != nil {
		return release.UploadTicket{}, errx.Wrap(err, "decode upload ticket config", errx.TypeInternal)
	}
	return out, nil
}

// CreateUploadTicket stores the ticket by its token hash; the token itself
// (ticket.Token) is never written.
func (r *Repository) CreateUploadTicket(ctx context.Context, t release.UploadTicket) error {
	cfg, err := json.Marshal(t.Config)
	if err != nil {
		return errx.Wrap(err, "marshal upload ticket config", errx.TypeInternal)
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err = txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO release.upload_tickets
		(id, organization_id, project_id, environment_id, token_hash, name, config, client, requested_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		t.ID, t.Organization, t.Project, t.Environment, t.TokenHash, t.Name, cfg, t.Client, t.RequestedBy, t.CreatedAt, t.ExpiresAt)
	return pgerr.MapWith(err, "create upload ticket", pgerr.Messages{Unique: "this upload ticket already exists"})
}

// ConsumeUploadTicket is the single-use guarantee: one UPDATE flips used_at
// for an unused, unexpired ticket. It is not scoped by organization.
func (r *Repository) ConsumeUploadTicket(ctx context.Context, hash []byte, now time.Time) (release.UploadTicket, error) {
	var row ticketRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `UPDATE release.upload_tickets SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING `+ticketColumns, hash, now)
	if errors.Is(err, sql.ErrNoRows) {
		return release.UploadTicket{}, errx.NotFound("upload ticket not found")
	}
	if err != nil {
		return release.UploadTicket{}, pgerr.Map(err, "consume upload ticket")
	}
	return row.ticket()
}

func (r *Repository) FindUploadTicketByHash(ctx context.Context, hash []byte) (release.UploadTicket, error) {
	var row ticketRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+ticketColumns+` FROM release.upload_tickets WHERE token_hash = $1`, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return release.UploadTicket{}, errx.NotFound("upload ticket not found")
	}
	if err != nil {
		return release.UploadTicket{}, pgerr.Map(err, "find upload ticket")
	}
	return row.ticket()
}

func (r *Repository) SetUploadDeployment(ctx context.Context, organization identity.OrganizationID, ticket release.UploadTicketID, deployment release.DeploymentID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.upload_tickets SET deployment_id = $3
		WHERE organization_id = $1 AND id = $2 AND used_at IS NOT NULL`, organization, ticket, deployment)
	return affected(res, err, "record upload deployment", "upload ticket not found or not used")
}

func (r *Repository) DeleteUploadTickets(ctx context.Context, organization identity.OrganizationID, cutoff time.Time) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `DELETE FROM release.upload_tickets WHERE organization_id = $1 AND expires_at < $2`, organization, cutoff)
	return pgerr.Map(err, "delete upload tickets")
}
