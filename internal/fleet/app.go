package fleet

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// MaxPlacements bounds Queries.Placements (one read of the inventory).
const MaxPlacements = 2000

// App is the desired state of one app of an environment: what the agent of
// its cluster turns into an OdysseyApp.
type App struct {
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	// Cluster is where the environment runs (set by placement).
	Cluster ClusterID `json:"cluster_id" db:"cluster_id"`
	// Name is the app's DNS-label name in the cluster.
	Name string `json:"name" db:"name"`
	// Revision grows with every deployment of the environment: an older one
	// never replaces a newer one, here or in the cluster.
	Revision   int64             `json:"revision" db:"revision"`
	Image      string            `json:"image" db:"image"`
	Port       int               `json:"port" db:"port"`
	HealthPath string            `json:"health_path" db:"health_path"`
	Replicas   int               `json:"replicas" db:"replicas"`
	Env        map[string]string `json:"env" db:"-"`
	Labels     map[string]string `json:"labels" db:"-"`
	// Access is who may open the app: one of the Access* modes. "" is
	// "public" and only exists for revisions stored before 1e.
	Access string `json:"access" db:"-"`
	// Secrets are the app's secret variables, by name (references only).
	Secrets []AppSecret `json:"secrets" db:"-"`
	// Resources are the environment's resources the app uses (2b): the
	// agent wires their connection into the app and waits for them.
	Resources []ResourceRef `json:"resources" db:"-"`
	// AI connects the app to the organization's AI gateway (2d-ai); nil: no
	// AI. Its key is one of Secrets.
	AI *AIRef `json:"ai,omitempty" db:"-"`
	// Deleted: the app was retired (its project archived). Only the ids, name
	// and revision matter; the agent removes it and reports it PhaseDeleted,
	// and then it is forgotten.
	Deleted   bool      `json:"deleted" db:"deleted"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// Retirement is a retired app's desired state (App.Deleted): what Dispatch.
// Retire stores. Revision is above every deployment of the environment.
type Retirement struct {
	Organization identity.OrganizationID
	Project      project.ID
	Environment  project.EnvironmentID
	Name         string
	Revision     int64
}

func (r Retirement) Validate() error {
	switch {
	case r.Organization.IsZero() || r.Project.IsZero() || r.Environment.IsZero():
		return errx.Validation("retirement needs organization, project and environment")
	case !ValidAppName(r.Name):
		return errx.Validation("app name must be a DNS label")
	case r.Revision < 1:
		return errx.Validation("app revision must be positive")
	}
	return nil
}

// CapabilityRetire is listed in Capabilities.APIs when the agent removes an
// app sent with App.Deleted. A cluster whose agent does not list it is not
// sent a retirement (it would run the app again): it waits, stored, until
// the agent is updated.
const CapabilityRetire = "odicea.retire"

// CanRetire reports whether the cluster lists CapabilityRetire.
func (c Capabilities) CanRetire() bool { return slices.Contains(c.APIs, CapabilityRetire) }

// MaxAppSecrets bounds an app's secret variables.
const MaxAppSecrets = 100

var envVarPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// Access modes of an app (proto App.access.mode).
const (
	AccessPublic       = "public"       // anyone with the link
	AccessTeam         = "team"         // users of the app's organization
	AccessOrganization = "organization" // any user of the IAMKit environment
)

// ValidAccess accepts a mode, or "" (public, for revisions before 1e).
func ValidAccess(mode string) bool {
	return mode == "" || mode == AccessPublic || mode == AccessTeam || mode == AccessOrganization
}

// Validate checks what the release domain hands over (it validated the
// release already; this guards the fleet's own invariants).
func (a App) Validate() error {
	switch {
	case a.Organization.IsZero() || a.Project.IsZero() || a.Environment.IsZero():
		return errx.Validation("app needs organization, project and environment")
	case !ValidAppName(a.Name):
		return errx.Validation("app name must be a DNS label")
	case a.Revision < 1:
		return errx.Validation("app revision must be positive")
	case a.Image == "":
		return errx.Validation("app image is required")
	case !ValidAccess(a.Access):
		return errx.Validation("app access must be public, team or organization")
	case len(a.Secrets) > MaxAppSecrets:
		return errx.Validation("an app has at most 100 secrets")
	case len(a.Resources) > MaxAppResources:
		return errx.Validation("an app uses at most 10 resources")
	}
	if a.AI != nil {
		if err := a.AI.Validate(a.Secrets); err != nil {
			return err
		}
	}
	seenRes := make(map[string]bool, len(a.Resources))
	for _, r := range a.Resources {
		switch {
		case resourcekind.CheckName(r.Name) != nil || !r.Kind.Valid() || seenRes[r.Name]:
			return errx.Validation("resources must be distinct, with a valid name and kind")
		case resourcekind.CheckPrefix(r.Prefix) != nil:
			return errx.Validation(resourcekind.CheckPrefix(r.Prefix).Error())
		}
		seenRes[r.Name] = true
	}
	seen := make(map[string]bool, len(a.Secrets))
	for _, s := range a.Secrets {
		switch {
		case !envVarPattern.MatchString(s.Name) || seen[s.Name]:
			return errx.Validation("secret names must be distinct environment variable names")
		case s.Source == SecretManaged && (s.Version < 1 || s.KeyID == ""):
			return errx.Validation("a managed secret needs its loaded version and key")
		case s.Source == SecretExternal && (!ValidStoreName(s.Store) || s.Key == ""):
			return errx.Validation("an external secret needs a store and a key")
		case s.Source != SecretManaged && s.Source != SecretExternal:
			return errx.Validation("secret source must be managed or external")
		}
		seen[s.Name] = true
	}
	return nil
}

// Phase is how the agent sees an app.
type Phase string

const (
	PhaseProgressing Phase = "progressing"
	PhaseReady       Phase = "ready"
	PhaseFailed      Phase = "failed"
	// PhaseDeleted: a retired app (App.Deleted) is gone from the cluster.
	// It is never stored: the report forgets the app.
	PhaseDeleted Phase = "deleted"
)

func (p Phase) Valid() bool {
	return p == PhaseProgressing || p == PhaseReady || p == PhaseFailed || p == PhaseDeleted
}

// Reason explains a failed phase.
type Reason string

const (
	ReasonUnhealthy Reason = "unhealthy"
	ReasonImagePull Reason = "image_pull"
	ReasonCrash     Reason = "crash"
	ReasonConfig    Reason = "config"
	// ReasonResource: a database, cache or volume the app uses was not ready
	// in time.
	ReasonResource Reason = "resource"
)

func (r Reason) Valid() bool {
	return r == "" || r == ReasonUnhealthy || r == ReasonImagePull || r == ReasonCrash || r == ReasonConfig || r == ReasonResource
}

// AppStatus is what the agent observed of an app.
type AppStatus struct {
	Environment project.EnvironmentID `json:"environment_id" db:"environment_id"`
	Name        string                `json:"name" db:"name"`
	// ObservedRevision is the desired revision the status describes (0:
	// the agent has not acted on it yet).
	ObservedRevision int64     `json:"observed_revision" db:"observed_revision"`
	Phase            Phase     `json:"phase" db:"phase"`
	Reason           Reason    `json:"reason" db:"reason"`
	Message          string    `json:"message" db:"message"`
	ReadyReplicas    int       `json:"ready_replicas" db:"ready_replicas"`
	URL              string    `json:"url" db:"url"`
	ObservedAt       time.Time `json:"observed_at" db:"observed_at"`
}

// MaxMessage bounds the agent's message of an app.
const MaxMessage = 2000

// Validate bounds what an agent reports of an app.
func (s AppStatus) Validate() error {
	switch {
	case s.Environment.IsZero() || !ValidAppName(s.Name):
		return errx.Validation("reported app needs an environment and a valid name")
	case s.ObservedRevision < 0 || s.ReadyReplicas < 0:
		return errx.Validation("reported revision and replicas must not be negative")
	case !s.Phase.Valid() || !s.Reason.Valid():
		return errx.Validation("reported phase or reason is not valid")
	case utf8.RuneCountInString(s.Message) > MaxMessage || utf8.RuneCountInString(s.URL) > text.MaxURL:
		return errx.Validation("reported message or url is too long")
	case text.HasForbiddenControl(s.Message) || text.HasForbiddenControl(s.URL):
		return errx.Validation("reported message or url contains control characters")
	case !ValidAppURL(s.URL):
		return errx.Validation("reported url must be an http or https address")
	}
	return nil
}

// ValidAppURL accepts what the console may link to: empty (no address yet)
// or an absolute http(s) URL with a host and no credentials. The agent is
// outside the trust boundary, so a "javascript:" URL from a compromised
// cluster must never reach a builder's browser.
func ValidAppURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

// Report is a batch of statuses from an agent. A full report lists every
// app of the cluster (sent once per tunnel); the others carry changes.
type Report struct {
	Full bool
	Apps []AppStatus
}

// Validate checks the batch itself; an invalid status in it does not make
// the batch invalid (Valid splits them: one bad app must not hide the
// others' state).
func (r Report) Validate() error {
	if len(r.Apps) > MaxReportApps {
		return errx.Validation("too many apps in one report")
	}
	return nil
}

// Valid is the report without its invalid statuses, and why the last of
// those was refused (nil when all are valid).
func (r Report) Valid() (Report, error) {
	out := Report{Full: r.Full, Apps: make([]AppStatus, 0, len(r.Apps))}
	var invalid error
	dropped := 0
	for _, a := range r.Apps {
		if err := a.Validate(); err != nil {
			invalid, dropped = err, dropped+1
			continue
		}
		out.Apps = append(out.Apps, a)
	}
	if invalid != nil {
		invalid = errx.Wrap(invalid, fmt.Sprintf("%d app status(es) dropped from the report", dropped), errx.TypeValidation)
	}
	return out, invalid
}

// Agent identities are URIs in the certificate (SPIFFE-style), so the
// gateway learns the organization and cluster from the verified certificate
// alone.
const agentURIPrefix = "spiffe://odyssey.dev/org/"

// AgentURI names the agent of a cluster in its certificate.
func AgentURI(organization identity.OrganizationID, cluster ClusterID) string {
	return agentURIPrefix + organization.String() + "/cluster/" + cluster.String()
}

// ParseAgentURI reads an AgentURI.
func ParseAgentURI(u *url.URL) (identity.OrganizationID, ClusterID, error) {
	bad := fmt.Errorf("not an agent identity: %s", u)
	rest, ok := strings.CutPrefix(u.String(), agentURIPrefix)
	if !ok {
		return identity.OrganizationID{}, ClusterID{}, bad
	}
	org, cluster, ok := strings.Cut(rest, "/cluster/")
	if !ok {
		return identity.OrganizationID{}, ClusterID{}, bad
	}
	o, err := identity.ParseOrganizationID(org)
	if err != nil {
		return identity.OrganizationID{}, ClusterID{}, bad
	}
	c, err := ParseClusterID(cluster)
	if err != nil {
		return identity.OrganizationID{}, ClusterID{}, bad
	}
	return o, c, nil
}
