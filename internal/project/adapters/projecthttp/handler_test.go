package projecthttp_test

import (
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projecthttp"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

func TestSetTierRoute(t *testing.T) {
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	app := httpxtest.App()
	projecthttp.New(svc, svc).Register(app.Group("/api/v1"))
	org := identity.NewOrganizationID()
	builder := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite)
	it := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsTier)
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead, permission.ProjectsTier)

	res := httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", builder, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[project.Project](t, res)
	if created.Tier != project.TierInternal {
		t.Fatalf("new app's tier %q", created.Tier)
	}
	path := "/api/v1/projects/" + created.ID.String() + "/tier"

	// only projects:tier moves it (a builder with projects:write cannot)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPut, path, builder, map[string]any{"tier": "sandbox"}), http.StatusForbidden)
	res = httpxtest.Do(t, app, http.MethodPut, path, it, map[string]any{"tier": "sandbox"})
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[project.Project](t, res); got.Tier != project.TierSandbox {
		t.Fatalf("tier %q", got.Tier)
	}
	res = httpxtest.Do(t, app, http.MethodPut, path, it, map[string]any{"tier": "gold"})
	httpxtest.Want(t, res, http.StatusBadRequest)
	if httpxtest.ErrorField(res) != "tier" {
		t.Fatalf("field %q", httpxtest.ErrorField(res))
	}
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPut, path, it, `{"tier":"sandbox","by":"me"}`), http.StatusBadRequest)
	// the organization comes from the token: another tenant finds nothing
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPut, path, other, map[string]any{"tier": "production"}), http.StatusNotFound)
}

func TestProjectRoutes(t *testing.T) {
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
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
