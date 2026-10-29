package release

import (
	"sort"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the release domain's public contract. Payloads carry facts
// and IDs; add fields freely, rename or remove one only in a new version.
const (
	EventCreated             = "release.created"
	EventDeploymentStarted   = "deployment.started"
	EventDeploymentReady     = "deployment.ready"
	EventDeploymentFailed    = "deployment.failed"
	EventDeploymentCancelled = "deployment.cancelled"
)

// Subject kinds ("release/<id>", "deployment/<id>").
const (
	SubjectKind           = "release"
	DeploymentSubjectKind = "deployment"
)

// Names are the human names events are summarised with.
type Names struct {
	Project     string
	Environment string
}

func (n Names) where() string { return n.Project + " en " + n.Environment }

// Created v1. Env values are not in the trail; their names are.
type Created struct {
	Release    string   `json:"release_id"`
	Project    string   `json:"project_id"`
	Image      string   `json:"image"`
	Port       int      `json:"port"`
	HealthPath string   `json:"health_path"`
	Replicas   int      `json:"replicas"`
	EnvKeys    []string `json:"env_keys"`
}

// DeploymentStarted v1.
type DeploymentStarted struct {
	Deployment  DeploymentID `json:"deployment_id"`
	Project     string       `json:"project_id"`
	Environment string       `json:"environment_id"`
	// Release and Image are null and "" for a deployment from source (it
	// builds first: deployment.ready names the built release); BuildID is its
	// build, null for an image (since v1, additive).
	Release    *ID           `json:"release_id"`
	Image      string        `json:"image"`
	Build      *string       `json:"build_id"`
	Reason     Reason        `json:"reason"`
	Revision   int64         `json:"revision"`
	Previous   *ID           `json:"previous_release_id"`
	RollbackOf *DeploymentID `json:"rollback_of"`
}

// DeploymentReady v1: the release is now the environment's current one.
type DeploymentReady struct {
	Deployment  DeploymentID `json:"deployment_id"`
	Project     string       `json:"project_id"`
	Environment string       `json:"environment_id"`
	Release     ID           `json:"release_id"`
	URL         string       `json:"url"`
}

// DeploymentFailed v1. AutoRollback is the deployment restoring the
// previous release, when one was serving.
type DeploymentFailed struct {
	Deployment   DeploymentID  `json:"deployment_id"`
	Project      string        `json:"project_id"`
	Environment  string        `json:"environment_id"`
	Release      *ID           `json:"release_id"` // null when its build failed
	Step         string        `json:"step"`
	Code         string        `json:"code"`
	Message      string        `json:"message"`
	Detail       string        `json:"detail"` // the cluster's own words (since v1, additive)
	AutoRollback *DeploymentID `json:"auto_rollback_id"`
}

// DeploymentCancelled v1: a newer deployment of the environment superseded it.
type DeploymentCancelled struct {
	Deployment   DeploymentID `json:"deployment_id"`
	Project      string       `json:"project_id"`
	Environment  string       `json:"environment_id"`
	Release      *ID          `json:"release_id"` // null when it was still building
	SupersededBy DeploymentID `json:"superseded_by"`
}

func NewCreated(org identity.OrganizationID, actor identity.UserID, r Release, n Names) event.Event {
	keys := make([]string, 0, len(r.Config.Env))
	for k := range r.Config.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return event.New(EventCreated, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Creó una versión de "+n.Project,
		Created{Release: r.ID.String(), Project: r.Project.String(), Image: r.Image, Port: r.Config.Port,
			HealthPath: r.Config.HealthPath, Replicas: r.Config.Replicas, EnvKeys: keys})
}

func NewDeploymentStarted(org identity.OrganizationID, actor identity.UserID, d Deployment, n Names) event.Event {
	summary := "Empezó a publicar " + n.where()
	switch d.Reason {
	case ReasonRollback:
		summary = "Volvió a una versión anterior de " + n.where()
	case ReasonAutoRollback:
		summary = "Restaurando la versión anterior de " + n.where()
	}
	return event.New(EventDeploymentStarted, 1, org, actor, event.Subject(DeploymentSubjectKind, d.ID), summary,
		DeploymentStarted{Deployment: d.ID, Project: d.Project.String(), Environment: d.Environment.String(),
			Release: d.Release, Image: d.Image, Build: buildOf(d), Reason: d.Reason, Revision: d.Revision, Previous: d.Previous, RollbackOf: d.RollbackOf})
}

func buildOf(d Deployment) *string {
	if d.Build == nil {
		return nil
	}
	s := d.Build.String()
	return &s
}

func NewDeploymentReady(org identity.OrganizationID, actor identity.UserID, d Deployment, url string, n Names) event.Event {
	summary := n.Project + " quedó en línea en " + n.Environment
	if d.Reason == ReasonAutoRollback {
		summary = "Se restauró la versión anterior de " + n.where()
	}
	return event.New(EventDeploymentReady, 1, org, actor, event.Subject(DeploymentSubjectKind, d.ID), summary,
		DeploymentReady{Deployment: d.ID, Project: d.Project.String(), Environment: d.Environment.String(), Release: *d.Release, URL: url})
}

func NewDeploymentFailed(org identity.OrganizationID, actor identity.UserID, d Deployment, autoRollback *DeploymentID, n Names) event.Event {
	var f Failure
	if d.Error != nil {
		f = *d.Error
	}
	return event.New(EventDeploymentFailed, 1, org, actor, event.Subject(DeploymentSubjectKind, d.ID),
		"Falló la publicación de "+n.where()+": "+f.Message,
		DeploymentFailed{Deployment: d.ID, Project: d.Project.String(), Environment: d.Environment.String(), Release: d.Release,
			Step: d.Step, Code: f.Code, Message: f.Message, Detail: f.Detail, AutoRollback: autoRollback})
}

func NewDeploymentCancelled(org identity.OrganizationID, actor identity.UserID, d Deployment, by DeploymentID, n Names) event.Event {
	return event.New(EventDeploymentCancelled, 1, org, actor, event.Subject(DeploymentSubjectKind, d.ID),
		"Se canceló una publicación de "+n.where()+" (la reemplazó otra)",
		DeploymentCancelled{Deployment: d.ID, Project: d.Project.String(), Environment: d.Environment.String(), Release: d.Release, SupersededBy: by})
}
