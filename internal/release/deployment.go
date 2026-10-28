package release

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Status is a deployment's lifecycle:
//
//	pending → applying → verifying → ready
//	   (apply or health check fails) ↘ failed
//	(any unfinished) → cancelled: a newer deployment superseded it
//
// A finished deployment never changes again. When one fails while another
// release was serving, the platform starts a new deployment of that release
// (Reason auto_rollback, RollbackOf the failed one): the environment's
// current release never moved, and the cluster is put back to it.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApplying  Status = "applying"
	StatusVerifying Status = "verifying"
	StatusReady     Status = "ready"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusApplying, StatusVerifying, StatusReady, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Finished reports whether the deployment will not change any more.
func (s Status) Finished() bool {
	return s == StatusReady || s == StatusFailed || s == StatusCancelled
}

// StatusMessage is the 400 text for a value outside the set.
const StatusMessage = "status must be pending, applying, verifying, ready, failed or cancelled"

// Reason is why a deployment exists.
type Reason string

const (
	ReasonDeploy       Reason = "deploy"        // someone published a new version
	ReasonRollback     Reason = "rollback"      // someone went back to an earlier version
	ReasonAutoRollback Reason = "auto_rollback" // the platform restored the serving version after a failure
)

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
)

// WorkflowDeploy is the workflow that drives a deployment (releasesvc).
const WorkflowDeploy = "release.deploy"

// DeployInput is the deploy workflow's input.
type DeployInput struct {
	Deployment DeploymentID `json:"deployment_id"`
}

// Steps of the deploy workflow, in order (the console's progress).
const (
	StepApply     = "apply"      // hand the desired app to the cluster
	StepWaitReady = "wait_ready" // wait until it answers its health check
	StepPromote   = "promote"    // make it the environment's current release
)

// Steps lists the deploy workflow's steps in order.
func Steps() []string { return []string{StepApply, StepWaitReady, StepPromote} }

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

// Failure is why a deployment did not go live.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Deployment is one attempt to make a release the one an environment serves.
type Deployment struct {
	ID           DeploymentID            `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	Release      ID                      `json:"release_id" db:"release_id"`
	// Image is the release's image (denormalised for listings).
	Image  string `json:"image" db:"image"`
	Status Status `json:"status" db:"status"`
	Reason Reason `json:"reason" db:"reason"`
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
	RollbackOf *DeploymentID  `json:"rollback_of" db:"rollback_of"`
	Error      *Failure       `json:"error" db:"-"`
	Run        workflow.RunID `json:"run_id" db:"run_id"`
	// RequestedBy is zero when the platform started it (auto_rollback).
	RequestedBy identity.UserID `json:"requested_by" db:"requested_by"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at"`
	FinishedAt  *time.Time      `json:"finished_at" db:"finished_at"`
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
}

// Current is what an environment serves, for the console.
type Current struct {
	Environment project.EnvironmentID `json:"environment_id"`
	URL         string                `json:"url"`
	Release     *Release              `json:"release"`
	Deployment  *Deployment           `json:"deployment"`
	// Active is the deployment in progress, if any.
	Active *Deployment `json:"active"`
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
	d.Steps = make([]Progress, 0, 3)
	reached := false
	for _, name := range Steps() {
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
			case StatusPending:
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
