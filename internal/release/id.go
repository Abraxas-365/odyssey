package release

import "github.com/Abraxas-365/odyssey/internal/identity"

type (
	releaseTag    struct{}
	deploymentTag struct{}
)

func (releaseTag) IDName() string    { return "release_id" }
func (deploymentTag) IDName() string { return "deployment_id" }

// ID identifies a release: an immutable image plus its configuration.
type ID = identity.ID[releaseTag]

func NewID() ID                      { return identity.NewID[releaseTag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[releaseTag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[releaseTag](raw) }

// DeploymentID identifies one attempt to run a release in an environment.
type DeploymentID = identity.ID[deploymentTag]

func NewDeploymentID() DeploymentID { return identity.NewID[deploymentTag]() }
func ParseDeploymentID(raw string) (DeploymentID, error) {
	return identity.ParseID[deploymentTag](raw)
}
func MustParseDeploymentID(raw string) DeploymentID {
	return identity.MustParseID[deploymentTag](raw)
}
