package release

import (
	"context"
	"io"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/gate"
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
	// SetEnv changes the environment's plain variables, as SetAccess changes
	// its access (a new revision; a config deployment when something is
	// online). Setting a name that is a secret is refused.
	SetEnv(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input SetEnv) (SettingsChange, error)
	// ConnectResource connects a resource of the environment to the app
	// (a new settings revision and, when something is online, a config
	// deployment that wires its variables). The resource must exist in the
	// environment and not be deleting; a variable another variable, secret or
	// resource holds is refused (409 VARIABLE_TAKEN). Connecting it as it
	// already is changes nothing.
	ConnectResource(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input ConnectResource) (SettingsChange, error)
	// DisconnectResource removes the connection (the resource keeps
	// existing; NotFound when it was not connected).
	DisconnectResource(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input DisconnectResource) (SettingsChange, error)
	// RequestSecret asks a person to load a managed secret's value (nothing
	// changes in the cluster until it is loaded). A secret already there is
	// returned as it is, without a request.
	RequestSecret(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input RequestSecret) (SecretView, error)
	// LoadSecret stores a managed value sealed in the browser to the
	// cluster's key: the reference goes into a new revision, the sealed
	// bytes to the cluster, and a config deployment applies it. Version
	// must be the next one (CodeSecretVersionConflict otherwise).
	LoadSecret(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input LoadSecret) (SettingsChange, error)
	// SetExternalSecret points a secret at the company's vault through a
	// ClusterSecretStore of the cluster. The caller checks the permission.
	SetExternalSecret(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input SetExternalSecret) (SettingsChange, error)
	// RemoveSecret drops a secret variable, or a request (NotFound when neither).
	RemoveSecret(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input RemoveSecret) (SettingsChange, error)
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
	// Secrets are the environment's secret variables and open requests, with
	// their state against its cluster, and what the cluster offers.
	Secrets(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Secrets, error)
	// Uses is what the app depends on and what still needs a person (2e):
	// access, identity, resources, secrets, AI and egress, with the closed
	// list of Attention. It changes nothing.
	Uses(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Uses, error)
	// Serving is, for each of the environments (at most MaxServing), what is
	// online and how its latest deployment ended: one query for the inventory
	// and the attention list. An environment that never deployed is absent.
	// It changes nothing.
	Serving(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) ([]Serving, error)
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
	// Retire has the cluster remove the app named name (its project was
	// archived): revision is above every deployment of the environment, so
	// no older one runs it again. It joins the caller's transaction; an app
	// the cluster never had is a no-op, and so is a repeated retirement.
	Retire(ctx context.Context, target Target, name string, revision int64) error
	// Status reports the app named name (ObservedRevision 0 when unknown).
	Status(ctx context.Context, target Target, name string) (AppStatus, error)
	// CanProtect reports whether the environment's cluster can serve apps
	// behind the access sign-in (team, organization). It changes nothing.
	CanProtect(ctx context.Context, target Target) (bool, error)
	// Secrets is what the environment's cluster (or, unplaced, the
	// organization's default) offers for secrets. It changes nothing.
	Secrets(ctx context.Context, target Target) (ClusterSecrets, error)
	// DeliverSecret has a sealed managed value taken to the environment's
	// cluster. It joins the caller's transaction; a value sealed to another
	// key than the cluster's is refused (Business, CodeSecretKeyChanged).
	DeliverSecret(ctx context.Context, target Target, delivery SecretDelivery) error
	// Egress is the egress policy of the environment's cluster and how its
	// agent took it (Available false when it has no cluster). It changes
	// nothing.
	Egress(ctx context.Context, target Target) (ClusterEgress, error)
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

// Resources is what release asks the resource domain, through an adapter
// (releaseresource): whether a resource can be connected. It runs inside the
// caller's transaction, so a resource being deleted at the same time is seen
// (the deletion holds the same settings lock, see Usage).
type Resources interface {
	// Find is the resource of the environment (NotFound when there is none).
	Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (ResourceInfo, error)
	// Statuses are the resources of the environment with those names (the
	// ones the app is connected to; the caller already placed the
	// environment) and how the cluster sees each: the Uses read model asks
	// for nothing else.
	Statuses(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, names []string) ([]ResourceState, error)
}

// AIStatus is what release asks the ai domain (its releaseai adapter) to
// describe the app's AI in Uses. release never imports the ai domain
// (architecture test).
type AIStatus interface {
	// AIState says whether the AI can be turned on for the environment and
	// where it stands.
	AIState(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (AIState, error)
}

// Policy is what release asks IT's rules (the policy domain, through the
// policyrelease adapter; release never imports policy): may this action
// happen? release enforces it where each action starts (SetAccess,
// ConnectResource: releasesvc.authorize) and, for a deployment or a rollback,
// in the deploy workflow's authorize step (a cheap deny-only check at the
// request, so a refused deploy answers 403 at once).
type Policy interface {
	// Check decides the request for the organization, as the actor on ctx.
	Check(ctx context.Context, organization identity.OrganizationID, request gate.Request) (gate.Verdict, error)
}

// Approvals is where release holds an action IT's rules want approved (the
// approvalrelease adapter on the approval domain; release never imports
// approval). A direct action (SetAccess, ConnectResource) is Held and answers
// pending_approval; when it is approved it re-runs as the requester with the
// approval's mark on ctx (gate.WithApproval) and Redeems it. A deployment
// holds itself from the workflow's authorize step (Run set), is released by
// the decision signal and Consumes the approval; a deployment a newer one
// replaced has its request cancelled (CancelRun).
type Approvals interface {
	gate.RunApprover
}

// Usage is what the resource domain asks release (its resourcerelease
// adapter), implemented by releasesvc.Service.
type Usage interface {
	// WithResourceUseLocked runs fn in a transaction holding the lock of the
	// environment's settings, with the resources connected at that moment.
	// ConnectResource takes the same lock, so a deletion checking "not in use"
	// inside fn can never race a connection: either the connection is seen, or
	// it waits and finds the resource deleting.
	WithResourceUseLocked(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID,
		fn func(ctx context.Context, connected []ResourceUse) error) error
	// AuthorizeConnect asks IT's rules whether a resource of the kind may be
	// connected to the environment's app, as the actor on ctx, and changes
	// nothing. Creating a resource that connects asks it before provisioning
	// anything; ConnectResource (Judged) then does not ask again. A rule
	// that wants approval holds the operation described by detail and returns
	// the pending answer (nothing else may then happen); the approved
	// operation re-runs and redeems it here.
	AuthorizeConnect(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, kind string, detail gate.Detail) (*gate.Pending, error)
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
	// ListServing is Serving for several environments, in one query.
	ListServing(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) ([]Serving, error)
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
	// Retire records that the environment's app was retired at revision (its
	// project was archived): the revision handed to the cluster, and when
	// (RetiredAt; SetAppliedSettings clears it). NotFound when the environment
	// has no state, or holds that revision or a newer one.
	Retire(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, revision int64) error
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

	// PutSecretRequest stores (or renews) a request for a managed value.
	PutSecretRequest(ctx context.Context, request SecretRequest) error
	ListSecretRequests(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]SecretRequest, error)
	// DeleteSecretRequest drops the request named name (false when none).
	DeleteSecretRequest(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (bool, error)
	// ListSecretVersions is, by name, the highest managed version any settings
	// revision of the environment ever recorded. AppendSettings raises it in
	// the revision's transaction and it never goes down, even if the history
	// were purged: a value loaded again after a removal gets a newer version
	// than the one the cluster may still hold.
	ListSecretVersions(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (map[string]int64, error)
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
