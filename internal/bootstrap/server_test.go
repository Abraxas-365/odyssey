package bootstrap_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
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
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
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
	if v := httpxtest.JSON[audit.Verification](t, res); !v.Valid || v.Entries != 3 { // + environment.created
		t.Fatalf("verify %+v", v)
	}
	// the trail is its own permission, and per organization
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/audit", other, nil), http.StatusForbidden)

	// An upload ticket (what deploy_folder issues) is the only credential of
	// PUT /uploads/:ticket: it publishes a folder once, attributed to the
	// MCP client that asked for it.
	res = checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Subida"})
	httpxtest.Want(t, res, http.StatusCreated)
	up := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+up.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	user := identity.MustParseUserID(httpxtest.User)
	ticket, err := platform.Releases.Commands.IssueUpload(event.WithMeta(context.Background(), event.Meta{Client: "mcp:test"}),
		org, up.ID, env.ID, release.IssueUpload{Name: "subida", Config: release.Config{Port: 3000}, RequestedBy: user})
	if err != nil || !release.ValidUploadToken(ticket.Token) || ticket.Client != "mcp:test" {
		t.Fatalf("issue %+v %v", ticket, err)
	}
	archive := folder(t, "uploaded")
	res = uploadRaw(t, app, "/uploads/"+ticket.Token, "application/gzip", archive)
	httpxtest.Want(t, res, http.StatusAccepted)
	accepted := httpxtest.JSON[struct {
		Deployment string `json:"deployment_id"`
		Status     string `json:"status"`
		NextStep   string `json:"next_step"`
	}](t, res)
	if accepted.Deployment == "" || accepted.Status != string(release.StatusBuilding) || accepted.NextStep == "" {
		t.Fatalf("accepted %+v", accepted)
	}
	d, err := platform.Releases.Queries.FindDeployment(context.Background(), org, up.ID, env.ID, release.MustParseDeploymentID(accepted.Deployment))
	if err != nil || d.RequestedBy != user || d.Source == nil {
		t.Fatalf("deployment %+v %v", d, err)
	}
	res = uploadRaw(t, app, "/uploads/"+ticket.Token, "application/gzip", archive) // spent
	httpxtest.Want(t, res, http.StatusConflict)
	if httpxtest.ErrorCode(res) != release.CodeUploadTicketUsed || !strings.Contains(string(res.Body), accepted.Deployment) {
		t.Fatalf("reused ticket: %s", res.Body)
	}
	for _, bad := range []string{"nope", "odu_" + strings.Repeat("A", 43), release.UploadTicketPrefix} {
		httpxtest.Want(t, uploadRaw(t, app, "/uploads/"+bad, "application/gzip", archive), http.StatusNotFound)
	}
	httpxtest.Want(t, uploadRaw(t, app, "/uploads/"+ticket.Token, "application/json", archive), http.StatusUnsupportedMediaType)
}

// uploadRaw PUTs a raw body (no Authorization) and validates the response
// against the documented operation.
func uploadRaw(t *testing.T, app *fiber.App, path, contentType string, body []byte) httpxtest.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	res, err := app.Test(req, 30000)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	r := httpxtest.Response{Status: res.StatusCode, Header: res.Header, Body: out}
	validate(t, http.MethodPut, path, r)
	return r
}
