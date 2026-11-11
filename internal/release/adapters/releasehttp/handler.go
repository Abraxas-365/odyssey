// Package releasehttp exposes what runs in each environment: deployments
// (publish, progress, history, rollback) and releases (docs/api.md).
package releasehttp

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
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
func (h *Handler) Register(r *httpx.Router) {
	env := r.Group("/projects/{id}/environments/{environment}")
	env.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/current", h.current)
	env.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/services", h.services)
	env.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/deployments", h.list)
	env.With(httpx.RequirePermissions(permission.DeploymentsCreate)).Post("/deployments", h.deploy)
	env.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/deployments/{deployment}", h.find)
	env.With(httpx.RequirePermissions(permission.DeploymentsRollback)).Post("/deployments/{deployment}/rollback", h.rollback)
	env.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/settings", h.settings)
	env.With(httpx.RequirePermissions(permission.ProjectsRead)).Get("/uses", h.uses)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/access", h.setAccess)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Patch("/env", h.setEnv)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/secrets/{name}", h.putSecret)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Delete("/secrets/{name}", h.removeSecret)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/secrets/{name}/value", h.loadSecret)
	env.With(httpx.RequirePermissions(permission.ProjectsWrite)).Put("/secrets/{name}/services", h.scopeSecret)
	r.With(httpx.RequirePermissions(permission.DeploymentsRead)).Get("/projects/{id}/releases/{release}", h.findRelease)
}

// RegisterPublic mounts the unauthenticated routes on the app's root: the
// upload ticket in the path is the only credential.
func (h *Handler) RegisterPublic(r *httpx.Router) { r.Put("/uploads/{ticket}", h.upload) }

// upload redeems an upload ticket (deploy_folder's command): the body is the
// folder as a raw tar.gz, streamed to storage. The ticket is spent even if
// the archive is refused; a fresh one is the retry.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.StreamBody(w, r, build.MaxSourceBytes)
	if err != nil {
		return err
	}
	d, err := h.commands.RedeemUpload(httpx.PublicContext(w, r), r.PathValue("ticket"), body)
	if err != nil {
		return err
	}
	return httpx.JSONStatus(w, http.StatusAccepted, httpx.Map{
		"deployment_id": d.ID,
		"status":        d.Status,
		"next_step":     "Sigue la publicación con get_deployment (app y wait_seconds).",
	})
}

type place struct {
	project     project.ID
	environment project.EnvironmentID
}

func parsePlace(r *http.Request) (place, error) {
	p, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return place{}, err
	}
	e, err := project.ParseEnvironmentID(r.PathValue("environment"))
	if err != nil {
		return place{}, err
	}
	return place{project: p, environment: e}, nil
}

