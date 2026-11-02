// Package sourcerelease is the source.Deployer adapter on the release
// domain: imported sources are deployed through release's Commands port
// only (never releasesvc). It is the only way source reaches release.
package sourcerelease

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
)

type Deployer struct{ commands release.Commands }

func New(commands release.Commands) *Deployer { return &Deployer{commands: commands} }

var _ source.Deployer = (*Deployer)(nil)

func (d *Deployer) DeployUploaded(ctx context.Context, organization identity.OrganizationID, id project.ID, env project.EnvironmentID, input release.DeployUploaded) (release.Deployment, error) {
	return d.commands.DeployUploaded(ctx, organization, id, env, input)
}
