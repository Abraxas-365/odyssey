// Package sourcehttp exposes the GitHub integration: installations of the
// App, the repositories they reach, each project's connected repository
// and its pushes (docs/api.md), plus the provider's public callbacks.
//
// It is the only HTTP adapter that mounts routes outside the authenticated
// /api/v1 group, and only the ones in PublicRoutes.
package sourcehttp

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// PublicRoutes are the unauthenticated routes (the provider calls them, or
// sends the browser to them); each checks its own proof.
var PublicRoutes = []string{"GET /github/setup", "POST /webhooks/github"}

type Handler struct {
	commands source.Commands
	queries  source.Queries
	// console is the console's base URL: the setup callback sends the
	// browser back there.
	console string
}

func New(commands source.Commands, queries source.Queries, console string) *Handler {
	return &Handler{commands: commands, queries: queries, console: console}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r *httpx.Router) {
	gh := r.Group("/github")
	gh.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/", h.info)
	gh.With(httpx.RequirePermissions(permission.IntegrationsManage)).Get("/install", h.installURL)
	gh.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/installations", h.installations)
	gh.With(httpx.RequirePermissions(permission.IntegrationsManage)).Post("/installations", h.claim)
	gh.With(httpx.RequirePermissions(permission.IntegrationsManage)).Delete("/installations/{installation}", h.unlink)
	gh.With(httpx.RequirePermissions(permission.ProjectsWrite)).Get("/repositories", h.repositories)
	gh.With(httpx.RequirePermissions(permission.ProjectsWrite)).Get("/installations/{installation}/repositories/{repository}/branches", h.branches)
	gh.With(httpx.RequirePermissions(permission.ProjectsWrite)).Get("/installations/{installation}/repositories/{repository}/services", h.services)

	repo := r.Group("/projects/{id}/repository")
	repo.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/", h.connection)
	repo.With(httpx.RequirePermissions(permission.ProjectsWrite, permission.DeploymentsCreate)).Put("/", h.connect)
	repo.With(httpx.RequirePermissions(permission.ProjectsWrite)).Delete("/", h.disconnect)
	repo.With(httpx.RequirePermissions(permission.DeploymentsCreate)).Post("/publish", h.publish)
	repo.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/pushes", h.pushes)
}

// RegisterPublic mounts PublicRoutes on the app's root, outside auth.
func (h *Handler) RegisterPublic(r *httpx.Router) {
	r.Get("/github/setup", h.setup)
	r.Post("/webhooks/github", h.webhook)
}

// webhook receives GitHub's deliveries. The signature over the exact body is
// the only proof (checked by the service before anything else); a delivery
// seen before is accepted too, so GitHub does not redeliver it.
func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) error {
	// the exact buffered body: the signature is computed over these bytes
	payload, err := httpx.Body(r)
	if err != nil {
		return err
	}
	err = h.commands.Receive(r.Context(), source.Webhook{
		Delivery:  r.Header.Get("X-GitHub-Delivery"),
		Event:     r.Header.Get("X-GitHub-Event"),
		Signature: r.Header.Get("X-Hub-Signature-256"),
		Payload:   payload,
	})
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, httpx.Map{"status": "accepted"})
}

// ---- installations ----

func (h *Handler) info(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, h.queries.Info(r.Context()))
}

func (h *Handler) installURL(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	u, err := h.commands.InstallURL(r.Context(), org, user, r.URL.Query().Get("return_to"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"url": u})
}

func (h *Handler) installations(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	items, err := h.queries.ListInstallations(r.Context(), org)
	if err != nil {
		return err
	}
	if items == nil {
		items = []source.Installation{}
	}
	return httpx.JSON(w, httpx.Map{"items": items})
}

