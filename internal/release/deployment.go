package release

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Status is a deployment's lifecycle:
//
//	(waiting_approval →) (building →) pending → applying → verifying → ready
//	   (build, apply or health check fails) ↘ failed
//	(any unfinished) → cancelled: a newer deployment superseded it, or IT
//	                   did not approve it (see CancelReason)
//
// The workflow's first step, authorize, asks IT's rules; a rule that wants
// approval parks the deployment in waiting_approval until someone with the
// permission decides (docs/approvals.md).
//
// A deployment from source starts building: its release (the built image
// plus its configuration) exists only once the build succeeded. A finished
// deployment never changes again. When one fails after reaching the cluster
// while another release was serving, the platform starts a new deployment of
// that release (Reason auto_rollback, RollbackOf the failed one): the
// environment's current release never moved, and the cluster is put back to
// it. A failed build changed nothing in the cluster: nothing is restored.
type Status string

const (
	StatusBuilding Status = "building"
	// StatusWaitingApproval: IT's rules asked for approval before it builds
	// (the workflow's authorize step); it continues when someone with the
	// permission approves, and is cancelled when they reject, it expires or
	// the requester takes it back.
	StatusWaitingApproval Status = "waiting_approval"
	StatusPending         Status = "pending"
	StatusApplying        Status = "applying"
	StatusVerifying       Status = "verifying"
	StatusReady           Status = "ready"
	StatusFailed          Status = "failed"
	StatusCancelled       Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case StatusBuilding, StatusWaitingApproval, StatusPending, StatusApplying, StatusVerifying, StatusReady, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Finished reports whether the deployment will not change any more.
func (s Status) Finished() bool {
	return s == StatusReady || s == StatusFailed || s == StatusCancelled
}

// StatusMessage is the 400 text for a value outside the set.
const StatusMessage = "status must be building, waiting_approval, pending, applying, verifying, ready, failed or cancelled"

// Reason is why a deployment exists.
type Reason string

const (
	ReasonDeploy       Reason = "deploy"        // someone published a new version
	ReasonRollback     Reason = "rollback"      // someone went back to an earlier version
	ReasonAutoRollback Reason = "auto_rollback" // the platform restored the serving version after a failure
	ReasonConfig       Reason = "config"        // the serving version again, to apply changed settings (who can open it)
)

// CodeResourceNotReady: a database, cache or volume the app uses was not
// ready in time (the agent's failure reason "resource").
const CodeResourceNotReady = "RESOURCE_NOT_READY"

// Error codes of failed deployments and refused requests.
const (
	CodeUnhealthy       = "HEALTH_CHECK_FAILED" // the app started but its health check fails
	CodeNotReady        = "NOT_READY"           // it did not become ready in time
	CodeApplyFailed     = "APPLY_FAILED"        // the cluster could not run it
	CodeImagePull       = "IMAGE_PULL_FAILED"   // the cluster cannot download the image
	CodeCrashed         = "APP_CRASHED"         // the app keeps exiting on start
	CodePromoteFailed   = "PROMOTE_FAILED"      // it ran, but recording it as current failed
	CodeAlreadyCurrent  = "ALREADY_CURRENT"     // rollback to what is already serving
	CodeNotRollbackable = "NOT_ROLLBACKABLE"    // rollback to a version that never went live
	CodeArchived        = project.CodeArchived  // deploy to an archived project
	// CodeBuildInternal is a build that failed on the platform's side: the
	// build's INTERNAL, named apart from the API's own INTERNAL errors. The
	// other build failures keep the build's code (build.Code*).
	CodeBuildInternal = "BUILD_INTERNAL"
	// CodePolicyDenied: IT's rules refused the deployment at its authorize
	// step (a rule changed or the tier moved after it was asked for).
	CodePolicyDenied = gate.CodeDenied
)

// Where a deployment was requested from (Deployment.RequestedVia).
const (
	ViaConsole  = "console"  // someone in the console (or the API)
	ViaGitHub   = "github"   // a push, the poll or "Publicar ahora" on a connected repository
	ViaPlatform = "platform" // the platform itself (the automatic restore)
	// ViaAssistantPrefix + an assistant key (claude_code, codex, vscode,
	// antigravity, other): an AI assistant through the MCP endpoint.
	ViaAssistantPrefix = "assistant:"
)

// ViaFor names where a request came from: the platform for an automatic
// restore; an assistant when the event client is an MCP one ("mcp:<oauth
// client id>", antigravityID being the id IAMKit gave Antigravity); the
// console otherwise.
func ViaFor(reason Reason, client, antigravityID string) string {
	if reason == ReasonAutoRollback {
		return ViaPlatform
	}
	if key, ok := assistant.Preset(client, antigravityID); ok {
		return ViaAssistantPrefix + key
	}
	return ViaConsole
}

// WorkflowDeploy is the workflow that drives a deployment (releasesvc).
const WorkflowDeploy = "release.deploy"

// DeployInput is the deploy workflow's input.
type DeployInput struct {
	Deployment DeploymentID `json:"deployment_id"`
	// Actor is who asked for the deployment, as they were then (the user and
	// the permissions of their token; the system actor for a push): the
	// authorize step has IT's rules judge the deployment as them, and an
	// approval request keeps it to run as them. Zero for the platform's own
	// deployments (auto-rollback, config), which are not judged.
	Actor actor.Actor `json:"actor"`
}

// Steps of the deploy workflow, in order (the console's progress).
const (
	StepBuild     = "build"      // build the uploaded source into an image (deployments from source)
	StepApply     = "apply"      // hand the desired app to the cluster
	StepWaitReady = "wait_ready" // wait until it answers its health check
	StepPromote   = "promote"    // make it the environment's current release
)

// Steps lists the deploy workflow's steps in order.
func Steps() []string { return []string{StepBuild, StepApply, StepWaitReady, StepPromote} }

// The workflow's gate steps run before the visible ones and are not part of
// the console's progress: StepAuthorize asks IT's rules (and, when a rule
// wants approval, parks the run until someone decides); StepApproval acts on
// that decision (continue, or cancel the deployment). A scan gate
// (`authorize_scan`, after the build) joins them in phase 3c.
const (
	StepAuthorize = "authorize"
	StepApproval  = "approval"
)

// Why IT's decision cancelled a deployment (DeploymentCancelled.Reason, and
// the console's wording); a deployment a newer one replaced says "superseded".
const (
	CancelSuperseded       = "superseded"
	CancelApprovalRejected = "approval_rejected"
	CancelApprovalExpired  = "approval_expired"
	CancelApprovalCanceled = "approval_cancelled"
)

// StepNames are the steps d goes through: an image deployment skips build.
func (d Deployment) StepNames() []string {
	if d.Build == nil {
		return Steps()[1:]
	}
	return Steps()
}

// StepState is one step's progress.
type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepDone    StepState = "done"
	StepFailed  StepState = "failed"
	StepSkipped StepState = "skipped" // never reached (failed earlier or cancelled)
)

