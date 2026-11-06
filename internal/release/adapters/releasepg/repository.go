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
	source_size, source_git, config, status, reason, step, revision, previous_release_id, rollback_of, settings_revision, error_code, error_message, error_detail,
	run_id, requested_by, requested_via, created_at, updated_at, finished_at`

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
		 status, reason, step, revision, previous_release_id, rollback_of, run_id, requested_by, requested_via)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
		d.ID, d.Organization, d.Project, d.Environment, d.Release, d.Image, d.Build, source, name, size, git, cfg,
		d.Status, d.Reason, d.Step, d.Revision, d.Previous, d.RollbackOf, d.Run, d.RequestedBy, d.RequestedVia)
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

// SetSettingsRevision records the settings revision the apply step handed to
// the cluster.
func (r *Repository) SetSettingsRevision(ctx context.Context, organization identity.OrganizationID, id release.DeploymentID, revision int64) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.deployments SET settings_revision = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND finished_at IS NULL`, organization, id, revision)
	return affected(res, err, "set deployment settings revision", "deployment not found or finished")
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
	current_deployment_id, active_deployment_id, url, updated_at, applied_settings_revision, retired_at`

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

// ---- batch reads (inventory, attention) ----

type servingRow struct {
	Environment project.EnvironmentID `db:"environment_id"`
	Project     project.ID            `db:"project_id"`
	URL         string                `db:"url"`
	Image       string                `db:"image"`
	Build       *build.ID             `db:"build_id"`
	Since       *time.Time            `db:"since"`
	LastID      *release.DeploymentID `db:"last_id"`
	LastStatus  *release.Status       `db:"last_status"`
	LastReason  *release.Reason       `db:"last_reason"`
	ErrorCode   *string               `db:"error_code"`
	ErrorMsg    *string               `db:"error_message"`
	ErrorDetail *string               `db:"error_detail"`
	LastCreated *time.Time            `db:"last_created_at"`
	LastEnded   *time.Time            `db:"last_finished_at"`
}

// ListServing reads, in one statement, what each environment serves (its
// current deployment's build and the time it went live) and how its latest
// deployment stands. Environments with no state row are absent.
func (r *Repository) ListServing(ctx context.Context, organization identity.OrganizationID, envs []project.EnvironmentID) ([]release.Serving, error) {
	rows := []servingRow{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `
		SELECT s.environment_id, s.project_id, s.url, COALESCE(rel.image, '') AS image,
		       (SELECT b.build_id FROM release.deployments b
		         WHERE b.organization_id = s.organization_id AND b.release_id = s.current_release_id AND b.build_id IS NOT NULL
		         ORDER BY b.created_at LIMIT 1) AS build_id,
		       cd.finished_at AS since,
		       l.id AS last_id, l.status AS last_status, l.reason AS last_reason, l.error_code, l.error_message, l.error_detail,
		       l.created_at AS last_created_at, l.finished_at AS last_finished_at
		  FROM release.environment_states s
		  LEFT JOIN release.releases rel ON rel.id = s.current_release_id
		  LEFT JOIN release.deployments cd ON cd.id = s.current_deployment_id
		  LEFT JOIN LATERAL (SELECT d.id, d.status, d.reason, d.error_code, d.error_message, d.error_detail, d.created_at, d.finished_at
		                       FROM release.deployments d
		                      WHERE d.organization_id = s.organization_id AND d.environment_id = s.environment_id
		                      ORDER BY d.created_at DESC, d.revision DESC LIMIT 1) l ON true
		 WHERE s.organization_id = $1 AND s.environment_id = ANY($2::uuid[])
		 ORDER BY s.environment_id`, organization, pq.Array(envStrings(envs)))
	if err != nil {
		return nil, pgerr.MapWith(err, "list environments serving", messages)
	}
	out := make([]release.Serving, len(rows))
	for i, row := range rows {
		s := release.Serving{Environment: row.Environment, Project: row.Project, URL: row.URL, Image: row.Image, Build: row.Build, Since: row.Since}
		if row.LastID != nil && row.LastStatus != nil && row.LastReason != nil && row.LastCreated != nil {
			s.Last = &release.LastDeployment{ID: *row.LastID, Status: *row.LastStatus, Reason: *row.LastReason, CreatedAt: *row.LastCreated, FinishedAt: row.LastEnded}
			if row.ErrorCode != nil {
				s.Last.Error = &release.Failure{Code: *row.ErrorCode}
				if row.ErrorMsg != nil {
					s.Last.Error.Message = *row.ErrorMsg
				}
				if row.ErrorDetail != nil {
					s.Last.Error.Detail = *row.ErrorDetail
				}
			}
		}
		out[i] = s
	}
	return out, nil
}

func envStrings(ids []project.EnvironmentID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// FindStateByEnvironment finds a state by environment id alone (the access
// sign-in); NotFound when there is none.
func (r *Repository) FindStateByEnvironment(ctx context.Context, env project.EnvironmentID) (release.EnvironmentState, error) {
	var s release.EnvironmentState
	err := txn.Q(ctx, r.db).GetContext(ctx, &s, `SELECT `+stateColumns+` FROM release.environment_states WHERE environment_id = $1`, env)
	if errors.Is(err, sql.ErrNoRows) {
		return release.EnvironmentState{}, errx.NotFound("environment not found")
	}
	if err != nil {
		return release.EnvironmentState{}, pgerr.MapWith(err, "find environment state by id", messages)
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

// Retire records the retirement at revision; like Begin, never backwards.
func (r *Repository) Retire(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, revision int64) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.environment_states
		SET revision = $3, retired_at = now(), updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND revision < $3`, organization, env, revision)
	return affected(res, err, "retire app", "environment not found")
}

