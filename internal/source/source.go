// Package source connects apps to their code in git (phase 1c: GitHub).
//
// An organization links the installations of Odyssey's GitHub App it owns
// (Installation); a project connects one repository branch (Connection); a
// commit to publish is a Push. A push is imported, not cloned in the
// cluster: the control plane downloads the commit's tarball with a
// short-lived installation token, repacks it and uploads it as a
// build.Source, and from there a deployment from source runs exactly like
// an uploaded folder (the cluster never holds a GitHub credential).
//
// GitHub itself is behind the GitProvider port (adapter sourcegithub; a fake
// for tests and for installations without GitHub).
package source

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Installation is an installation of Odyssey's GitHub App on a GitHub
// account (a user or an organization), linked to one Odyssey organization.
// GitHub's numeric installation id is the identity: an installation belongs
// to at most one organization.
type Installation struct {
	ID           InstallationID          `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	// ExternalID is GitHub's installation id.
	ExternalID int64 `json:"external_id" db:"external_id"`
	// Account is the GitHub login it is installed on; AccountType User or Organization.
	Account     string `json:"account" db:"account"`
	AccountType string `json:"account_type" db:"account_type"`
	// AllRepositories: installed on every repository of the account (else
	// on the ones chosen there).
	AllRepositories bool            `json:"all_repositories" db:"all_repositories"`
	Suspended       bool            `json:"suspended" db:"suspended"`
	LinkedBy        identity.UserID `json:"linked_by" db:"linked_by"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at" db:"updated_at"`
}

// Connection is a project's repository: which branch publishes it, from
// which folder, and how the result runs. One per project (phase 1c
// publishes to production only).
type Connection struct {
	ID           ConnectionID            `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Installation InstallationID          `json:"installation_id" db:"installation_id"`
	// RepositoryID is GitHub's numeric id: it survives a rename or transfer.
	RepositoryID int64  `json:"repository_id" db:"repository_id"`
	Repository   string `json:"repository" db:"repository"` // "owner/name" as of the last (re)connect; a push does not rename it
	Branch       string `json:"branch" db:"branch"`
	// Root is the folder of the app inside the repository ("" for its root).
	Root   string         `json:"root" db:"root"`
	Config release.Config `json:"config" db:"-"`
	// LastCommit is the newest commit seen on the branch ("" before the first).
	LastCommit string `json:"last_commit" db:"last_commit"`
	// ETag is the provider's tag of the branch head the poll saw last
	// (sent back so an unchanged branch costs no rate limit).
	ETag        string          `json:"-" db:"etag"`
	ConnectedBy identity.UserID `json:"connected_by" db:"connected_by"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at"`
}

// PushStatus is a push's lifecycle:
//
//	queued → importing → deployed | failed
//	queued | importing → skipped: a newer push of the same connection
//	                      arrived (only the newest commit is published)
type PushStatus string

const (
	StatusQueued    PushStatus = "queued"
	StatusImporting PushStatus = "importing"
	StatusDeployed  PushStatus = "deployed" // imported and handed to a deployment (Deployment set)
	StatusFailed    PushStatus = "failed"
	StatusSkipped   PushStatus = "skipped"
)

func (s PushStatus) Finished() bool {
	return s == StatusDeployed || s == StatusFailed || s == StatusSkipped
}

func (s PushStatus) Valid() bool { return s == StatusQueued || s == StatusImporting || s.Finished() }

// Trigger is what asked to publish a commit.
type Trigger string

const (
	TriggerWebhook   Trigger = "webhook"   // GitHub said a branch moved
	TriggerReconcile Trigger = "reconcile" // the poll found the branch ahead of the last commit seen
	TriggerManual    Trigger = "manual"    // someone pressed "Publicar ahora" (or connected the repository)
)

// Push is one commit of a connection to publish. A commit is published once
// per connection, whatever reported it.
type Push struct {
	ID           PushID                  `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Connection   ConnectionID            `json:"connection_id" db:"connection_id"`
	Git          build.GitRef            `json:"git" db:"-"`
	Trigger      Trigger                 `json:"trigger" db:"trigger"`
	Status       PushStatus              `json:"status" db:"status"`
	// Deployment is the deployment it became (deployed only).
	Deployment *release.DeploymentID `json:"deployment_id" db:"deployment_id"`
	Error      *Failure              `json:"error" db:"-"`
	// RequestedBy is zero for a webhook or the poll.
	RequestedBy identity.UserID `json:"requested_by" db:"requested_by"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at"`
}