// Progress is one step of a deployment, for the console's progress view.
type Progress struct {
	Name   string    `json:"name"`
	Status StepState `json:"status"`
}

// Failure is why a deployment did not go live: Message in plain Spanish for
// whoever deploys, Detail the cluster's own words ("" when it said nothing).
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

// DeploymentSource is the code a deployment from source builds: an uploaded
// folder, or a commit imported from git (Git set).
type DeploymentSource struct {
	ID   build.SourceID `json:"id"`
	Name string         `json:"name"`
	// Size is the archive's size in bytes.
	Size int64 `json:"size"`
	// Git is the commit it came from (nil for an uploaded folder).
	Git *build.GitRef `json:"git"`
}

// Deployment is one attempt to make a release the one an environment serves.
type Deployment struct {
	ID           DeploymentID            `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	// Release is nil while a deployment from source builds.
	Release *ID `json:"release_id" db:"release_id"`
	// Image is the release's image (denormalised for listings; "" while building).
	Image string `json:"image" db:"image"`
	// Build makes the image of a deployment from source, out of Source (both
	// nil when an image was published).
	Build  *build.ID         `json:"build_id" db:"build_id"`
	Source *DeploymentSource `json:"source" db:"-"`
	// Config is how a deployment from source runs once built; its release
	// keeps it from then on (nil for an image: its release's).
	Config *Config `json:"-" db:"-"`
	Status Status  `json:"status" db:"status"`
	Reason Reason  `json:"reason" db:"reason"`
	// Step is the workflow step the deployment is in (or failed at).
	Step  string     `json:"step" db:"step"`
	Steps []Progress `json:"steps" db:"-"`
	// Revision orders the deployments of an environment: the cluster ignores
	// an apply older than the last it saw (a superseded run cannot win late).
	Revision int64 `json:"revision" db:"revision"`
	// Previous is the release the environment served when this one started:
	// what a failure restores.
	Previous *ID `json:"previous_release_id" db:"previous_release_id"`
	// RollbackOf is the deployment this one undoes: the one that failed
	// (auto_rollback) or the one serving when someone went back (rollback).
	RollbackOf *DeploymentID `json:"rollback_of" db:"rollback_of"`
	// SettingsRevision is the environment settings revision the apply step
	// handed to the cluster (nil until it ran; settings are resolved when
	// applying, not when the deployment starts).
	SettingsRevision *int64         `json:"settings_revision" db:"settings_revision"`
	Error            *Failure       `json:"error" db:"-"`
	Run              workflow.RunID `json:"run_id" db:"run_id"`
	// RequestedBy is zero when the platform started it (auto_rollback).
	RequestedBy identity.UserID `json:"requested_by" db:"requested_by"`
	// RequestedVia is where the request came from (see Via*), set by the
	// service, never by the caller; "" for deployments made before it was
	// recorded.
	RequestedVia string     `json:"requested_via" db:"requested_via"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at" db:"finished_at"`
}