// SetAppliedSettings records the settings revision the cluster was just told
// (and that it runs the app again: no longer retired).
// Only the deployment that holds the environment's latest revision may: one
// superseded meanwhile must not put back older settings.
func (r *Repository) SetAppliedSettings(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, deploymentRevision, settings int64) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `UPDATE release.environment_states
		SET applied_settings_revision = $4, retired_at = NULL, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND revision = $3`, organization, env, deploymentRevision, settings)
	return pgerr.MapWith(err, "record applied settings", messages)
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

// ---- environment settings ----

const settingsColumns = `environment_id, organization_id, revision, access_mode, env, secrets, resources, ai, created_by, created_at`

type settingsRow struct {
	Environment  project.EnvironmentID   `db:"environment_id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Revision     int64                   `db:"revision"`
	AccessMode   release.AccessMode      `db:"access_mode"`
	Env          []byte                  `db:"env"`
	Secrets      []byte                  `db:"secrets"`
	Resources    []byte                  `db:"resources"`
	AI           []byte                  `db:"ai"`
	CreatedBy    identity.UserID         `db:"created_by"`
	CreatedAt    time.Time               `db:"created_at"`
}

func (s settingsRow) settings() (release.Settings, error) {
	out := release.Settings{Environment: s.Environment, Organization: s.Organization, Revision: s.Revision,
		Access: release.AccessSettings{Mode: s.AccessMode}, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt}
	if err := json.Unmarshal(s.Env, &out.Env); err != nil {
		return release.Settings{}, errx.Wrap(err, "decode environment variables", errx.TypeInternal)
	}
	if err := json.Unmarshal(s.Secrets, &out.Secrets); err != nil {
		return release.Settings{}, errx.Wrap(err, "decode environment secrets", errx.TypeInternal)
	}
	if err := json.Unmarshal(s.Resources, &out.Resources); err != nil {
		return release.Settings{}, errx.Wrap(err, "decode environment resources", errx.TypeInternal)
	}
	if err := json.Unmarshal(s.AI, &out.AI); err != nil {
		return release.Settings{}, errx.Wrap(err, "decode environment ai", errx.TypeInternal)
	}
	return out.Normalize(), nil
}

// FindSettings is the latest revision; an environment that never changed its
// settings has the defaults (revision 0).
func (r *Repository) FindSettings(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID) (release.Settings, error) {
	var row settingsRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+settingsColumns+` FROM release.environment_settings
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY revision DESC LIMIT 1`, organization, env)
	if errors.Is(err, sql.ErrNoRows) {
		return release.DefaultSettings(organization, env), nil
	}
	if err != nil {
		return release.Settings{}, pgerr.MapWith(err, "find environment settings", messages)
	}
	return row.settings()
}

// FindSettingsRevision is one revision; 0 is the defaults.
func (r *Repository) FindSettingsRevision(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, revision int64) (release.Settings, error) {
	if revision == 0 {
		return release.DefaultSettings(organization, env), nil
	}
	var row settingsRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+settingsColumns+` FROM release.environment_settings
		WHERE organization_id = $1 AND environment_id = $2 AND revision = $3`, organization, env, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Settings{}, errx.NotFound("settings revision not found")
	}
	if err != nil {
		return release.Settings{}, pgerr.MapWith(err, "find environment settings revision", messages)
	}
	return row.settings()
}

// AppendSettings writes s as the environment's next revision. Callers hold
// the environment's state lock, so revisions never race.
func (r *Repository) AppendSettings(ctx context.Context, s release.Settings) (release.Settings, error) {
	s = s.Normalize()
	env, err := json.Marshal(s.Env)
	if err != nil {
		return release.Settings{}, errx.Wrap(err, "encode environment variables", errx.TypeInternal)
	}
	secrets, err := json.Marshal(s.Secrets)
	if err != nil {
		return release.Settings{}, errx.Wrap(err, "encode environment secrets", errx.TypeInternal)
	}
	resources, err := json.Marshal(s.Resources)
	if err != nil {
		return release.Settings{}, errx.Wrap(err, "encode environment resources", errx.TypeInternal)
	}
	ai, err := json.Marshal(s.AI)
	if err != nil {
		return release.Settings{}, errx.Wrap(err, "encode environment ai", errx.TypeInternal)
	}
	var row settingsRow
	err = txn.Q(ctx, r.db).GetContext(ctx, &row, `INSERT INTO release.environment_settings (environment_id, organization_id, revision, access_mode, env, secrets, resources, ai, created_by)
		SELECT $1, $2, COALESCE(max(revision), 0) + 1, $3, $4, $5, $6, $7, $8 FROM release.environment_settings WHERE environment_id = $1 AND organization_id = $2
		RETURNING `+settingsColumns, s.Environment, s.Organization, s.Access.Mode, env, secrets, resources, ai, s.CreatedBy)
	if err != nil {
		return release.Settings{}, pgerr.MapWith(err, "append environment settings", messages)
	}
	if err := r.raiseSecretVersions(ctx, s); err != nil {
		return release.Settings{}, err
	}
	return row.settings()
}

// raiseSecretVersions lifts the high-water mark of every managed secret of s
// (in the revision's transaction, so the two never disagree). It only ever
// raises: GREATEST keeps the old mark when the name came back with a lower
// version or was removed. Another organization's row of the same environment
// id is left alone (the WHERE), like PutSecretRequest.
func (r *Repository) raiseSecretVersions(ctx context.Context, s release.Settings) error {
	var names []string
	var versions []int64
	for _, ref := range s.Secrets {
		if ref.Source == release.SecretManaged && ref.Version > 0 {
			names, versions = append(names, ref.Name), append(versions, ref.Version)
		}
	}
	if s.AI.Enabled && s.AI.KeyVersion > 0 {
		// the AI's key is a managed secret of its own name: its versions never repeat either
		names, versions = append(names, release.AIKeySecret), append(versions, s.AI.KeyVersion)
	}
	if len(names) == 0 {
		return nil
	}
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO release.secret_versions (environment_id, organization_id, name, version)
		SELECT $1, $2, n, v FROM unnest($3::text[], $4::bigint[]) AS u(n, v)
		ON CONFLICT (environment_id, name) DO UPDATE SET version = GREATEST(release.secret_versions.version, EXCLUDED.version)
		WHERE release.secret_versions.organization_id = EXCLUDED.organization_id`,
		s.Environment, s.Organization, pq.Array(names), pq.Array(versions))
	return pgerr.MapWith(err, "raise secret versions", messages)
}

