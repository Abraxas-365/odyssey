package bootstrap_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
)

// TestCatalogOverHTTP: anyone in the organization reads the catalog (the
// factory offerings appear on the first read); only catalog:manage edits an
// offering; a second default in a tier is 409; organizations do not mix.
func TestCatalogOverHTTP(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	reader := httpxtest.Token(org, permission.ProjectsRead)
	it := httpxtest.Token(org, permission.CatalogManage)

	type list struct {
		Items []catalog.Entry `json:"items"`
	}
	res := checked(t, app, http.MethodGet, "/api/v1/catalog/recipes", reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if items := httpxtest.JSON[list](t, res).Items; len(items) != 4 || items[0].Recipe != catalog.PostgresBasic {
		t.Fatalf("factory catalog: %+v", items)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/recipes/"+catalog.PostgresProduction, reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Spec.Postgres == nil || e.Offering.Revision != 1 || e.Offering.Preapproved[catalog.ParamStorage].Max != 50 {
		t.Fatalf("production postgres: %+v", e)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/catalog/recipes/mysql", reader, nil), http.StatusNotFound)

	// the kinds' contracts are anyone's: what the console asks IT for
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/kinds", reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if kinds := httpxtest.JSON[struct {
		Items []catalog.KindContract `json:"items"`
	}](t, res).Items; len(kinds) != 4 || kinds[0].Kind != "postgres" || !kinds[0].Existing || kinds[2].Existing || !kinds[0].Variables[0].Derived {
		t.Fatalf("kinds: %+v", kinds)
	}

	offer := func(tok string, body map[string]any) httpxtest.Response {
		return checked(t, app, http.MethodPut, "/api/v1/catalog/recipes/"+catalog.PostgresProduction+"/offering", tok, body)
	}
	body := map[string]any{"version": 1, "active": true, "tiers": []string{"internal", "production"}, "defaults": []string{"production"},
		"limits": map[string]any{"storage_gi": map[string]int{"min": 5, "max": 80}}, "preapproved": map[string]any{"storage_gi": map[string]int{"max": 20}}}
	httpxtest.Want(t, offer(reader, body), http.StatusForbidden)
	refused(t, offer(it, body), http.StatusConflict, catalog.CodeDefaultTaken)

	body["defaults"] = []string{}
	res = offer(it, body)
	httpxtest.Want(t, res, http.StatusOK)
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Offering.Revision != 2 || len(e.Offering.Tiers) != 2 || e.Offering.Limits[catalog.ParamStorage].Max != 80 {
		t.Fatalf("updated: %+v", e.Offering)
	}
	body["limits"] = map[string]any{"storage_gi": map[string]int{"max": 500}}
	httpxtest.Want(t, offer(it, body), http.StatusBadRequest)

	// another organization still has the factory offering
	res = checked(t, app, http.MethodGet, "/api/v1/catalog/recipes/"+catalog.PostgresProduction, httpxtest.Token(other), nil)
	httpxtest.Want(t, res, http.StatusOK)
	if e := httpxtest.JSON[catalog.Entry](t, res); e.Offering.Revision != 1 || len(e.Offering.Tiers) != 1 {
		t.Fatalf("other organization: %+v", e.Offering)
	}
}
