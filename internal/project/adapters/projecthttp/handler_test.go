package projecthttp_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projecthttp"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// memRepo is an in-memory project.Repository, scoped by organization.
type memRepo struct {
	rows map[project.ID]project.Project
}

func (m *memRepo) Create(_ context.Context, org identity.OrganizationID, id project.ID, in project.Create) error {
	for _, p := range m.rows {
		if p.Organization == org && p.Slug == in.Slug {
			return errx.Conflict("a project with this slug already exists")
		}
	}
	now := time.Now()
	m.rows[id] = project.Project{ID: id, Organization: org, Name: in.Name, Slug: in.Slug, Description: in.Description,
		Status: project.StatusActive, CreatedBy: in.CreatedBy, UpdatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now}
	return nil
}

func (m *memRepo) Find(_ context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	p, ok := m.rows[id]
	if !ok || p.Organization != org {
		return project.Project{}, errx.NotFound("project not found")
	}
	return p, nil
}

func (m *memRepo) FindForUpdate(ctx context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	return m.Find(ctx, org, id)
}

func (m *memRepo) List(_ context.Context, org identity.OrganizationID, f project.Filter, p query.Pagination) (query.Paginated[project.Project], error) {
	var items []project.Project
	for _, r := range m.rows {
		for _, s := range f.Status {
			if r.Organization == org && r.Status == s {
				items = append(items, r)
			}
		}
	}
	return query.NewPaginated(items, len(items), p), nil
}

func (m *memRepo) Update(ctx context.Context, org identity.OrganizationID, id project.ID, in project.Update) error {
	p, err := m.Find(ctx, org, id)
	if err != nil {
		return err
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	m.rows[id] = p
	return nil
}

func (m *memRepo) Archive(ctx context.Context, org identity.OrganizationID, id project.ID, _ project.Archive) error {
	p, err := m.Find(ctx, org, id)
	if err != nil {
		return err
	}
	now := time.Now()
	p.Status, p.ArchivedAt = project.StatusArchived, &now
	m.rows[id] = p
	return nil
}

func TestProjectRoutes(t *testing.T) {
	svc := projectsvc.New(&memRepo{rows: map[project.ID]project.Project{}}, txn.Passthrough{})
	app := httpxtest.App()
	projecthttp.New(svc, svc).Register(app.Group("/api/v1"))
	org := identity.NewOrganizationID()
	writer := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite)
	reader := httpxtest.Token(org, permission.ProjectsRead)
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead, permission.ProjectsWrite)

	// create: 201 + Location + full resource, slug derived from the name
	res := httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", writer, map[string]any{"name": "Portal de Ventas"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[project.Project](t, res)
	if created.Slug != "portal-de-ventas" || created.Status != project.StatusActive || created.CreatedBy.String() != httpxtest.User {
		t.Fatalf("created %+v", created)
	}
	if loc := res.Header.Get("Location"); loc != "/api/v1/projects/"+created.ID.String() {
		t.Fatalf("Location %q", loc)
	}
	path := "/api/v1/projects/" + created.ID.String()

	// duplicate slug → 409; unknown field → 400; reader can't write → 403
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", writer, map[string]any{"name": "x", "slug": "portal-de-ventas"}), http.StatusConflict)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", writer, `{"name":"x","owner":"me"}`), http.StatusBadRequest)
	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", writer, map[string]any{"name": "x", "slug": "Bad Slug"})
	httpxtest.Want(t, res, http.StatusBadRequest)
	if httpxtest.ErrorField(res) != "slug" {
		t.Fatalf("field %q", httpxtest.ErrorField(res))
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", reader, map[string]any{"name": "Otro"}), http.StatusForbidden)

	// read, list, tenant isolation
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, path, reader, nil), http.StatusOK)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, path, other, nil), http.StatusNotFound)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects/not-a-uuid", reader, nil), http.StatusBadRequest)
	res = httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects?limit=10", reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if page := httpxtest.JSON[query.Paginated[project.Project]](t, res); page.Page.Total != 1 || page.Page.Limit != 10 {
		t.Fatalf("page %+v", page.Page)
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects?limit=0", reader, nil), http.StatusBadRequest)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects?status=deleted", reader, nil), http.StatusBadRequest)

	// patch: 200 + full resource; null on a non-nullable field → 400
	res = httpxtest.Do(t, app, http.MethodPatch, path, writer, map[string]any{"description": "Ventas B2B"})
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[project.Project](t, res); got.Description != "Ventas B2B" {
		t.Fatalf("patched %+v", got)
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPatch, path, writer, `{"name":null}`), http.StatusBadRequest)

	// archive: 200, then changes are refused with 422 PROJECT_ARCHIVED
	res = httpxtest.Do(t, app, http.MethodPost, path+"/archive", writer, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[project.Project](t, res); got.Status != project.StatusArchived || got.ArchivedAt == nil {
		t.Fatalf("archived %+v", got)
	}
	res = httpxtest.Do(t, app, http.MethodPatch, path, writer, map[string]any{"name": "Nuevo"})
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if httpxtest.ErrorCode(res) != project.CodeArchived {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, path+"/archive", writer, nil), http.StatusOK) // idempotent

	// archived projects leave the default list
	res = httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects", reader, nil)
	if page := httpxtest.JSON[query.Paginated[project.Project]](t, res); page.Page.Total != 0 {
		t.Fatalf("default list shows archived: %+v", page.Page)
	}
	res = httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects?status=archived", reader, nil)
	if page := httpxtest.JSON[query.Paginated[project.Project]](t, res); page.Page.Total != 1 {
		t.Fatalf("archived list: %+v", page.Page)
	}
}