// ---- secret requests ----

const requestColumns = `environment_id, organization_id, name, note, requested_by, requested_via, created_at`

// PutSecretRequest stores the request, or renews the one of the same name
// (another person asked again: theirs is the note now).
func (r *Repository) PutSecretRequest(ctx context.Context, q release.SecretRequest) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO release.secret_requests (environment_id, organization_id, name, note, requested_by, requested_via)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (environment_id, name) DO UPDATE SET note = EXCLUDED.note, requested_by = EXCLUDED.requested_by,
		    requested_via = EXCLUDED.requested_via, created_at = now()
		WHERE release.secret_requests.organization_id = EXCLUDED.organization_id`,
		q.Environment, q.Organization, q.Name, q.Note, q.RequestedBy, q.RequestedVia)
	return pgerr.MapWith(err, "put secret request", messages)
}

func (r *Repository) ListSecretRequests(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID) ([]release.SecretRequest, error) {
	out := []release.SecretRequest{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+requestColumns+` FROM release.secret_requests
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY name`, organization, env)
	return out, pgerr.MapWith(err, "list secret requests", messages)
}

func (r *Repository) DeleteSecretRequest(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, name string) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx, `DELETE FROM release.secret_requests
		WHERE organization_id = $1 AND environment_id = $2 AND name = $3`, organization, env, name)
	if err != nil {
		return false, pgerr.MapWith(err, "delete secret request", messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, errx.Wrap(err, "delete secret request", errx.TypeInternal)
	}
	return n > 0, nil
}

// ListSecretVersions reads the high-water mark of every managed secret of the
// environment (release.secret_versions, kept by AppendSettings). The mark
// never goes down, whatever happens to the settings history.
func (r *Repository) ListSecretVersions(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID) (map[string]int64, error) {
	var rows []struct {
		Name    string `db:"name"`
		Version int64  `db:"version"`
	}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT name, version FROM release.secret_versions
		WHERE organization_id = $1 AND environment_id = $2`, organization, env)
	if err != nil {
		return nil, pgerr.MapWith(err, "read secret versions", messages)
	}
	out := make(map[string]int64, len(rows))
	for _, x := range rows {
		out[x.Name] = x.Version
	}
	return out, nil
}

// ---- upload tickets ----

const ticketColumns = `id, organization_id, project_id, environment_id, token_hash, name, config, client, requested_by,
	permissions, created_at, expires_at, used_at, deployment_id`

type ticketRow struct {
	release.UploadTicket
	RawConfig   []byte         `db:"config"`
	Permissions pq.StringArray `db:"permissions"`
}

func (t ticketRow) ticket() (release.UploadTicket, error) {
	out := t.UploadTicket
	out.Permissions = []string(t.Permissions)
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
	if t.Permissions == nil { // a nil array would be NULL
		t.Permissions = []string{}
	}
	_, err = txn.Q(ctx, r.db).ExecContext(ctx, `INSERT INTO release.upload_tickets
		(id, organization_id, project_id, environment_id, token_hash, name, config, client, requested_by, permissions, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		t.ID, t.Organization, t.Project, t.Environment, t.TokenHash, t.Name, cfg, t.Client, t.RequestedBy, pq.Array(t.Permissions), t.CreatedAt, t.ExpiresAt)
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
