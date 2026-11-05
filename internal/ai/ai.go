// Package ai is an app's access to the organization's AI gateway (2d-ai):
// the people who build apps turn it on and the app receives the standard
// variables (OPENAI_API_KEY, OPENAI_BASE_URL, ANTHROPIC_API_KEY,
// ANTHROPIC_BASE_URL) that every SDK reads. The domain owns the lifecycle of
// the key (made at the gateway, delivered sealed to the app's cluster as a
// managed secret, revoked when the AI is turned off or the key is rotated);
// the settings that wire the variables into the app are release's, reached
// through the Release port (never releasesvc). The key exists in clear only
// in the worker's memory between the gateway making it and the sealing: it
// is never stored, logged, put in an event or a workflow output.
package ai

import (
	"errors"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Codes of the AI.
const (
	// CodeUnavailable: the AI is not connected in this organization (or the
	// environment's cluster cannot run it).
	CodeUnavailable = "AI_UNAVAILABLE"
	// CodeBusy: another change of the app's AI is still running.
	CodeBusy = "AI_BUSY"
	// CodeNotEnabled: there is no AI to renew.
	CodeNotEnabled = "AI_NOT_ENABLED"
	// CodeSecretsUnavailable is release's code of the same name: the cluster
	// announced no secret key, so the key cannot travel to it.
	CodeSecretsUnavailable = "SECRETS_UNAVAILABLE"
)

// Variables are what the app receives while its AI is on. The agent sets
// them (they win over anything the app defines).
var Variables = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}

// ErrUnavailable is the 422 for an organization without the AI.
func ErrUnavailable() error {
	return errx.Business("La IA no está habilitada en esta organización. Pide a TI que la conecte.").WithCode(CodeUnavailable)
}

// ErrClusterUnavailable is the 422 for a cluster whose agent cannot wire the AI.
func ErrClusterUnavailable() error {
	return errx.Business("El clúster de este entorno todavía no puede dar IA a las apps. Pide a TI que actualice su agente.").WithCode(CodeUnavailable)
}

// ErrNoSecrets is the 422 for a cluster that cannot take the key.
func ErrNoSecrets() error {
	return errx.Business("Este clúster todavía no puede guardar secretos, así que no puede recibir la llave de la IA. Pide a TI que actualice su agente.").
		WithCode(CodeSecretsUnavailable)
}

// ErrBusy is the 409 for a change that waits for another.
func ErrBusy() error {
	return errx.Conflict("Otro cambio de la IA de esta app sigue en curso. Espera a que termine.").WithCode(CodeBusy)
}

// ErrNotEnabled is the 409 for renewing what is off.
func ErrNotEnabled() error {
	return errx.Conflict("La IA de esta app no está activada.").WithCode(CodeNotEnabled)
}

// NotFound is the 404 for a key of another environment or tenant.
func NotFound() error { return errx.NotFound("ai key not found") }

// Status is where a key is in its life: creating (being made and delivered)
// → active → revoking (until the gateway confirmed) → forgotten (row gone).
// A revocation that gave up (the gateway down or not connected) is
// revoke_failed: visible, retried by the sweep and by turning the AI off again.
type Status string

const (
	StatusCreating Status = "creating"
	StatusActive   Status = "active"
	StatusRevoking Status = "revoking"
	// StatusRevokeFailed: the app no longer uses the key, but the gateway
	// could not be told to revoke it. IT has to look at it unless the sweep
	// gets there first.
	StatusRevokeFailed Status = "revoke_failed"
)

func (s Status) Valid() bool {
	return s == StatusCreating || s == StatusActive || s == StatusRevoking || s == StatusRevokeFailed
}

// WarningRevokeFailed is what the view says (for the app's people and for IT)
// while a key could not be revoked.
const WarningRevokeFailed = "No se pudo revocar la llave en el gateway de IA; TI debe revisarlo."

// ReasonNoGateway is the reason stored when the AI is turned off in an
// organization whose gateway is not connected.
const ReasonNoGateway = "La IA no está conectada en esta organización: no hay gateway al que pedirle que revoque la llave."

// MaxReason bounds the stored reason.
const MaxReason = 300

// FailureReason is the reason stored for a revocation that failed with cause:
// the message of one of our own errors (the gateway adapters never put a key
// in them), bounded; anything else is not shown to people.
func FailureReason(cause error) string {
	r := "La revocación falló."
	var e *errx.Error
	if errors.As(cause, &e) && e.Message != "" {
		r = e.Message
	}
	if rs := []rune(r); len(rs) > MaxReason {
		r = string(rs[:MaxReason])
	}
	return r
}

