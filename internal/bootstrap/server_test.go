package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// assemble builds the real platform over a fresh, fully migrated database.
func assemble(t *testing.T) *bootstrap.Platform {
	t.Helper()
	db := pgtest.DB(t)
	if err := bootstrap.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	p, err := bootstrap.Assemble(context.Background(), db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestServerRoundTrip drives the real composition (server middleware, every
// module, Postgres with all migrations) with only IAMKit faked.
func TestServerRoundTrip(t *testing.T) {
	platform := assemble(t)
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth())
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.AuditRead)

	httpxtest.Want(t, checked(t, app, http.MethodGet, "/health", "", nil), http.StatusOK)
	res := checked(t, app, http.MethodGet, "/api/v1/projects", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if res.Header.Get("X-Request-ID") == "" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", res.Header)
	}

	res = checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Cotizador Ñandú"})
	httpxtest.Want(t, res, http.StatusCreated)
	created := httpxtest.JSON[project.Project](t, res)
	if created.Slug != "cotizador-nandu" || created.Organization != org {
		t.Fatalf("created %+v", created)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/projects?search=nandu", token, nil)
	if page := httpxtest.JSON[query.Paginated[project.Project]](t, res); page.Page.Total != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("list %+v", page)
	}
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/projects/"+created.ID.String(), other, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/projects/"+created.ID.String()+"/archive", token, nil), http.StatusOK)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/nope", token, nil), http.StatusNotFound) // not in the spec
	// error responses follow the contract too
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Otra", "slug": "cotizador-nandu"}), http.StatusConflict)
	httpxtest.Want(t, checked(t, app, http.MethodPatch, "/api/v1/projects/"+created.ID.String(), token, map[string]any{"name": "x"}), http.StatusUnprocessableEntity)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/projects?limit=abc", token, nil), http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/projects", "", nil), http.StatusUnauthorized)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/config", "", nil), http.StatusOK)

	// the worker delivers every write to the trail through the outbox, in
	// order, correlated to its request, and the chain verifies
	ctx, stop := context.WithCancel(context.Background())
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(ctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	var trail query.Paginated[audit.Entry]
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		res = checked(t, app, http.MethodGet, "/api/v1/audit?subject=project/"+created.ID.String(), token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		if trail = httpxtest.JSON[query.Paginated[audit.Entry]](t, res); trail.Page.Total == 2 || time.Now().After(deadline) {
			break
		}
	}
	stop()
	if err := <-worked; err != nil {
		t.Fatalf("worker: %v", err)
	}
	if trail.Page.Total != 2 || trail.Items[0].Type != project.EventArchived || trail.Items[1].Type != project.EventCreated ||
		trail.Items[1].Summary != "Creó la app Cotizador Ñandú" || trail.Items[1].Actor.String() != httpxtest.User ||
		trail.Items[1].CorrelationID == "" || trail.Items[1].Seq != 1 {
		t.Fatalf("trail %+v", trail)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/audit/verify", token, nil)
	if v := httpxtest.JSON[audit.Verification](t, res); !v.Valid || v.Entries != 2 {
		t.Fatalf("verify %+v", v)
	}
	// the trail is its own permission, and per organization
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/audit", other, nil), http.StatusForbidden)
}
