package release

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Serving (3c) is, for one environment, what is online and how its latest
// deployment ended: the release side of the inventory and of "Necesita tu
// atención". It is read in batches (one query for any number of
// environments), never assembled one environment at a time.
type Serving struct {
	Environment project.EnvironmentID `json:"environment_id"`
	Project     project.ID            `json:"project_id"`
	// URL is where the app answers ("" until its first deployment is live).
	URL string `json:"url"`
	// Image is the release online ("" when nothing is).
	Image string `json:"image"`
	// Build is the build that made the image online (nil for an image that
	// was published as it was: it is never scanned, see docs/scan.md).
	Build *build.ID `json:"build_id"`
	// Since is when the online release went live.
	Since *time.Time `json:"since"`
	// Last is the environment's latest deployment, whatever its status (nil
	// before the first).
	Last *LastDeployment `json:"last"`
	// Access is who can open the app online now (nil when nothing is online
	// or the mode in force cannot be told).
	Access *ServingAccess `json:"access"`
}

// ServingAccess is who can open the app online: the mode the cluster was
// last told (what the access gate enforces), never merely the stored one.
type ServingAccess struct {
	Mode AccessMode `json:"mode"`
	// Pending is a different mode that is stored and not applied yet (nil
	// when none): until it is, Mode is still who can open the app.
	Pending *AccessMode `json:"pending"`
}

// AccessInForce is who can open an app online. applied is the settings
// revision the cluster was last told (nil: never) and appliedMode that
// revision's mode (nil for revision 0, the defaults, or when the revision is
// gone); latest and latestMode are the newest stored revision (nil: none,
// the defaults). Nothing online, or a mode that cannot be told, is nil: the
// console then says nothing rather than guess.
func AccessInForce(online bool, applied *int64, appliedMode *AccessMode, latest *int64, latestMode *AccessMode) *ServingAccess {
	if !online || applied == nil {
		return nil
	}
	mode := DefaultAccess
	if *applied > 0 {
		if appliedMode == nil {
			return nil
		}
		mode = *appliedMode
	}
	out := &ServingAccess{Mode: mode}
	if latest != nil && *latest > *applied && latestMode != nil && *latestMode != mode {
		next := *latestMode
		out.Pending = &next
	}
	return out
}

// LastDeployment is how an environment's latest deployment stands.
type LastDeployment struct {
	ID         DeploymentID `json:"id"`
	Status     Status       `json:"status"`
	Reason     Reason       `json:"reason"`
	Error      *Failure     `json:"error"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt *time.Time   `json:"finished_at"`
}

// FailedWithin reports whether the latest deployment failed at or after
// since (a later successful one clears it: it would not be the latest).
func (s Serving) FailedWithin(since time.Time) bool {
	return s.Last != nil && s.Last.Status == StatusFailed && s.Last.FinishedAt != nil && !s.Last.FinishedAt.Before(since)
}

// MaxServing bounds ServingAll (environments read at once) and ServingOf.
const MaxServing = 2000
