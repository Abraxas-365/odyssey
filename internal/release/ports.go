package release

import (
	"context"
	"io"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
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
	// DeploySource stores the uploaded folder (a tar.gz stream) and starts a
	// deployment that builds it first, cancelling the one in progress there.
	DeploySource(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input DeploySource, archive io.Reader) (Deployment, error)
	// DeployUploaded starts a deployment that builds a source already
	// uploaded to the project (a commit imported from git), cancelling the
	// one in progress there. The source must be the project's and not
	// expired; it is not discarded when the deployment is refused (its
	// uploader decides).
	DeployUploaded(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input DeployUploaded) (Deployment, error)
	// Rollback deploys again the release of an earlier deployment that went live.
	Rollback(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, deployment DeploymentID, input Rollback) (Deployment, error)
	// SetAccess changes who can open the environment's app: it stores a new
	// settings revision and, when something is online and no deployment is in
	// progress, starts a config deployment of the current release to apply it
	// (a deployment in progress applies it itself). Choosing the mode already
	// in force changes nothing. The caller checks the permission for the mode.
	SetAccess(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input SetAccess) (AccessChange, error)
	// IssueUpload issues a one-use ticket to publish a folder to the
	// environment (see UploadTicket); the returned ticket carries its token.
	// The ticket names the request's client (event.Meta).
	IssueUpload(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input IssueUpload) (UploadTicket, error)
	// RedeemUpload uses a ticket: it publishes archive like DeploySource, for
	// the ticket's organization, app, environment, user, configuration and
	// client. The token is the only credential (a public route): unknown is
	// NotFound, used is CodeUploadTicketUsed, expired CodeUploadTicketExpired.
	// A ticket is used even when the upload then fails.
	RedeemUpload(ctx context.Context, token string, archive io.Reader) (Deployment, error)
}

// Queries are the read operations, implemented by releasesvc.Service.
type Queries interface {
	Current(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Current, error)
	FindDeployment(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, deployment DeploymentID) (Deployment, error)
	ListDeployments(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, filter DeploymentFilter, page query.Pagination) (query.Paginated[Deployment], error)
	FindRelease(ctx context.Context, organization identity.OrganizationID, project project.ID, release ID) (Release, error)
	// Settings are the environment's settings in force (the defaults when it
	// never changed them).
	Settings(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Settings, error)
	// AccessProtectionAvailable: the environment's cluster can protect apps
	// with the sign-in (see Cluster.CanProtect). When it cannot, only public
	// can be applied there.
	AccessProtectionAvailable(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (bool, error)
}

// AccessLookup finds an environment by its id alone, for the access sign-in
// (an unauthenticated route: the gate sends only the environment id, and
// the organization comes from here, never from the request). Only the
// access adapter calls it (architecture test).
type AccessLookup interface {
	FindAccessTarget(ctx context.Context, environment project.EnvironmentID) (AccessTarget, error)
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
	// CanProtect reports whether the environment's cluster can serve apps
	// behind the access sign-in (team, organization). It changes nothing.
	CanProtect(ctx context.Context, target Target) (bool, error)
}

// Builder turns uploaded sources into images: the build domain, through
// adapter releasebuild (release depends on this port, never on buildsvc).
type Builder interface {
	// Upload stores and validates a source archive streamed from archive.
	Upload(ctx context.Context, organization identity.OrganizationID, project project.ID, input build.Upload, archive io.Reader) (build.Source, error)
	// Start builds a source; the same build ID starts it once. It joins the
	// caller's transaction.
	Start(ctx context.Context, organization identity.OrganizationID, input build.Start) error
	// Status is the build (NotFound before Start).
	Status(ctx context.Context, organization identity.OrganizationID, id build.ID) (build.Build, error)
	// Cancel stops a build nobody needs (a no-op when finished or never
	// started). It joins the caller's transaction.
	Cancel(ctx context.Context, organization identity.OrganizationID, id build.ID, input build.Cancel) error
	// Discard drops a source no deployment will build (best effort: what it
	// cannot drop, the sweep does).
	Discard(ctx context.Context, organization identity.OrganizationID, source build.SourceID) error
	// Source is an uploaded source (NotFound when it is not the
	// organization's).
	Source(ctx context.Context, organization identity.OrganizationID, source build.SourceID) (build.Source, error)
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
	// SetSettingsRevision records the settings revision the apply step
	// handed to the cluster.
	SetSettingsRevision(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID, revision int64) error
	// SetBuilt records the release a building deployment built and moves it
	// on to apply.
	SetBuilt(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID, release Release) error
	// SetAppliedSettings records on the environment's state the settings
	// revision the cluster was just told, when deploymentRevision is still the
	// latest one begun there (a superseded deployment changes nothing).
	SetAppliedSettings(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, deploymentRevision, settings int64) error
	// Finish moves an unfinished deployment to a final status.
	Finish(ctx context.Context, organization identity.OrganizationID, deployment DeploymentID, status Status, failure *Failure) error

	// FindState is the environment's release state (zero before its first deployment).
	FindState(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (EnvironmentState, error)
	// FindStateByEnvironment is FindState without the organization: the
	// access sign-in knows only the environment id the gate sent. NotFound
	// when the environment has no state. Only releasesvc calls it
	// (architecture test), and only for FindAccessTarget.
	FindStateByEnvironment(ctx context.Context, environment project.EnvironmentID) (EnvironmentState, error)
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

	// FindSettings is the environment's latest settings revision (the
	// defaults, revision 0, when it never changed them).
	FindSettings(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (Settings, error)
	// FindSettingsRevision is one revision of the environment's settings
	// (NotFound when it does not exist; revision 0 is the defaults).
	FindSettingsRevision(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, revision int64) (Settings, error)
	// AppendSettings stores s as the environment's next revision (history is
	// append-only) and returns it. Call it holding the environment's state
	// lock (LockState): revisions are assigned there.
	AppendSettings(ctx context.Context, settings Settings) (Settings, error)

	CreateUploadTicket(ctx context.Context, ticket UploadTicket) error
	// ConsumeUploadTicket marks the unused, unexpired ticket with hash used
	// at now and returns it (NotFound otherwise). It is not scoped by
	// organization (the token is all the caller has): only releasesvc calls
	// it (architecture test).
	ConsumeUploadTicket(ctx context.Context, hash []byte, now time.Time) (UploadTicket, error)
	// FindUploadTicketByHash tells why a ticket could not be consumed
	// (cross-org like ConsumeUploadTicket).
	FindUploadTicketByHash(ctx context.Context, hash []byte) (UploadTicket, error)
	// SetUploadDeployment records the deployment a ticket's upload started.
	SetUploadDeployment(ctx context.Context, organization identity.OrganizationID, ticket UploadTicketID, deployment DeploymentID) error
	// DeleteUploadTickets deletes the organization's tickets that expired
	// before cutoff.
	DeleteUploadTickets(ctx context.Context, organization identity.OrganizationID, cutoff time.Time) error
}
