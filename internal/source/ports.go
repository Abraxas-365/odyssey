package source

import (
	"context"
	"io"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Commands are the write operations, implemented by sourcesvc.Service.
type Commands interface {
	// InstallURL is where to send a user to install the App for the
	// organization: the provider's install page with a signed state that
	// the setup callback checks (it expires, and names the organization and
	// the user who asked).
	InstallURL(ctx context.Context, organization identity.OrganizationID, user identity.UserID, returnTo string) (string, error)
	// Setup handles the provider's setup callback, which is public and so
	// proves nothing about who is logged in to Odyssey. It checks the state,
	// confirms with the user's OAuth code that the GitHub user owns or
	// administers the installation's account, and writes nothing: it returns
	// a signed, short-lived claim bound to the state's organization and user.
	Setup(ctx context.Context, state string, input Link) (SetupResult, error)
	// Claim links the installation a claim names to the organization, for
	// the authenticated user the claim was issued to (STATE_INVALID for
	// another user or organization, or a forged or expired claim). Linking
	// it again to the same organization refreshes it; Conflict when another
	// organization has it.
	Claim(ctx context.Context, organization identity.OrganizationID, user identity.UserID, input ClaimInstallation) (ClaimResult, error)
	Unlink(ctx context.Context, organization identity.OrganizationID, installation InstallationID, input Unlink) error

	// Connect connects (or reconnects) the project's repository and asks to
	// publish its branch's head.
	Connect(ctx context.Context, organization identity.OrganizationID, project project.ID, input Connect) (Connection, error)
	Disconnect(ctx context.Context, organization identity.OrganizationID, project project.ID, input Disconnect) error
	// Publish asks to publish the connected branch's head now.
	Publish(ctx context.Context, organization identity.OrganizationID, project project.ID, input Publish) (Push, error)

	// Receive handles a provider webhook delivery (public: it proves itself
	// with the signature, checked first; a bad one is Unauthorized). A
	// delivery seen before does nothing. Everything one delivery causes
	// happens in one transaction.
	Receive(ctx context.Context, input Webhook) error
	// Reconcile compares the connected branches with their last commit (the
	// poll: webhooks may be lost, or unable to reach Odyssey) and queues the
	// ones that moved; the number of commits queued. A provider outage stops
	// the round with its error.
	Reconcile(ctx context.Context) (int, error)
	// Report shows the status of a push (or of the deployment it became) on
	// the provider's commit. Best effort: provider errors are logged, not
	// returned, so a late status is never retried; nil for a deployment not
	// published from git.
	Report(ctx context.Context, organization identity.OrganizationID, input Report) error
}

// Queries are the read operations, implemented by sourcesvc.Service.
type Queries interface {
	Info(ctx context.Context) Info
	ListInstallations(ctx context.Context, organization identity.OrganizationID) ([]Installation, error)
	// ListRepositories lists what the organization's installations reach
	// (Search filters by name).
	ListRepositories(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Repo], error)
	ListBranches(ctx context.Context, organization identity.OrganizationID, installation InstallationID, repository int64) ([]Branch, error)
	// FindConnection is the project's connection (NotFound when none).
	FindConnection(ctx context.Context, organization identity.OrganizationID, project project.ID) (Connection, error)
	ListPushes(ctx context.Context, organization identity.OrganizationID, project project.ID, filter PushFilter, page query.Pagination) (query.Paginated[Push], error)
}

// Workflows are the definitions the composition root registers.
type Workflows interface {
	Workflows() []workflow.Definition
}

// GitProvider is the git host (adapter sourcegithub; sourcefake in tests).
// Installation methods act as the App on one installation, with a
// short-lived token limited to what they need.
type GitProvider interface {
	// Enabled reports whether the App is configured.
	Enabled() bool
	// Slug is the App's name in URLs.
	Slug() string
	// InstallPage is the provider's page to install the App, carrying state.
	InstallPage(state string) string
	// ConfirmInstaller exchanges a user's OAuth code and proves the user owns
	// the installation's account: the installation must be among the ones
	// the user reaches and the user must be the account itself (a User
	// account) or an active admin of it (an Organization). CodeNotYours
	// otherwise. The setup callback needs this proof: reaching an
	// installation (a plain member of the organization) is not enough.
	ConfirmInstaller(ctx context.Context, code string, installation InstallationInfo) error
	// Installation describes one installation of the App (NotFound when gone).
	Installation(ctx context.Context, installation int64) (InstallationInfo, error)
	// Repositories lists what an installation reaches (Installation unset:
	// the caller knows which of its own it asked).
	Repositories(ctx context.Context, installation int64) ([]Repo, error)
	Repository(ctx context.Context, installation int64, repository int64) (Repo, error)
	Branches(ctx context.Context, installation int64, repository int64) ([]Branch, error)
	// Head is the newest commit of a branch. With etag (the one the last
	// call returned) an unchanged branch answers changed=false without
	// spending the rate limit; then, and on errors, newETag is etag.
	Head(ctx context.Context, installation int64, repository int64, branch, etag string) (head Commit, newETag string, changed bool, err error)
	// Tarball streams the commit's gzipped tarball (as the provider makes
	// it: one top folder, a pax global header). The caller closes it.
	Tarball(ctx context.Context, installation int64, repository int64, commit string) (io.ReadCloser, error)
	// SetStatus reports a deployment of commit back to the provider (a
	// commit status named "Odicea"); best effort for the caller.
	SetStatus(ctx context.Context, installation int64, repository int64, commit string, status CommitStatus) error
	// VerifyWebhook checks a delivery's signature over body.
	VerifyWebhook(signature string, body []byte) error
	PushEvent(payload []byte) (PushEvent, error)
	InstallationEvent(event string, payload []byte) (InstallationEvent, error)
}

