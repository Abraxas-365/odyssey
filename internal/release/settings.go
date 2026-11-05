package release

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// AccessMode is who can open an app (docs/api.md §11, "Quién puede abrirla").
// team and organization are enforced by the access gate in the app's cluster
// with an Odicea login; public lets anyone with the link in.
type AccessMode string

const (
	AccessTeam         AccessMode = "team"         // people of the app's organization (the default)
	AccessOrganization AccessMode = "organization" // everyone in the company's IAMKit environment
	AccessPublic       AccessMode = "public"       // anyone with the link (needs odyssey:access:public)
)

// DefaultAccess is what an environment without settings uses: safe by default.
const DefaultAccess = AccessTeam

// AccessMessage is the 400 text for a mode outside the set.
const AccessMessage = "mode must be team, organization or public"

func (m AccessMode) Valid() bool {
	switch m {
	case AccessTeam, AccessOrganization, AccessPublic:
		return true
	}
	return false
}

// AccessLabel words a mode for summaries in the activity trail.
func AccessLabel(m AccessMode) string {
	switch m {
	case AccessTeam:
		return "solo tu equipo"
	case AccessOrganization:
		return "toda la empresa"
	case AccessPublic:
		return "cualquiera con el enlace"
	}
	return string(m)
}

// Protected reports whether opening the app takes an Odicea login.
func (m AccessMode) Protected() bool { return m == AccessTeam || m == AccessOrganization }

// AccessSettings is the access part of an environment's settings.
type AccessSettings struct {
	Mode AccessMode `json:"mode"`
}

// Settings is how an environment is configured, as one revision of an
// append-only history: a change is a new, higher revision, never an update.
// Each revision is the whole configuration (a change copies the rest
// forward), so a deployment that applies revision N gets exactly N.
// An environment with no revision uses the defaults (Revision 0).
type Settings struct {
	Environment  project.EnvironmentID   `json:"-"`
	Organization identity.OrganizationID `json:"-"`
	Revision     int64                   `json:"revision"`
	Access       AccessSettings          `json:"access"`
	// Env are the environment's plain variables (2a). They win over a
	// release's env of the same name; never secrets.
	Env map[string]string `json:"env"`
	// Secrets are the environment's secret variables, sorted by name: only
	// references, never a value. A name is in Env or in Secrets, not both.
	Secrets []SecretRef `json:"secrets"`
	// Resources are the databases, caches and volumes connected to the app
	// (2b), sorted by name: references only. The variables they define are
	// not in Env nor Secrets (see resources.go).
	Resources []ResourceUse `json:"resources"`
	// AI is the app's use of the organization's AI gateway (2d-ai; see ai.go).
	AI        AISettings      `json:"ai"`
	CreatedBy identity.UserID `json:"-"`
	CreatedAt time.Time       `json:"-"`
}

// DefaultSettings are the settings of an environment that never changed them.
func DefaultSettings(organization identity.OrganizationID, env project.EnvironmentID) Settings {
	return Settings{Environment: env, Organization: organization, Access: AccessSettings{Mode: DefaultAccess},
		Env: map[string]string{}, Secrets: []SecretRef{}, Resources: []ResourceUse{}}
}

// Normalize fills the empty collections (rows written before 2a have none).
func (s Settings) Normalize() Settings {
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if s.Secrets == nil {
		s.Secrets = []SecretRef{}
	}
	if s.Resources == nil {
		s.Resources = []ResourceUse{}
	}
	slices.SortFunc(s.Secrets, func(a, b SecretRef) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.Resources, func(a, b ResourceUse) int { return strings.Compare(a.Name, b.Name) })
	return s
}

// next is a copy of s to change into the following revision (deep: the
// maps and slices are not shared with s).
func (s Settings) next(actor identity.UserID) Settings {
	n := s.Normalize()
	n.Revision, n.CreatedBy, n.CreatedAt = 0, actor, time.Time{}
	n.Env = maps.Clone(n.Env)
	n.Secrets = slices.Clone(n.Secrets)
	n.Resources = slices.Clone(n.Resources)
	return n
}

// Secret is the reference named name (false when there is none).
func (s Settings) Secret(name string) (SecretRef, bool) {
	for _, r := range s.Secrets {
		if r.Name == name {
			return r, true
		}
	}
	return SecretRef{}, false
}

// WithAccess is the next settings with the access mode changed (the rest
// copied forward).
func (s Settings) WithAccess(mode AccessMode, actor identity.UserID) Settings {
	n := s.next(actor)
	n.Access.Mode = mode
	return n
}

// EnvNames are the plain variables' names, sorted.
func (s Settings) EnvNames() []string {
	return slices.Sorted(maps.Keys(s.Normalize().Env))
}

// SecretNames are the secrets' names, sorted.
func (s Settings) SecretNames() []string {
	out := make([]string, 0, len(s.Secrets))
	for _, r := range s.Normalize().Secrets {
		out = append(out, r.Name)
	}
	return out
}

// SetAccess is the request to change who can open an environment's app.
type SetAccess struct {
	Mode        AccessMode
	RequestedBy identity.UserID
}

func (in SetAccess) Validate() error {
	if !in.Mode.Valid() {
		return errx.InvalidField("mode", AccessMessage)
	}
	return nil
}

// AccessChange is the outcome of SetAccess: the settings now in force and the
// deployment that applies them (nil when it only stored them: another
// deployment is in progress and will pick them up, nothing is online yet, or
// nothing changed).
type AccessChange = SettingsChange

// SettingsChange is the outcome of any settings change (access, variables,
// secrets): the settings now in force and the config deployment that applies
// them (nil when it only stored them: another deployment is in progress and
// will pick them up, nothing is online yet, or nothing changed).
type SettingsChange struct {
	Settings   Settings    `json:"settings"`
	Deployment *Deployment `json:"deployment"`
}

// AppliedSettings is the settings revision the deployment handed to the
// cluster; -1 when it never recorded one (it ran before settings existed, so
// the cluster has no access mode from us: any revision, even the defaults at
// 0, is newer).
func (d Deployment) AppliedSettings() int64 {
	if d.SettingsRevision == nil {
		return -1
	}
	return *d.SettingsRevision
}

// AccessTarget is where an environment's app is served and whose it is.
type AccessTarget struct {
	Environment  project.EnvironmentID
	Organization identity.OrganizationID
	// URL is the app's address as promoted ("" before its first deployment).
	URL string
	// Mode is the access the cluster was last told: the settings revision
	// the latest deployment that reached apply carried (the latest settings
	// for an environment that has none recorded). The sign-in trusts this,
	// never the gate's query: a public app has no sign-in.
	Mode AccessMode
}
