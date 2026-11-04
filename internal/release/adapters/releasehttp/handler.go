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
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	commands release.Commands
	queries  release.Queries
	opts     Options
}

// Options are what the deployment decides for the handler.
type Options struct {
	// AccessLoginAvailable: this control plane serves the sign-in endpoints
	// apps with a protected access mode rely on (/access/*).
	AccessLoginAvailable bool
}

func New(commands release.Commands, queries release.Queries, opts Options) *Handler {
	return &Handler{commands: commands, queries: queries, opts: opts}
}

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	env := r.Group("/projects/:id/environments/:environment")
	env.Get("/current", httpx.RequirePermissions(permission.DeploymentsRead), h.current)
	env.Get("/deployments", httpx.RequirePermissions(permission.DeploymentsRead), h.list)
	env.Post("/deployments", httpx.RequirePermissions(permission.DeploymentsCreate), h.deploy)
	env.Get("/deployments/:deployment", httpx.RequirePermissions(permission.DeploymentsRead), h.find)
	env.Post("/deployments/:deployment/rollback", httpx.RequirePermissions(permission.DeploymentsRollback), h.rollback)
	env.Get("/settings", httpx.RequirePermissions(permission.ProjectsRead), h.settings)
	env.Put("/access", httpx.RequirePermissions(permission.ProjectsWrite), h.setAccess)
	env.Patch("/env", httpx.RequirePermissions(permission.ProjectsWrite), h.setEnv)
	env.Put("/secrets/:name", httpx.RequirePermissions(permission.ProjectsWrite), h.putSecret)
	env.Delete("/secrets/:name", httpx.RequirePermissions(permission.ProjectsWrite), h.removeSecret)
	env.Put("/secrets/:name/value", httpx.RequirePermissions(permission.ProjectsWrite), h.loadSecret)
	r.Get("/projects/:id/releases/:release", httpx.RequirePermissions(permission.DeploymentsRead), h.findRelease)
}

// RegisterPublic mounts the unauthenticated routes on the app's root: the
// upload ticket in the path is the only credential.
func (h *Handler) RegisterPublic(r fiber.Router) { r.Put("/uploads/:ticket", h.upload) }

// upload redeems an upload ticket (deploy_folder's command): the body is the
// folder as a raw tar.gz, streamed to storage. The ticket is spent even if
// the archive is refused; a fresh one is the retry.
func (h *Handler) upload(c *fiber.Ctx) error {
	body, err := httpx.StreamBody(c, build.MaxSourceBytes)
	if err != nil {
		return err
	}
	d, err := h.commands.RedeemUpload(httpx.PublicContext(c), c.Params("ticket"), body)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"deployment_id": d.ID,
		"status":        d.Status,
		"next_step":     "Sigue la publicación con get_deployment (app y wait_seconds).",
	})
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

// settingsView is the settings of an environment as the API shows them.
type settingsView struct {
	release.Settings
	// Secrets replace the settings' references with their state against the
	// cluster, and add the open requests.
	Secrets []release.SecretView `json:"secrets"`
	// SecretKey is the cluster key a managed value is sealed to in the
	// browser (null: the cluster cannot take managed secrets yet).
	SecretKey *release.ClusterKey `json:"secret_key"`
	// ExternalSecrets is true when the cluster has the External Secrets
	// Operator; SecretStores are its ClusterSecretStores.
	ExternalSecrets bool     `json:"external_secrets"`
	SecretStores    []string `json:"secret_stores"`
	// SecretsClusterRevoked: the environment's cluster was revoked, which is
	// why it offers no key nor stores (IT moves the environment).
	SecretsClusterRevoked bool `json:"secrets_cluster_revoked"`
	// AccessLoginAvailable is false when this control plane cannot serve the
	// sign-in of team and organization apps (no signing key or login client).
	AccessLoginAvailable bool `json:"access_login_available"`
	// AccessProtectionAvailable is false when the environment's cluster cannot
	// protect apps yet (its agent lacks the access capability): team and
	// organization are refused there until IT enables it; public still works.
	// An unplaced environment is judged by the organization's default cluster.
	AccessProtectionAvailable bool `json:"access_protection_available"`
}

func (h *Handler) settings(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	at, err := parsePlace(c)
	if err != nil {
		return err
	}
	ctx := c.UserContext()
	out, err := h.queries.Settings(ctx, org, at.project, at.environment)
	if err != nil {
		return err
	}
	can, err := h.queries.AccessProtectionAvailable(ctx, org, at.project, at.environment)
	if err != nil {
		return err
	}
	secrets, err := h.queries.Secrets(ctx, org, at.project, at.environment)
	if err != nil {
		return err
	}
	return c.JSON(settingsView{Settings: out, Secrets: secrets.Items, SecretKey: secrets.Cluster.Key,
		ExternalSecrets: secrets.Cluster.External, SecretStores: secrets.Cluster.Stores, SecretsClusterRevoked: secrets.Cluster.Revoked,
		AccessLoginAvailable: h.opts.AccessLoginAvailable, AccessProtectionAvailable: can})
}