// EnvironmentState is what an environment serves and what is changing it.
// The project domain owns the environment; this is the release side of it.
type EnvironmentState struct {
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	// App is the name of the app in the cluster (a DNS label).
	App string `json:"-" db:"app"`
	// Revision is the last revision handed to the cluster.
	Revision int64 `json:"-" db:"revision"`
	// Current is the release serving traffic (nil before the first deploy);
	// only the deploy workflow's promote step changes it.
	Current           *ID           `json:"current_release_id" db:"current_release_id"`
	CurrentDeployment *DeploymentID `json:"current_deployment_id" db:"current_deployment_id"`
	// Active is the unfinished deployment (one at a time per environment).
	Active    *DeploymentID `json:"active_deployment_id" db:"active_deployment_id"`
	URL       string        `json:"url" db:"url"`
	UpdatedAt time.Time     `json:"updated_at" db:"updated_at"`
	// AppliedSettings is the settings revision the cluster was last told: set
	// by the apply step the moment the cluster takes the app, whether or not
	// that deployment then goes live (nil: the cluster was never told one).
	// It is what the app is protected with, and what the sign-in trusts.
	AppliedSettings *int64 `json:"-" db:"applied_settings_revision"`
}

// AppliedRevision is the settings revision the cluster was last told; -1 when
// it never was (any revision, even the defaults at 0, is newer).
func (s EnvironmentState) AppliedRevision() int64 {
	if s.AppliedSettings == nil {
		return -1
	}
	return *s.AppliedSettings
}

// Current is what an environment serves, for the console.
type Current struct {
	Environment project.EnvironmentID `json:"environment_id"`
	URL         string                `json:"url"`
	Release     *Release              `json:"release"`
	Deployment  *Deployment           `json:"deployment"`
	// Active is the deployment in progress, if any.
	Active *Deployment `json:"active"`
	// AppliedSettingsRevision is the settings revision the cluster was last
	// told (null: never). The console compares the latest revision with it:
	// a config deployment that fails after apply leaves the cluster told, and
	// that is not reopened by itself (fail-closed beats convenience).
	AppliedSettingsRevision *int64 `json:"applied_settings_revision"`
}

// DeploymentFilter narrows ListDeployments; zero fields match everything.
type DeploymentFilter struct {
	Status []Status
}

func (f DeploymentFilter) Validate() error {
	for _, s := range f.Status {
		if !s.Valid() {
			return errx.InvalidField("status", StatusMessage)
		}
	}
	return nil
}

// WithProgress fills Steps from Status and Step.
func (d Deployment) WithProgress() Deployment {
	names := d.StepNames()
	d.Steps = make([]Progress, 0, len(names))
	reached := false
	for _, name := range names {
		p := Progress{Name: name, Status: StepPending}
		switch {
		case d.Status == StatusReady:
			p.Status = StepDone
		case reached:
			if d.Status.Finished() {
				p.Status = StepSkipped
			}
		case name == d.Step || d.Step == "":
			reached = true
			switch d.Status {
			case StatusFailed:
				p.Status = StepFailed
			case StatusCancelled:
				p.Status = StepSkipped
			case StatusPending, StatusWaitingApproval:
				p.Status = StepPending
			default:
				p.Status = StepRunning
			}
		default:
			p.Status = StepDone
		}
		d.Steps = append(d.Steps, p)
	}
	return d
}

// AppName is the app's name in the cluster: the project slug in production,
// "<project>-<environment>" elsewhere (both are DNS labels; at most 63).
func AppName(projectSlug string, env project.Environment) string {
	if env.Kind == project.KindProduction {
		return projectSlug
	}
	name := projectSlug + "-" + env.Slug
	if len(name) > 63 {
		name = name[:63]
	}
	for len(name) > 0 && name[len(name)-1] == '-' {
		name = name[:len(name)-1]
	}
	return name
}
