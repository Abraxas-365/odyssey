package release

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Commands are the write operations, implemented by releasesvc.Service.
type Commands interface {
	// Deploy creates a release of the image and starts deploying it to the
	// environment, cancelling the deployment in progress there.
	Deploy(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Deploy) (Deployment, error)
	// Rollback deploys again the release of an earlier deployment that went live.
	Rollback(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, deployment DeploymentID, input Rollback) (Deployment, error)
}

// Queries are the read operations, implemented by releasesvc.Service.
type Queries interface {
	Current(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Current, error)
	FindDeployment(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, deployment DeploymentID) (Deployment, error)
	ListDeployments(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, filter DeploymentFilter, page query.Pagination) (query.Paginated[Deployment], error)
	FindRelease(ctx context.Context, organization identity.OrganizationID, project project.ID, release ID) (Release, error)
}

// Workflows are the definitions the composition root registers.
type Workflows interface {
	Workflows() []workflow.Definition
}

// Cluster runs apps. The agent in each cluster reconciles an OdysseyApp
// from DesiredApp and reports AppStatus (adapter releasefleet, phase 1a-a3);
// releasefake stands in for it until then and in tests.
type Cluster interface {
	// Apply hands the desired app to the cluster. It is idempotent, and a
	// desired revision older than the last applied one is ignored.
	Apply(ctx context.Context, target Target, app DesiredApp) error
	// Status reports the app named name (ObservedRevision 0 when unknown).
	Status(ctx context.Context, target Target, name string) (AppStatus, error)
}

// Repository is the persistence contract; only the service depends on it.
// Every statement filters by organization (another tenant's row is NotFound).
type Repository interface {
	CreateRelease(ctx context.Context, release Release) error
	FindRelease(ctx context.Context, organization identity.OrganizationID, release ID) (Release, error)

	CreateDeployment(ctx context.Context, deployment Deployment) error
	FindDeployment(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID) (Deployment, error)
	// FindDeploymentForUpdate locks the row until the transaction ends.
	FindDeploymentForUpdate(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID) (Deployment, error)
	ListDeployments(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, filter DeploymentFilter, page query.Pagination) (query.Paginated[Deployment], error)
	// SetProgress records the step an unfinished deployment is in.
	SetProgress(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID, status Status, step string) error
	// Finish moves an unfinished deployment to a final status.
	Finish(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID, status Status, failure *Failure) error

	// FindState is the environment's release state (zero before its first deployment).
	FindState(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (EnvironmentState, error)
	// LockState locks the environment's release state until the transaction
	// ends, creating it on first use: it serialises the environment's deploys.
	LockState(ctx context.Context, state EnvironmentState) (EnvironmentState, error)
	// Begin records a new active deployment and the revision handed to the cluster.
	Begin(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, deployment DeploymentID, revision int64) error
	// Settle clears the active deployment (it finished without going live).
	Settle(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, deployment DeploymentID) error
	// Promote makes the deployment's release the one the environment serves.
	// Only the deploy workflow's promote step calls it (architecture test).
	Promote(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, deployment Deployment, url string) error
}