// Builder uploads imported commits as build sources (adapter sourcebuild,
// on build.Commands).
type Builder interface {
	Upload(ctx context.Context, organization identity.OrganizationID, project project.ID, input build.Upload, archive io.Reader) (build.Source, error)
	Discard(ctx context.Context, organization identity.OrganizationID, source build.SourceID) error
}

// Deployer starts deployments of imported sources (adapter sourcerelease,
// on release.Commands).
type Deployer interface {
	DeployUploaded(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input release.DeployUploaded) (release.Deployment, error)
}

// Repository is the persistence contract; only the service depends on it.
// Every statement filters by organization (another tenant's row is
// NotFound), but for three lookups that know only the provider's ids or run
// across tenants: FindInstallationByExternal, ListConnectionsByRepository
// and ListConnectionsToReconcile. They are cross-org: callers take the
// organization from the installation or connection row they return, keep
// using the org-scoped Lock* methods, and must check that
// connection.Organization == installation.Organization.
type Repository interface {
	UpsertInstallation(ctx context.Context, installation Installation) (Installation, error)
	FindInstallation(ctx context.Context, organization identity.OrganizationID, installation InstallationID) (Installation, error)
	// FindInstallationByExternal is unscoped (cross-org): webhooks name
	// GitHub's id. Use the returned row's organization from then on.
	FindInstallationByExternal(ctx context.Context, external int64) (Installation, error)
	ListInstallations(ctx context.Context, organization identity.OrganizationID) ([]Installation, error)
	DeleteInstallation(ctx context.Context, organization identity.OrganizationID, installation InstallationID) error

	// UpsertConnection creates the project's connection or replaces its
	// repository, branch, folder and config (keeping its id; a different
	// repository or branch forgets the last commit and ETag seen).
	UpsertConnection(ctx context.Context, connection Connection) (Connection, error)
	FindConnection(ctx context.Context, organization identity.OrganizationID, project project.ID) (Connection, error)
	// LockConnection is FindConnection FOR UPDATE (inside txn.Runner.Do).
	LockConnection(ctx context.Context, organization identity.OrganizationID, project project.ID) (Connection, error)
	// ListConnectionsByRepository is unscoped (cross-org): a push webhook
	// names the repository id (every connection of it, whatever the branch,
	// ordered by id). Check each one's organization against the installation's.
	ListConnectionsByRepository(ctx context.Context, installation InstallationID, repository int64) ([]Connection, error)
	// ListConnectionsToReconcile returns connections whose last check is
	// older than before (or never), oldest first, at most limit, skipping
	// suspended installations (unscoped, cross-org: the poll).
	ListConnectionsToReconcile(ctx context.Context, before time.Time, limit int) ([]Connection, error)
	DeleteConnection(ctx context.Context, organization identity.OrganizationID, project project.ID) error
	// DeleteConnectionsOfInstallation deletes the installation's
	// connections to repositories (nil: to any repository) and returns them.
	DeleteConnectionsOfInstallation(ctx context.Context, organization identity.OrganizationID, installation InstallationID, repositories []int64) ([]Connection, error)
	// ObserveHead records what the poll saw of a connection's branch (its
	// ETag; at becomes the last check).
	ObserveHead(ctx context.Context, connection ConnectionID, etag string, at time.Time) error

	// CreatePush inserts a push and makes its commit the connection's last
	// commit. Deduplication is the caller's: under LockConnection, a webhook
	// or the poll queues only a commit other than the last one ("Publicar
	// ahora" always queues).
	CreatePush(ctx context.Context, push Push) error
	FindPush(ctx context.Context, organization identity.OrganizationID, push PushID) (Push, error)
	// FindPushByDeployment is the push a deployment came from (NotFound for
	// a deployment not published from git).
	FindPushByDeployment(ctx context.Context, organization identity.OrganizationID, deployment release.DeploymentID) (Push, error)
	// LockPush is FindPush FOR UPDATE (inside txn.Runner.Do).
	LockPush(ctx context.Context, organization identity.OrganizationID, push PushID) (Push, error)
	ListPushes(ctx context.Context, organization identity.OrganizationID, project project.ID, filter PushFilter, page query.Pagination) (query.Paginated[Push], error)
	// SkipPushes marks the unfinished pushes of a connection queued before
	// newer as skipped and returns them.
	SkipPushes(ctx context.Context, organization identity.OrganizationID, connection ConnectionID, newer PushID) ([]Push, error)
	// UpdatePush writes a push's status, deployment and error.
	UpdatePush(ctx context.Context, push Push) error

	// ObserveDelivery records a webhook delivery id and reports whether it
	// is new (false when seen before). In the same call it forgets up to
	// 1000 ids seen before forgetBefore (the provider stops redelivering
	// long before), so no separate sweep is needed.
	ObserveDelivery(ctx context.Context, delivery string, forgetBefore time.Time) (bool, error)
}
