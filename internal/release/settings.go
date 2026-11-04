package release

import (
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
// An environment with no revision uses the defaults (Revision 0).
type Settings struct {
	Environment  project.EnvironmentID   `json:"-"`
	Organization identity.OrganizationID `json:"-"`
	Revision     int64                   `json:"revision"`
	Access       AccessSettings          `json:"access"`
	CreatedBy    identity.UserID         `json:"-"`
	CreatedAt    time.Time               `json:"-"`
}

// DefaultSettings are the settings of an environment that never changed them.
func DefaultSettings(organization identity.OrganizationID, env project.EnvironmentID) Settings {
	return Settings{Environment: env, Organization: organization, Access: AccessSettings{Mode: DefaultAccess}}
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
type AccessChange struct {
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