// Key is a key the platform made for an environment's app. The secret is
// never here: Name is what it is called at the gateway, ProviderKeyID how to
// revoke it.
type Key struct {
	ID           KeyID                   `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	// ProviderKeyID is the gateway's id of the key (nil until it exists and
	// was delivered).
	ProviderKeyID *string         `json:"provider_key_id" db:"provider_key_id"`
	Name          string          `json:"name" db:"name"`
	Status        Status          `json:"status" db:"status"`
	CreatedBy     identity.UserID `json:"created_by" db:"created_by"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
	// RevokeError is why the last revocation failed (revoke_failed only).
	RevokeError string `json:"revoke_error" db:"revoke_error"`
	// RevokeAttempts counts the revocations that failed since a person last
	// turned the AI off (the sweep's retries add to it; a person's retry
	// starts over). NextRetryAt is when the sweep may try again (nil: now).
	// They are the sweep's backoff; the view does not show them.
	RevokeAttempts int        `json:"revoke_attempts" db:"revoke_attempts"`
	NextRetryAt    *time.Time `json:"next_retry_at" db:"next_retry_at"`
}

// NamePrefix is what every key of the environment is called at the gateway
// starts with.
func NamePrefix(environment project.EnvironmentID) string {
	return "odicea-" + environment.String() + "-"
}

// KeyName is the deterministic name of a key at the gateway: finding the keys
// of an environment (or of one attempt) never needs a stored id.
func KeyName(environment project.EnvironmentID, id KeyID) string {
	return NamePrefix(environment) + id.String()
}

// SecretName is the managed secret that carries the key to the cluster
// (release.AIKeySecret).
const SecretName = "ODICEA_AI_KEY"

// State of an environment's AI, as people read it. revoke_failed: the app no
// longer has the AI but a key could not be revoked at the gateway (Warning
// says so; turning the AI off again retries it).
const (
	StateOff          = "off"
	StateEnabling     = "enabling"
	StateEnabled      = "enabled"
	StateRotating     = "rotating"
	StateDisabling    = "disabling"
	StateRevokeFailed = "revoke_failed"
)

// View is the AI of an app's environment.
type View struct {
	// Available: the AI can be turned on here (the gateway is connected and
	// the environment's cluster can run it); Message says why not.
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
	// Enabled: the app's settings carry the AI (its key is delivered).
	Enabled   bool     `json:"enabled"`
	Status    string   `json:"status"`
	BaseURL   string   `json:"base_url"`
	Variables []string `json:"variables"`
	// EnabledBy and EnabledAt are who made the key in force and when.
	EnabledBy *identity.UserID `json:"enabled_by"`
	EnabledAt *time.Time       `json:"enabled_at"`
	// Warning is set while a key could not be revoked at the gateway (also
	// when the app has the AI again with a new key); RevokeError is why.
	Warning     string `json:"warning,omitempty"`
	RevokeError string `json:"revoke_error,omitempty"`
}

// NewView puts an environment's keys together as a View (available and
// message come from the cluster and the gateway, not from the rows).
func NewView(keys []Key, baseURL string, available bool, message string) View {
	v := View{Available: available, Message: message, BaseURL: strings.TrimRight(baseURL, "/"),
		Variables: append([]string{}, Variables...), Status: StateOff}
	var active *Key
	var creating, revoking, failed bool
	for i := range keys {
		switch keys[i].Status {
		case StatusActive:
			active = &keys[i]
		case StatusCreating:
			creating = true
		case StatusRevoking:
			revoking = true
		case StatusRevokeFailed:
			failed = true
			if v.RevokeError == "" {
				v.RevokeError = keys[i].RevokeError
			}
		}
	}
	if failed {
		v.Warning = WarningRevokeFailed
	}
	switch {
	case active != nil:
		v.Enabled, v.Status = true, StateEnabled
		if creating || revoking {
			v.Status = StateRotating
		}
		by, at := active.CreatedBy, active.CreatedAt
		v.EnabledBy, v.EnabledAt = &by, &at
	case creating:
		v.Status = StateEnabling
	case revoking:
		v.Status = StateDisabling
	case failed:
		v.Status = StateRevokeFailed
	}
	return v
}

// Enable, Disable and Rotate are the requests; RequestedBy is the person.
type Enable struct{ RequestedBy identity.UserID }
type Disable struct{ RequestedBy identity.UserID }
type Rotate struct{ RequestedBy identity.UserID }

// MessageOf is the plain text of an error of this package (what people read),
// without the code errx puts in Error().
func MessageOf(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
