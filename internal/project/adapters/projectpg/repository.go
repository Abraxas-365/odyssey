// Package projectpg is the Postgres implementation of project.Repository.
package projectpg

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "project"

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

var _ project.Repository = (*Repository)(nil)

// messages are the client-facing texts for constraint violations.
var messages = pgerr.Messages{Unique: "a project with this slug already exists"}

const columns = `id, organization_id, name, slug, description, status, created_by, updated_by, created_at, updated_at, archived_at`

func notFound() error { return errx.NotFound("project not found") }

func (r *Repository) Create(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.Create) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO project.projects (id, organization_id, name, slug, description, status, created_by, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		id, organization, text.Clean(input.Name), input.Slug, text.Clean(input.Description), project.StatusActive, input.CreatedBy)
	return pgerr.MapWith(err, "create project", messages)
}

func (r *Repository) Find(ctx context.Context, organization identity.OrganizationID, id project.ID) (project.Project, error) {
	return r.find(ctx, organization, id, "")
}

func (r *Repository) FindForUpdate(ctx context.Context, organization identity.OrganizationID, id project.ID) (project.Project, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return project.Project{}, errx.Internal("FindForUpdate needs a transaction")
	}
	return r.find(ctx, organization, id, " FOR UPDATE")
}

func (r *Repository) find(ctx context.Context, organization identity.OrganizationID, id project.ID, lock string) (project.Project, error) {
	var p project.Project
	err := txn.Q(ctx, r.db).GetContext(ctx, &p,
		`SELECT `+columns+` FROM project.projects WHERE organization_id = $1 AND id = $2`+lock, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return project.Project{}, notFound()
	}
	if err != nil {
		return project.Project{}, pgerr.MapWith(err, "find project", messages)
	}
	return p, nil
}

func (r *Repository) List(ctx context.Context, organization identity.OrganizationID, filter project.Filter, page query.Pagination) (query.Paginated[project.Project], error) {
	where := []string{"organization_id = $1"}
	args := []any{organization}
	if len(filter.Status) > 0 {
		values := make([]string, len(filter.Status))
		for i, v := range filter.Status {
			values[i] = string(v)
		}
		args = append(args, pq.Array(values))
		where = append(where, "status = ANY($"+strconv.Itoa(len(args))+")")
	}
	if pattern := query.EscapeLike(page.Search); pattern != "" {
		args = append(args, pattern)
		n := strconv.Itoa(len(args))
		where = append(where, "(name ILIKE $"+n+" OR slug ILIKE $"+n+")")
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := txn.Q(ctx, r.db).GetContext(ctx, &total, `SELECT count(*) FROM project.projects WHERE `+clause, args...); err != nil {
		return query.Paginated[project.Project]{}, pgerr.MapWith(err, "count projects", messages)
	}
	items := []project.Project{}
	args = append(args, page.Limit, page.Offset)
	err := txn.Q(ctx, r.db).SelectContext(ctx, &items,
		`SELECT `+columns+` FROM project.projects WHERE `+clause+
			` ORDER BY lower(name), id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		return query.Paginated[project.Project]{}, pgerr.MapWith(err, "list projects", messages)
	}
	return query.NewPaginated(items, total, page), nil
}

// Update sets only the fields present in the patch. Column names are
// literals and values placeholders: nothing from the request reaches the SQL.
func (r *Repository) Update(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.Update) error {
	set := []string{"updated_at = now()", "updated_by = $3"}
	args := []any{organization, id, input.UpdatedBy}
	add := func(column string, v any) {
		args = append(args, v)
		set = append(set, column+" = $"+strconv.Itoa(len(args)))
	}
	if input.Name != nil {
		add("name", text.Clean(*input.Name))
	}
	if input.Description != nil {
		add("description", text.Clean(*input.Description))
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE project.projects SET `+strings.Join(set, ", ")+` WHERE organization_id = $1 AND id = $2`, args...)
	return affected(res, err, "update project")
}

func (r *Repository) Archive(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.Archive) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE project.projects
		 SET status = $3, archived_at = now(), archived_by = $4, updated_at = now(), updated_by = $4
		 WHERE organization_id = $1 AND id = $2 AND status <> $3`,
		organization, id, project.StatusArchived, input.ArchivedBy)
	return affected(res, err, "archive project")
}

const environmentColumns = `id, organization_id, project_id, slug, kind, created_at`

var environmentMessages = pgerr.Messages{Unique: "an environment with this slug already exists in the project"}

func (r *Repository) CreateEnvironment(ctx context.Context, e project.Environment) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO project.environments (id, organization_id, project_id, slug, kind) VALUES ($1, $2, $3, $4, $5)`,
		e.ID, e.Organization, e.Project, e.Slug, e.Kind)
	return pgerr.MapWith(err, "create environment", environmentMessages)
}

func (r *Repository) FindEnvironment(ctx context.Context, organization identity.OrganizationID, id project.ID, environment project.EnvironmentID) (project.Environment, error) {
	return r.findEnvironment(ctx, `organization_id = $1 AND project_id = $2 AND id = $3`, organization, id, environment)
}

func (r *Repository) FindEnvironmentBySlug(ctx context.Context, organization identity.OrganizationID, id project.ID, slug string) (project.Environment, error) {
	return r.findEnvironment(ctx, `organization_id = $1 AND project_id = $2 AND slug = $3`, organization, id, slug)
}

func (r *Repository) findEnvironment(ctx context.Context, where string, args ...any) (project.Environment, error) {
	var e project.Environment
	err := txn.Q(ctx, r.db).GetContext(ctx, &e, `SELECT `+environmentColumns+` FROM project.environments WHERE `+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return project.Environment{}, errx.NotFound("environment not found")
	}
	if err != nil {
		return project.Environment{}, pgerr.MapWith(err, "find environment", environmentMessages)
	}
	return e, nil
}

// ListEnvironments orders production, staging, previews, then by slug.
func (r *Repository) ListEnvironments(ctx context.Context, organization identity.OrganizationID, id project.ID, filter project.EnvironmentFilter) ([]project.Environment, error) {
	where, args := `organization_id = $1 AND project_id = $2`, []any{organization, id}
	if len(filter.Kind) > 0 {
		kinds := make([]string, len(filter.Kind))
		for i, k := range filter.Kind {
			kinds[i] = string(k)
		}
		args = append(args, pq.Array(kinds))
		where += ` AND kind = ANY($3)`
	}
	out := []project.Environment{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+environmentColumns+` FROM project.environments WHERE `+where+
		` ORDER BY array_position(ARRAY['production','staging','preview'], kind), slug, id`, args...)
	if err != nil {
		return nil, pgerr.MapWith(err, "list environments", environmentMessages)
	}
	return out, nil
}

// affected maps "no row matched" to NotFound.
func affected(res sql.Result, err error, op string) error {
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errx.Wrap(err, op, errx.TypeInternal)
	}
	if n == 0 {
		return notFound()
	}
	return nil
}
