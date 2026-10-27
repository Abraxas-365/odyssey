package bootstrap_test

import (
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// TestServerRoundTrip drives the real composition (server middleware, every
// module, Postgres with all migrations) with only IAMKit faked.
func TestServerRoundTrip(t *testing.T) {
	db := pgtest.DB(t, bootstrap.Migrations()...)
	app := bootstrap.NewServer(db, config.Config{}, httpxtest.Auth())
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite)

	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/health", "", nil), http.StatusOK)
	res := httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if res.Header.Get("X-Request-ID") == "" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", res.Header)
	}

	res = httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Cotizador Ñandú"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[project.Project](t, res)
	if created.Slug != "cotizador-nandu" || created.Organization != org {
		t.Fatalf("created %+v", created)
	}
	res = httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects?search=nandu", token, nil)
	if page := httpxtest.JSON[query.Paginated[project.Project]](t, res); page.Page.Total != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("list %+v", page)
	}
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects/"+created.ID.String(), other, nil), http.StatusNotFound)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodPost, "/api/v1/projects/"+created.ID.String()+"/archive", token, nil), http.StatusOK)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/nope", token, nil), http.StatusNotFound)
}
