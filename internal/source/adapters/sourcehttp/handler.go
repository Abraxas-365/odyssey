// Package sourcehttp exposes the GitHub integration: installations of the
// App, the repositories they reach, each project's connected repository
// and its pushes (docs/api.md), plus the provider's public callbacks.
//
// It is the only HTTP adapter that mounts routes outside the authenticated
// /api/v1 group, and only the ones in PublicRoutes.
package sourcehttp

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/gofiber/fiber/v2"
)

// PublicRoutes are the unauthenticated routes (the provider calls them, or
// sends the browser to them); each checks its own proof.
var PublicRoutes = []string{"GET /github/setup"}

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
func (h *Handler) Register(r fiber.Router) {
	gh := r.Group("/github")
	gh.Get("", httpx.RequirePermissions(permission.ProjectsRead), h.info)
	gh.Get("/install", httpx.RequirePermissions(permission.IntegrationsManage), h.installURL)
	gh.Get("/installations", httpx.RequirePermissions(permission.ProjectsRead), h.installations)
	gh.Post("/installations", httpx.RequirePermissions(permission.IntegrationsManage), h.claim)
	gh.Delete("/installations/:installation", httpx.RequirePermissions(permission.IntegrationsManage), h.unlink)
	gh.Get("/repositories", httpx.RequirePermissions(permission.ProjectsWrite), h.repositories)
	gh.Get("/installations/:installation/repositories/:repository/branches", httpx.RequirePermissions(permission.ProjectsWrite), h.branches)

	repo := r.Group("/projects/:id/repository")
	repo.Get("", httpx.RequirePermissions(permission.DeploymentsRead), h.connection)
	repo.Put("", httpx.RequirePermissions(permission.ProjectsWrite, permission.DeploymentsCreate), h.connect)
	repo.Delete("", httpx.RequirePermissions(permission.ProjectsWrite), h.disconnect)
	repo.Post("/publish", httpx.RequirePermissions(permission.DeploymentsCreate), h.publish)
	repo.Get("/pushes", httpx.RequirePermissions(permission.DeploymentsRead), h.pushes)
}

// RegisterPublic mounts PublicRoutes on the app's root, outside auth.
func (h *Handler) RegisterPublic(r fiber.Router) {
	r.Get("/github/setup", h.setup)
}

// ---- installations ----

func (h *Handler) info(c *fiber.Ctx) error {
	return c.JSON(h.queries.Info(c.UserContext()))
}

func (h *Handler) installURL(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	u, err := h.commands.InstallURL(c.UserContext(), org, user, c.Query("return_to"))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"url": u})
}

func (h *Handler) installations(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	items, err := h.queries.ListInstallations(c.UserContext(), org)
	if err != nil {
		return err
	}
	if items == nil {
		items = []source.Installation{}
	}
	return c.JSON(fiber.Map{"items": items})
}

func (h *Handler) claim(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	var body source.ClaimInstallation
	if err := httpx.DecodeJSON(c, &body); err != nil {
		return err
	}
	out, err := h.commands.Claim(c.UserContext(), org, user, body)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) unlink(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	id, err := source.ParseInstallationID(c.Params("installation"))
	if err != nil {
		return err
	}
	if err := h.commands.Unlink(c.UserContext(), org, id, source.Unlink{UnlinkedBy: user}); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) repositories(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.ListRepositories(c.UserContext(), org, page)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) branches(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := source.ParseInstallationID(c.Params("installation"))
	if err != nil {
		return err
	}
	repo, err := strconv.ParseInt(c.Params("repository"), 10, 64)
	if err != nil || repo <= 0 {
		return errx.InvalidField("repository", "repository must be GitHub's numeric repository id")
	}
	items, err := h.queries.ListBranches(c.UserContext(), org, id, repo)
	if err != nil {
		return err
	}
	if items == nil {
		items = []source.Branch{}
	}
	return c.JSON(fiber.Map{"items": items})
}

// setup is where GitHub sends the browser after installing the App. It
// always redirects to the console: the outcome travels in the query.
func (h *Handler) setup(c *fiber.Ctx) error {
	settings := h.console + source.SettingsPage
	if c.Query("setup_action") == "request" && c.Query("installation_id") == "" {
		// an organization owner on GitHub must approve the installation first
		return c.Redirect(settings+"?github=requested", fiber.StatusFound)
	}
	ext, _ := strconv.ParseInt(c.Query("installation_id"), 10, 64)
	out, err := h.commands.Setup(c.UserContext(), c.Query("state"), source.Link{ExternalID: ext, Code: c.Query("code")})
	if err != nil {
		return c.Redirect(settings+"?github_error="+url.QueryEscape(setupCode(err)), fiber.StatusFound)
	}
	// The callback is public: it links nothing. The console page posts the
	// claim (logged in) and the return path travels inside it.
	return c.Redirect(settings+"?github_claim="+url.QueryEscape(out.Claim), fiber.StatusFound)
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

func (h *Handler) connection(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.FindConnection(c.UserContext(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// connectBody is the body of PUT /projects/:id/repository (the release
// configuration inline, as in a deployment).
type connectBody struct {
	Installation source.InstallationID `json:"installation_id"`
	RepositoryID int64                 `json:"repository_id"`
	Branch       string                `json:"branch"`
	Root         string                `json:"root"`
	release.Config
}

func (h *Handler) connect(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	var body connectBody
	if err := httpx.DecodeJSON(c, &body); err != nil {
		return err
	}
	out, err := h.commands.Connect(c.UserContext(), org, id, source.Connect{Installation: body.Installation,
		RepositoryID: body.RepositoryID, Branch: body.Branch, Root: body.Root, Config: body.Config, ConnectedBy: user})
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) disconnect(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Disconnect(c.UserContext(), org, id, source.Disconnect{DisconnectedBy: user}); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) publish(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	out, err := h.commands.Publish(c.UserContext(), org, id, source.Publish{RequestedBy: user})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

func (h *Handler) pushes(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	status, err := httpx.EnumFilter(c, "status", source.PushStatus.Valid,
		"status must be queued, importing, deployed, failed or skipped")
	if err != nil {
		return err
	}
	out, err := h.queries.ListPushes(c.UserContext(), org, id, source.PushFilter{Status: status}, page)
	if err != nil {
		return err
	}
	return c.JSON(out)
}