func (h *Handler) current(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Current(r.Context(), org, at.project, at.environment)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// services answers {items}: the services of the release the environment
// serves and how its cluster sees each.
func (h *Handler) services(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	items, err := h.queries.ServiceStates(r.Context(), org, at.project, at.environment)
	if err != nil {
		return err
	}
	return httpx.JSON(w, httpx.Map{"items": items})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	var filter release.DeploymentFilter
	if filter.Status, err = httpx.EnumFilter(r, "status", release.Status.Valid, release.StatusMessage); err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.ListDeployments(r.Context(), org, at.project, at.environment, filter, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// deploy answers 202: the deployment runs in the worker; poll its URL. A
// JSON body publishes an image; a multipart one publishes a folder (see
// deploySource).
func (h *Handler) deploy(w http.ResponseWriter, r *http.Request) error {
	if httpx.IsMultipart(r) {
		return h.deploySource(w, r)
	}
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	var input release.Deploy
	if err := httpx.DecodeJSON(r, &input); err != nil {
		return err
	}
	input.RequestedBy = user
	d, err := h.commands.Deploy(r.Context(), org, at.project, at.environment, input)
	if err != nil {
		return err
	}
	httpx.SetLocation(w, r, d.ID.String())
	return httpx.JSONStatus(w, http.StatusAccepted, d)
}

// Limits of a deployment from source: the archive plus a small form.
const (
	maxSourceUpload = build.MaxSourceBytes + 1<<20
	maxConfigField  = 256 << 10
)

// deploySource publishes a folder: multipart/form-data with a `config` part
// (the JSON configuration, first) and a `source` part (the folder as
// tar.gz), streamed to storage as it arrives.
func (h *Handler) deploySource(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	up, err := httpx.NewUpload(w, r, maxSourceUpload)
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
	d, err := h.commands.DeploySource(r.Context(), org, at.project, at.environment, input, readerOf(up, part))
	if err != nil {
		return err
	}
	httpx.SetLocation(w, r, d.ID.String())
	return httpx.JSONStatus(w, http.StatusAccepted, d)
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

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(r.PathValue("deployment"))
	if err != nil {
		return err
	}
	d, err := h.queries.FindDeployment(r.Context(), org, at.project, at.environment, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, d)
}

// rollback takes no body: it deploys again the release of {deployment}.
func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	id, err := release.ParseDeploymentID(r.PathValue("deployment"))
	if err != nil {
		return err
	}
	d, err := h.commands.Rollback(r.Context(), org, at.project, at.environment, id, release.Rollback{RequestedBy: user})
	if err != nil {
		return err
	}
	// Location is the new deployment, a sibling of {deployment}.
	base := strings.TrimSuffix(strings.TrimRight(r.URL.Path, "/"), "/rollback")
	w.Header().Set("Location", base[:strings.LastIndex(base, "/")+1]+d.ID.String())
	return httpx.JSONStatus(w, http.StatusAccepted, d)
}

func (h *Handler) findRelease(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	p, err := project.ParseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	id, err := release.ParseID(r.PathValue("release"))
	if err != nil {
		return err
	}
	rel, err := h.queries.FindRelease(r.Context(), org, p, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, rel)
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
	// ScopedSettings is false when the environment's cluster cannot give a
	// setting to some services only (its agent lacks
	// odicea.service-settings): every setting then reaches the whole app.
	ScopedSettings bool `json:"scoped_settings"`
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
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
	return httpx.JSON(w, settingsView{Settings: out, Secrets: secrets.Items, SecretKey: secrets.Cluster.Key,
		ExternalSecrets: secrets.Cluster.External, SecretStores: secrets.Cluster.Stores, SecretsClusterRevoked: secrets.Cluster.Revoked,
		AccessLoginAvailable: h.opts.AccessLoginAvailable, AccessProtectionAvailable: can, ScopedSettings: secrets.Cluster.ScopedSettings})
}

// uses answers "lo que usa esta app": what the app depends on and what needs
// attention (projects:read).
func (h *Handler) uses(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Uses(r.Context(), org, at.project, at.environment)
	if err != nil {
		return err
	}
	return httpx.JSON(w, out)
}

// settingsChanged answers a settings change with 202 (the config deployment,
// when one starts, runs in the worker) and points Location at it.
func settingsChanged(w http.ResponseWriter, r *http.Request, out release.SettingsChange, levels int) error {
	if out.Deployment != nil {
		base := strings.TrimRight(r.URL.Path, "/")
		for range levels {
			base = base[:strings.LastIndex(base, "/")]
		}
		w.Header().Set("Location", base+"/deployments/"+out.Deployment.ID.String())
	}
	return httpx.JSONStatus(w, http.StatusAccepted, out)
}

// actor is the caller's organization, user and place.
func actor(r *http.Request) (identity.OrganizationID, identity.UserID, place, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return identity.OrganizationID{}, identity.UserID{}, place{}, err
	}
	user, err := httpx.User(r)
	if err != nil {
		return identity.OrganizationID{}, identity.UserID{}, place{}, err
	}
	at, err := parsePlace(r)
	return org, user, at, err
}

func (h *Handler) setEnv(w http.ResponseWriter, r *http.Request) error {
	org, user, at, err := actor(r)
	if err != nil {
		return err
	}
	var in release.SetEnv
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.RequestedBy = user
	out, err := h.commands.SetEnv(r.Context(), org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	return settingsChanged(w, r, out, 1)
}

// putSecret declares a secret: managed (a request for a person to load its
// value) or external (read from the company's vault, a permission of its own).
func (h *Handler) putSecret(w http.ResponseWriter, r *http.Request) error {
	org, user, at, err := actor(r)
	if err != nil {
		return err
	}
	var body struct {
		Source   release.SecretSource `json:"source"`
		Note     string               `json:"note"`
		Store    string               `json:"store"`
		Key      string               `json:"key"`
		Property string               `json:"property"`
		Services *release.Scope       `json:"services"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	name := r.PathValue("name")
	switch body.Source {
	case release.SecretManaged:
		v, err := h.commands.RequestSecret(r.Context(), org, at.project, at.environment, release.RequestSecret{Name: name, Note: body.Note,
			Services: body.Services, RequestedBy: user})
		if err != nil {
			return err
		}
		return httpx.JSON(w, v)
	case release.SecretExternal:
		if !httpx.Can(r, permission.SecretsExternal) {
			return errx.Forbidden("reading a secret from the company's vault needs the permission " + permission.SecretsExternal)
		}
		out, err := h.commands.SetExternalSecret(r.Context(), org, at.project, at.environment, release.SetExternalSecret{Name: name,
			Store: body.Store, Key: body.Key, Property: body.Property, Services: body.Services, RequestedBy: user})
		if err != nil {
			return err
		}
		return settingsChanged(w, r, out, 2)
	}
	return errx.InvalidField("source", "source is managed (a person loads the value) or external (the company's vault)")
}

// scopeSecret changes which services read a secret (or will read the one
// still asked for), without writing its value again (MS-4).
func (h *Handler) scopeSecret(w http.ResponseWriter, r *http.Request) error {
	org, user, at, err := actor(r)
	if err != nil {
		return err
	}
	var in release.ScopeSecret
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.Name, in.RequestedBy, in.MayChangeExternal = r.PathValue("name"), user, httpx.Can(r, permission.SecretsExternal)
	out, err := h.commands.ScopeSecret(r.Context(), org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	return settingsChanged(w, r, out, 3)
}

// loadSecret takes a managed value sealed in the browser: the API never sees
// it in the clear.
func (h *Handler) loadSecret(w http.ResponseWriter, r *http.Request) error {
	org, user, at, err := actor(r)
	if err != nil {
		return err
	}
	var in release.LoadSecret
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.Name, in.RequestedBy, in.MayChangeExternal = r.PathValue("name"), user, httpx.Can(r, permission.SecretsExternal)
	out, err := h.commands.LoadSecret(r.Context(), org, at.project, at.environment, in)
	if err != nil {
		return err
	}
	return settingsChanged(w, r, out, 3)
}

func (h *Handler) removeSecret(w http.ResponseWriter, r *http.Request) error {
	org, user, at, err := actor(r)
	if err != nil {
		return err
	}
	out, err := h.commands.RemoveSecret(r.Context(), org, at.project, at.environment, release.RemoveSecret{Name: r.PathValue("name"), RequestedBy: user,
		MayChangeExternal: httpx.Can(r, permission.SecretsExternal)})
	if err != nil {
		return err
	}
	return settingsChanged(w, r, out, 2)
}

// setAccess answers 202: the config deployment (when one starts) runs in the
// worker. Making an app public takes a permission of its own.
func (h *Handler) setAccess(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	user, err := httpx.User(r)
	if err != nil {
		return err
	}
	at, err := parsePlace(r)
	if err != nil {
		return err
	}
	var body struct {
		Mode release.AccessMode `json:"mode"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	if body.Mode == release.AccessPublic && !httpx.Can(r, permission.AccessPublic) {
		return errx.Forbidden("making an app public needs the permission " + permission.AccessPublic)
	}
	out, err := h.commands.SetAccess(r.Context(), org, at.project, at.environment, release.SetAccess{Mode: body.Mode, RequestedBy: user})
	if err != nil {
		return err
	}
	if done, err := httpx.Held(w, out.Outcome); done {
		return err
	}
	if out.Deployment != nil { // a sibling of /access, like rollback's
		base := strings.TrimRight(r.URL.Path, "/")
		w.Header().Set("Location", base[:strings.LastIndex(base, "/")+1]+"deployments/"+out.Deployment.ID.String())
	}
	return httpx.JSONStatus(w, http.StatusAccepted, struct {
		release.AccessChange
		AccessLoginAvailable bool `json:"access_login_available"`
	}{out, h.opts.AccessLoginAvailable})
}