// settingsChanged answers a settings change with 202 (the config deployment,
// when one starts, runs in the worker) and points Location at it.
func settingsChanged(c *fiber.Ctx, out release.SettingsChange, levels int) error {
	if out.Deployment != nil {
		base := strings.TrimRight(c.Path(), "/")
		for range levels {
			base = base[:strings.LastIndex(base, "/")]
		}
		c.Set(fiber.HeaderLocation, base+"/deployments/"+out.Deployment.ID.String())
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

// actor is the caller's organization, user and place.
func actor(c *fiber.Ctx) (identity.OrganizationID, identity.UserID, place, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return identity.OrganizationID{}, identity.UserID{}, place{}, err
	}
	user, err := httpx.User(c)
	if err != nil {
		return identity.OrganizationID{}, identity.UserID{}, place{}, err
	}
	at, err := parsePlace(c)
	return org, user, at, err
}

func (h *Handler) setEnv(c *fiber.Ctx) error {
	org, user, at, err := actor(c)
	if err != nil {
		return err
	}
	var in release.SetEnv
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.RequestedBy = user
	out, err := h.commands.SetEnv(c.UserContext(), org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	return settingsChanged(c, out, 1)
}

// putSecret declares a secret: managed (a request for a person to load its
// value) or external (read from the company's vault, a permission of its own).
func (h *Handler) putSecret(c *fiber.Ctx) error {
	org, user, at, err := actor(c)
	if err != nil {
		return err
	}
	var body struct {
		Source   release.SecretSource `json:"source"`
		Note     string               `json:"note"`
		Store    string               `json:"store"`
		Key      string               `json:"key"`
		Property string               `json:"property"`
	}
	if err := httpx.DecodeJSON(c, &body); err != nil {
		return err
	}
	name := c.Params("name")
	switch body.Source {
	case release.SecretManaged:
		v, err := h.commands.RequestSecret(c.UserContext(), org, at.project, at.environment, release.RequestSecret{Name: name, Note: body.Note, RequestedBy: user})
		if err != nil {
			return err
		}
		return c.JSON(v)
	case release.SecretExternal:
		if !httpx.Can(c, permission.SecretsExternal) {
			return errx.Forbidden("reading a secret from the company's vault needs the permission " + permission.SecretsExternal)
		}
		out, err := h.commands.SetExternalSecret(c.UserContext(), org, at.project, at.environment, release.SetExternalSecret{Name: name,
			Store: body.Store, Key: body.Key, Property: body.Property, RequestedBy: user})
		if err != nil {
			return err
		}
		return settingsChanged(c, out, 2)
	}
	return errx.InvalidField("source", "source is managed (a person loads the value) or external (the company's vault)")
}

// loadSecret takes a managed value sealed in the browser: the API never sees
// it in the clear.
func (h *Handler) loadSecret(c *fiber.Ctx) error {
	org, user, at, err := actor(c)
	if err != nil {
		return err
	}
	var in release.LoadSecret
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	in.Name, in.RequestedBy, in.MayChangeExternal = c.Params("name"), user, httpx.Can(c, permission.SecretsExternal)
	out, err := h.commands.LoadSecret(c.UserContext(), org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	return settingsChanged(c, out, 3)
}

func (h *Handler) removeSecret(c *fiber.Ctx) error {
	org, user, at, err := actor(c)
	if err != nil {
		return err
	}
	out, err := h.commands.RemoveSecret(c.UserContext(), org, at.project, at.environment, release.RemoveSecret{Name: c.Params("name"), RequestedBy: user,
		MayChangeExternal: httpx.Can(c, permission.SecretsExternal)})
	if err != nil {
		return err
	}
	return settingsChanged(c, out, 2)
}

// setAccess answers 202: the config deployment (when one starts) runs in the
// worker. Making an app public takes a permission of its own.
func (h *Handler) setAccess(c *fiber.Ctx) error {
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
	var body struct {
		Mode release.AccessMode `json:"mode"`
	}
	if err := httpx.DecodeJSON(c, &body); err != nil {
		return err
	}
	if body.Mode == release.AccessPublic && !httpx.Can(c, permission.AccessPublic) {
		return errx.Forbidden("making an app public needs the permission " + permission.AccessPublic)
	}
	out, err := h.commands.SetAccess(c.UserContext(), org, at.project, at.environment, release.SetAccess{Mode: body.Mode, RequestedBy: user})
	if err != nil {
		return err
	}
	if out.Deployment != nil { // a sibling of /access, like rollback's
		base := strings.TrimRight(c.Path(), "/")
		c.Set(fiber.HeaderLocation, base[:strings.LastIndex(base, "/")+1]+"deployments/"+out.Deployment.ID.String())
	}
	return c.Status(fiber.StatusAccepted).JSON(struct {
		release.AccessChange
		AccessLoginAvailable bool `json:"access_login_available"`
	}{out, h.opts.AccessLoginAvailable})
}