// Failure is why a push was not published, in plain Spanish (Message) and
// in the provider's words (Detail).
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

// Repo is a repository an installation reaches (as the provider lists it).
type Repo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"` // "owner/name"
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	// Installation is the organization's installation that reaches it.
	Installation InstallationID `json:"installation_id"`
}

// Branch is a branch of a repository and its head.
type Branch struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// InstallationInfo is an installation as the provider describes it.
type InstallationInfo struct {
	ID              int64
	Account         string
	AccountType     string
	AllRepositories bool
	Suspended       bool
}

// Commit status states.
const (
	CommitPending = "pending"
	CommitSuccess = "success"
	CommitFailure = "failure"
	// CommitError: the deployment did not finish (a newer one replaced it).
	CommitError = "error"
)

// StatusContext names Odyssey's commit statuses on the provider.
const StatusContext = "Odicea"

// CommitStatus is a status to show on a commit.
type CommitStatus struct {
	State       string // CommitPending, CommitSuccess, CommitFailure or CommitError
	Description string
	// TargetURL is where the status links to (the app in the console).
	TargetURL string
}

// Commit is a commit as the provider describes it.
type Commit struct {
	SHA    string
	Title  string // first line of the message
	Author string
}

// MaxRoot bounds the app's folder inside the repository.
const MaxRoot = 200

// Connect asks to connect (or reconnect) a project's repository.
type Connect struct {
	Installation InstallationID `json:"installation_id"`
	RepositoryID int64          `json:"repository_id"`
	// Branch "" takes the repository's default branch.
	Branch string `json:"branch"`
	Root   string `json:"root"`
	release.Config
	ConnectedBy identity.UserID `json:"-"`
}

func (c Connect) Normalize() Connect {
	c.Branch = strings.TrimSpace(c.Branch)
	c.Root = NormalizeRoot(c.Root)
	c.Config = c.Config.Normalize()
	return c
}

func (c Connect) Validate() error {
	switch {
	case c.Installation.IsZero():
		return errx.InvalidField("installation_id", "installation_id is required")
	case c.RepositoryID <= 0:
		return errx.InvalidField("repository_id", "repository_id is required")
	case len(c.Branch) > build.MaxBranch || !utf8.ValidString(c.Branch) || strings.ContainsAny(c.Branch, " ~^:?*[\\\x00"):
		return errx.InvalidField("branch", "branch must be a git branch name")
	}
	if err := ValidRoot(c.Root); err != nil {
		return err
	}
	return c.Config.Validate()
}

// NormalizeRoot trims slashes and spaces ("/apps/web/" → "apps/web").
func NormalizeRoot(root string) string {
	return strings.Trim(strings.TrimSpace(root), "/")
}

// ValidRoot accepts "" or a relative folder path without "." or ".." parts.
func ValidRoot(root string) error {
	if root == "" {
		return nil
	}
	if len(root) > MaxRoot || !utf8.ValidString(root) || strings.ContainsAny(root, "\\\x00") {
		return errx.InvalidField("root", "root must be a folder of the repository")
	}
	for _, part := range strings.Split(root, "/") {
		if part == "" || part == "." || part == ".." {
			return errx.InvalidField("root", "root must be a folder of the repository")
		}
	}
	return nil
}

// Publish asks to publish the head of the connected branch now.
type Publish struct {
	RequestedBy identity.UserID `json:"-"`
}

func (Publish) Validate() error { return nil }

// Disconnect asks to stop publishing a project from its repository.
type Disconnect struct {
	DisconnectedBy identity.UserID `json:"-"`
}

func (Disconnect) Validate() error { return nil }

// Link is what the setup callback brings: the provider must confirm the user
// (by the OAuth code) owns or administers the installation's account; the id
// in the URL alone proves nothing. It yields a claim, not a link.
type Link struct {
	ExternalID int64
	// Code is the OAuth code of the user who came back from installing.
	Code string
}

