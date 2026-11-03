package source

import (
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Report asks to show a commit status for a push on the provider. Name the
// push, or (for a deployment's events) the deployment it became.
type Report struct {
	Push       PushID
	Deployment release.DeploymentID
	Status     CommitStatus
}

// MaxStatusDescription bounds a status description in runes (the provider's
// limit is 140).
const MaxStatusDescription = 140

// Truncated is the status with its description cut to fit the provider.
func (s CommitStatus) Truncated() CommitStatus {
	if r := []rune(s.Description); len(r) > MaxStatusDescription {
		s.Description = string(r[:MaxStatusDescription])
	}
	return s
}

// The commit statuses of a commit's life, in the words whoever publishes
// reads on the provider (the target link is added by the service).

// PreparingStatus: the push was queued and will be imported.
func PreparingStatus() CommitStatus {
	return CommitStatus{State: CommitPending, Description: "Preparando la publicación"}
}

// PushFailedStatus: the commit could not be imported or deployed.
func PushFailedStatus(message string) CommitStatus {
	return CommitStatus{State: CommitFailure, Description: message}.Truncated()
}

// PublishingStatus: the deployment started.
func PublishingStatus() CommitStatus {
	return CommitStatus{State: CommitPending, Description: "Publicando"}
}

// LiveStatus: the deployment is serving.
func LiveStatus() CommitStatus {
	return CommitStatus{State: CommitSuccess, Description: "En línea"}
}

// DeploymentFailedStatus: the deployment failed.
func DeploymentFailedStatus(message string) CommitStatus {
	return CommitStatus{State: CommitFailure, Description: "No se pudo publicar: " + message}.Truncated()
}

// ReplacedStatus: a newer deployment superseded it.
func ReplacedStatus() CommitStatus {
	return CommitStatus{State: CommitError, Description: "Reemplazada por una publicación más nueva"}
}
