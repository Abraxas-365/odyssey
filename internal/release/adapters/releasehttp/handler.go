// Package releasehttp exposes what runs in each environment: deployments
// (publish, progress, history, rollback) and releases (docs/api.md).
package releasehttp

import (
	"errors"
	"io"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands release.Commands
	queries  release.Queries
}

func New(commands release.Commands, queries release.Queries) *Handler {
	return &Handler{commands: commands, queries: queries}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	env := r.Group("/projects/:id/environments/:environment")
	env.Get("/current", httpx.RequirePermissions(permission.DeploymentsRead), h.current)
	env.Get("/deployments", httpx.RequirePermissions(permission.DeploymentsRead), h.list)
	env.Post("/deployments", httpx.RequirePermissions(permission.DeploymentsCreate), h.deploy)
	env.Get("/deployments/:deployment", httpx.RequirePermissions(permission.DeploymentsRead), h.find)
	env.Post("/deployments/:deployment/rollback", httpx.RequirePermissions(permission.DeploymentsRollback), h.rollback)
	r.Get("/projects/:id/releases/:release", httpx.RequirePermissions(permission.DeploymentsRead), h.findRelease)
}

type place struct {
	project     project.ID
	environment project.EnvironmentID
}

func parsePlace(c *fiber.Ctx) (place, error) {
	p, err := project.ParseID(c.Params("id"))
	if err != nil {
		return place{}, err
	}
	e, err := project.ParseEnvironmentID(c.Params("environment"))
	if err != nil {
		return place{}, err
	}
	return place{project: p, environment: e}, nil
}

func (h *Handler) current(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Current(c.UserContext(), org, at.project, at.environment)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	var filter release.DeploymentFilter
	if filter.Status, err = httpx.EnumFilter(c, "status", release.Status.Valid, release.StatusMessage); err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.ListDeployments(c.UserContext(), org, at.project, at.environment, filter, page)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// deploy answers 202: the deployment runs in the worker; poll its URL. A
// JSON body publishes an image; a multipart one publishes a folder (see
// deploySource).
func (h *Handler) deploy(c *fiber.Ctx) error {
	if httpx.IsMultipart(c) {
		return h.deploySource(c)
	}
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	var input release.Deploy
	if err := httpx.DecodeJSON(c, &input); err != nil {
		return err
	}
	input.RequestedBy = user
	d, err := h.commands.Deploy(c.UserContext(), org, at.project, at.environment, input)
	if err != nil {
		return err
	}
	httpx.SetLocation(c, d.ID.String())
	return c.Status(fiber.StatusAccepted).JSON(d)
}

// Limits of a deployment from source: the archive plus a small form.
const (
	maxSourceUpload = build.MaxSourceBytes + 1<<20
	maxConfigField  = 256 << 10
)

// deploySource publishes a folder: multipart/form-data with a `config` part
// (the JSON configuration, first) and a `source` part (the folder as
// tar.gz), streamed to storage as it arrives.
func (h *Handler) deploySource(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	up, err := httpx.NewUpload(c, maxSourceUpload)
	if err != nil {
		return err
	}
	part, err := up.Next()
	if errors.Is(err, io.EOF) || (err == nil && part.FormName() != "config") {
		return errx.InvalidField("config", "the first part must be config (the JSON configuration), then source")
	}
	if err != nil {
		return err
	}
	raw, err := up.ReadSmall(part, maxConfigField)
	if err != nil {
		return err
	}
	var input release.DeploySource
	if err := httpx.DecodeStrict(raw, &input); err != nil {
		return err
	}
	part, err = up.Next()
	if errors.Is(err, io.EOF) || (err == nil && part.FormName() != "source") {
		return errx.InvalidField("source", "source (the folder as tar.gz) is required after config")
	}
	if err != nil {
		return err
	}
	if input.Name == "" {
		input.Name = part.FileName()
	}
	input.RequestedBy = user
	d, err := h.commands.DeploySource(c.UserContext(), org, at.project, at.environment, input, readerOf(up, part))
	if err != nil {
		return err
	}
	httpx.SetLocation(c, d.ID.String())
	return c.Status(fiber.StatusAccepted).JSON(d)
}

// readerOf words the transport's errors on the way (the body over its
// limit is a 413; a broken stream, a malformed body).
func readerOf(up *httpx.Upload, part io.Reader) io.Reader { return failing{up: up, r: part} }

type failing struct {
	up *httpx.Upload
	r  io.Reader
}

func (f failing) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, f.up.Failure(err)
	}
	return n, err
}

func (h *Handler) find(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(c.Params("deployment"))
	if err != nil {
		return err
	}
	d, err := h.queries.FindDeployment(c.UserContext(), org, at.project, at.environment, id)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// rollback takes no body: it deploys again the release of :deployment.
func (h *Handler) rollback(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	user, err := httpx.User(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(c.Params("deployment"))
	if err != nil {
		return err
	}
	d, err := h.commands.Rollback(c.UserContext(), org, at.project, at.environment, id, release.Rollback{RequestedBy: user})
	if err != nil {
		return err
	}
	// Location is the new deployment, a sibling of :deployment.
	base := strings.TrimSuffix(strings.TrimRight(c.Path(), "/"), "/rollback")
	c.Set(fiber.HeaderLocation, base[:strings.LastIndex(base, "/")+1]+d.ID.String())
	return c.Status(fiber.StatusAccepted).JSON(d)
}

func (h *Handler) findRelease(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	p, err := project.ParseID(c.Params("id"))
	if err != nil {
		return err
	}
	id, err := release.ParseID(c.Params("release"))
	if err != nil {
		return err
	}
	r, err := h.queries.FindRelease(c.UserContext(), org, p, id)
	if err != nil {
		return err
	}
	return c.JSON(r)
}