func (l Link) Validate() error {
	switch {
	case l.ExternalID <= 0:
		return errx.InvalidField("installation_id", "installation_id is required")
	case l.Code == "" || len(l.Code) > 512:
		return errx.InvalidField("code", "the authorization code is missing")
	}
	return nil
}

// MaxClaim bounds a claim's length.
const MaxClaim = 2048

// ClaimInstallation asks to link the installation a claim (from the setup
// callback) names, as the authenticated user the claim was issued to.
type ClaimInstallation struct {
	Claim string `json:"claim"`
}

func (c ClaimInstallation) Validate() error {
	if c.Claim == "" || len(c.Claim) > MaxClaim {
		return errx.InvalidField("claim", "claim is required and at most 2048 characters")
	}
	return nil
}

// ClaimResult is the linked installation and the console path to go back to.
type ClaimResult struct {
	Installation Installation `json:"installation"`
	Return       string       `json:"return_to"`
}

// Unlink asks to unlink an installation (its connections stop publishing).
type Unlink struct {
	UnlinkedBy identity.UserID `json:"-"`
}

func (Unlink) Validate() error { return nil }

// PushFilter narrows a push listing.
type PushFilter struct {
	Status []PushStatus
}

func (f PushFilter) Validate() error {
	for _, s := range f.Status {
		switch s {
		case StatusQueued, StatusImporting, StatusDeployed, StatusFailed, StatusSkipped:
		default:
			return errx.InvalidField("status", "status must be queued, importing, deployed, failed or skipped")
		}
	}
	return nil
}

// Webhook is a delivery from the provider. Receive verifies Signature (over
// Payload) before anything else.
type Webhook struct {
	// Delivery is the provider's delivery id: a redelivery is ignored.
	Delivery string
	Event    string // "push", "installation", "installation_repositories", "ping"
	// Signature is the provider's signature of Payload (X-Hub-Signature-256).
	Signature string
	Payload   []byte
}

func (w Webhook) Validate() error {
	if w.Delivery == "" || len(w.Delivery) > 100 || w.Event == "" || len(w.Event) > 100 {
		return errx.Validation("the webhook delivery has no id or event")
	}
	return nil
}

// PushEvent is what a push webhook says (decoded by the provider adapter).
type PushEvent struct {
	Installation int64
	RepositoryID int64
	Repository   string // "owner/name"
	// Branch is the branch that moved ("" for a tag or anything else).
	Branch string
	Head   Commit
	// Deleted: the branch was deleted (nothing to publish).
	Deleted bool
}

// InstallationEvent is what an installation webhook says.
type InstallationEvent struct {
	Installation int64
	// Action: deleted, suspend, unsuspend, created, new_permissions_accepted;
	// for installation_repositories: added, removed.
	Action string
	// Removed are repository ids taken away from the installation.
	Removed []int64
}

// Info is what the console needs of the GitHub integration (no secrets).
type Info struct {
	// Enabled: the App is configured on this Odyssey.
	Enabled bool `json:"enabled"`
	// Slug is the App's name in URLs (github.com/apps/<slug>). The install
	// link is Commands.InstallURL: it carries a signed state naming who asked.
	Slug string `json:"slug"`
}

// SetupResult is where the setup callback sends the browser back.
type SetupResult struct {
	// Claim is the signed token the console posts (logged in) to link the
	// installation.
	Claim string
	// Return is the console path to go back to.
	Return string
}

// SettingsPage is the console's GitHub settings page: where the setup
// callback returns when the install link named no page, and on failures.
const SettingsPage = "/configuracion/github"

// WorkflowImport turns a push into a deployment (sourcesvc).
const WorkflowImport = "source.import"

// ImportInput is the source.import workflow's input.
type ImportInput struct {
	Push PushID `json:"push_id"`
}

// Steps of source.import.
const (
	StepImport = "import" // download the commit, repack it, upload it as a build source
	StepDeploy = "deploy" // start the deployment from it
)

// ReconcileEvery is how often the poll compares connected branches with
// their last commit (webhooks may be lost, or unable to reach Odyssey).
const ReconcileEvery = 2 * time.Minute