func (h *Handler) claim(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	var body source.ClaimInstallation
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	out, err := h.commands.Claim(r.Context(), org, user, body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

func (h *Handler) unlink(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	id, err := source.ParseInstallationID(r.PathValue("installation"))
	if err != nil {
		return err
	}
	if err := h.commands.Unlink(r.Context(), org, id, source.Unlink{UnlinkedBy: user}); err != nil {
		return err
	}
	return httpx.Status(w, http.StatusNoContent)
}

func (h *Handler) repositories(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.ListRepositories(r.Context(), org, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

func (h *Handler) branches(w http.ResponseWriter, r *http.Request) error {
	org, id, repo, err := repositoryParams(r)
	if err != nil {
		return err
	}
	items, err := h.queries.ListBranches(r.Context(), org, id, repo)
	if err != nil {
		return err
	}
	if items == nil {
		items = []source.Branch{}
	}
	return httpx.JSON(w, httpx.Map{"items": items})
}

// services proposes the services of a repository's branch (MS-5).
func (h *Handler) services(w http.ResponseWriter, r *http.Request) error {
	org, id, repo, err := repositoryParams(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	out, err := h.queries.DetectServices(r.Context(), org, id, repo, q.Get("branch"), q.Get("root"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// repositoryParams are the organization (from the token) and the
// installation and repository of a /github/installations/... path.
func repositoryParams(r *http.Request) (identity.OrganizationID, source.InstallationID, int64, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return identity.OrganizationID{}, source.InstallationID{}, 0, err
	}
	id, err := source.ParseInstallationID(r.PathValue("installation"))
	if err != nil {
		return identity.OrganizationID{}, source.InstallationID{}, 0, err
	}
	repo, err := strconv.ParseInt(r.PathValue("repository"), 10, 64)
	if err != nil || repo <= 0 {
		return identity.OrganizationID{}, source.InstallationID{}, 0, errx.InvalidField("repository", "repository must be GitHub's numeric repository id")
	}
	return org, id, repo, nil
}

// setup is where GitHub sends the browser after installing the App. It
// always redirects to the console: the outcome travels in the query.
func (h *Handler) setup(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	settings := h.console + source.SettingsPage
	if q.Get("setup_action") == "request" && q.Get("installation_id") == "" {
		// an organization owner on GitHub must approve the installation first
		return httpx.Redirect(w, settings+"?github=requested", http.StatusFound)
	}
	ext, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	out, err := h.commands.Setup(r.Context(), q.Get("state"), source.Link{ExternalID: ext, Code: q.Get("code")})
	if err != nil {
		return httpx.Redirect(w, settings+"?github_error="+url.QueryEscape(setupCode(err)), http.StatusFound)
	}
	// The callback is public: it links nothing. The console page posts the
	// claim (logged in) and the return path travels inside it.
	return httpx.Redirect(w, settings+"?github_claim="+url.QueryEscape(out.Claim), http.StatusFound)
}

// setupCode is the code the console explains (internal ones stay "INTERNAL").
func setupCode(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		switch {
		case source.Known(e.Code):
			return e.Code
		case e.Type == errx.TypeValidation:
			return source.CodeStateInvalid // a hand-made or truncated callback URL
		}
	}
	return source.CodeInternal
}

// ---- connections ----

func (h *Handler) connection(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.FindConnection(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// connectBody is the body of PUT /projects/{id}/repository (the release
// configuration inline, as in a deployment).
type connectBody struct {
	Installation source.InstallationID `json:"installation_id"`
	RepositoryID int64                 `json:"repository_id"`
	Branch       string                `json:"branch"`
	Root         string                `json:"root"`
	release.Config
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var body connectBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	out, err := h.commands.Connect(r.Context(), org, id, source.Connect{Installation: body.Installation,
		RepositoryID: body.RepositoryID, Branch: body.Branch, Root: body.Root, Config: body.Config, ConnectedBy: user})
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Disconnect(r.Context(), org, id, source.Disconnect{DisconnectedBy: user}); err != nil {
		return err
	}
	return httpx.Status(w, http.StatusNoContent)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var body source.Publish
	raw, err := httpx.Body(r)
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			return err
		}
	}
	out, err := h.commands.Publish(r.Context(), org, id, source.Publish{Rebuild: body.Rebuild, RequestedBy: user})
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

func (h *Handler) pushes(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	status, err := httpx.EnumFilter(r, "status", source.PushStatus.Valid,
		"status must be queued, importing, deployed, failed or skipped")
	if err != nil {
		return err
	}
	out, err := h.queries.ListPushes(r.Context(), org, id, source.PushFilter{Status: status}, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}
