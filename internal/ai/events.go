package ai

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the AI's public contract. Payloads carry facts and IDs, never
// the key; add fields freely, rename or remove one only in a new version.
const (
	// EventEnabled: the app's AI is on (its key was made and delivered).
	EventEnabled = "ai.enabled"
	// EventDisabled: the AI is off and its keys revoked.
	EventDisabled = "ai.disabled"
	// EventRotated: the app got a new key; the old one is revoked once the
	// app runs with the new one.
	EventRotated = "ai.rotated"
	// EventRevokeFailed: a key could not be revoked at the gateway; it is
	// retried by the sweep (with a growing wait) and IT should review it. Only
	// the first failure of a revocation is published; the sweep's retries that
	// fail again are not.
	EventRevokeFailed = "ai.revoke_failed"
	// EventRevocationRetried: the sweep set a failed revocation going again
	// (the key's status changed without a person asking).
	EventRevocationRetried = "ai.revocation_retried"
)

// SubjectKind prefixes the key's event subjects ("ai_key/<id>").
const SubjectKind = "ai_key"

// EnvironmentSubjectKind prefixes the subject of what concerns the whole
// environment ("ai_environment/<id>"): turning the AI off.
const EnvironmentSubjectKind = "ai_environment"

// Names are the human names events are summarised with.
type Names struct {
	Project     string
	Environment string
}

func (n Names) where() string {
	if n.Project == "" && n.Environment == "" {
		return "un entorno que ya no existe"
	}
	return n.Project + " (" + n.Environment + ")"
}

// Enabled v1 (also Rotated v1: Version says which key version is in force).
type Enabled struct {
	Key         KeyID  `json:"key_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Version     int64  `json:"version"`
}

// Rotated v1.
type Rotated = Enabled

// Disabled v1.
type Disabled struct {
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
}

func NewEnabled(org identity.OrganizationID, actor identity.UserID, k Key, version int64, n Names) event.Event {
	return event.New(EventEnabled, 1, org, actor, event.Subject(SubjectKind, k.ID), "Activó la IA de "+n.where(),
		Enabled{Key: k.ID, Project: k.Project.String(), Environment: k.Environment.String(), Version: version})
}

func NewRotated(org identity.OrganizationID, actor identity.UserID, k Key, version int64, n Names) event.Event {
	return event.New(EventRotated, 1, org, actor, event.Subject(SubjectKind, k.ID), "Renovó la llave de IA de "+n.where(),
		Rotated{Key: k.ID, Project: k.Project.String(), Environment: k.Environment.String(), Version: version})
}

func NewDisabled(org identity.OrganizationID, actor identity.UserID, projectID, environmentID string, n Names) event.Event {
	return event.New(EventDisabled, 1, org, actor, EnvironmentSubjectKind+"/"+environmentID, "Desactivó la IA de "+n.where(),
		Disabled{Project: projectID, Environment: environmentID})
}

// RevokeFailed v1: a key could not be revoked at the gateway and waits for the
// sweep (or a retry); IT should look at it. Reason is the gateway's refusal in
// plain words, never a key.
type RevokeFailed struct {
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Reason      string `json:"reason"`
}

func NewRevokeFailed(org identity.OrganizationID, actor identity.UserID, projectID, environmentID, reason string, n Names) event.Event {
	return event.New(EventRevokeFailed, 1, org, actor, EnvironmentSubjectKind+"/"+environmentID,
		"No se pudo revocar la llave de IA de "+n.where(),
		RevokeFailed{Project: projectID, Environment: environmentID, Reason: reason})
}

// RevocationRetried v1: the sweep took a failed revocation up again. Attempts is
// how many revocations had failed so far; the next failure waits longer.
type RevocationRetried struct {
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Attempts    int    `json:"attempts"`
}

// NewRevocationRetried is the system's (the actor is zero: nobody asked).
func NewRevocationRetried(org identity.OrganizationID, projectID, environmentID string, attempts int, n Names) event.Event {
	return event.New(EventRevocationRetried, 1, org, identity.UserID{}, EnvironmentSubjectKind+"/"+environmentID,
		"Se reintentó revocar la llave de IA de "+n.where(),
		RevocationRetried{Project: projectID, Environment: environmentID, Attempts: attempts})
}
