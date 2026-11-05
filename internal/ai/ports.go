package ai

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Commands are the write operations, implemented by aisvc.Service.
type Commands interface {
	// Enable turns the app's AI on: it records the intent and starts the
	// workflow that makes the key, hands it sealed to the cluster and wires
	// the app (a configuration deployment). Already on (or on its way) it
	// changes nothing. 422 AI_UNAVAILABLE when the organization has no AI or
	// the environment's cluster cannot run it; 409 VARIABLE_TAKEN when the
	// app defines a variable the AI sets.
	Enable(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Enable) (View, error)
	// Rotate gives the app a new key; the old one is revoked once the app
	// runs with the new one.
	Rotate(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Rotate) (View, error)
	// Disable turns the AI off and revokes the keys. Off already: nothing.
	// Asked again while a revocation failed (status revoke_failed), it tries
	// the revocation again.
	Disable(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input Disable) (View, error)
	// Sweep retries the revocations that failed, in every organization, once
	// the gateway is connected. It returns how many environments it retried.
	Sweep(ctx context.Context) (int, error)
}

// Queries are the read operations, implemented by aisvc.Service.
type Queries interface {
	Get(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (View, error)
}

// Workflows are the module's workflow definitions, registered by the
// composition root.
type Workflows interface {
	Workflows() []workflow.Definition
}

// Repository is the persistence contract of ai.keys; only the service
// depends on it. Every statement filters by organization (another tenant's
// row is NotFound).
type Repository interface {
	// LockEnvironment takes a transaction-scoped lock on the environment's
	// keys, so two changes of its AI cannot interleave.
	LockEnvironment(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) error
	Create(ctx context.Context, key Key) error
	Find(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id KeyID) (Key, error)
	// List returns the environment's keys, oldest first.
	List(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]Key, error)
	// Activate makes the creating key the active one, remembering its id at
	// the gateway, and sets the key that was active (if any) revoking. replaced
	// is true when there was one. changed is false when the key already was
	// the active one (a retry); NotFound when it is gone or not creating.
	Activate(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id KeyID, providerKeyID string) (changed, replaced bool, err error)
	// MarkRevoking sets every key of the environment revoking (a key whose
	// revocation failed earlier is tried again, the backoff starting over: a
	// person asked) and returns them.
	MarkRevoking(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]Key, error)
	// FailRevocation gives up on those of ids that are still revoking: they become
	// revoke_failed with reason (what the console and IT read), one more failed
	// attempt is counted and the sweep's next try is set at now + min(5 min ×
	// 2^(attempts-1), 6 h). Returns them (RevokeAttempts 1 is the first failure).
	FailRevocation(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, ids []KeyID, reason string) ([]Key, error)
	// FailKey does the same for one key that is creating or revoking (an
	// attempt nobody can clean up now); false when it is anything else.
	FailKey(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id KeyID, reason string) (Key, bool, error)
	// RetryRevocation is the sweep's: it sets the environment's revoke_failed
	// keys that are due (next_retry_at passed or never set) revoking again and
	// clears their reason; the attempts stay, so a new failure waits longer.
	// It returns them.
	RetryRevocation(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]Key, error)
	// ListRevokeFailed returns one due revoke_failed key per environment, across
	// organizations (the sweep's: each environment is then handled within its
	// own organization): the ones that never waited first, then the longest
	// due, so a key that keeps failing (it moves to the future) cannot starve
	// the rest.
	ListRevokeFailed(ctx context.Context, limit int) ([]Key, error)
	// Delete removes the row; false when it was not there.
	Delete(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id KeyID) (bool, error)
	// DeleteCreating removes the row only while it is creating (an attempt
	// that failed): false when it is anything else or gone.
	DeleteCreating(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, id KeyID) (bool, error)
}

// Provider is the AI gateway (adapters aifreerouter, aifake): where keys are
// made and revoked. Names are chosen by the platform (KeyName), so keys can
// be found again without storing anything.
type Provider interface {
	CreateKey(ctx context.Context, name string) (ProviderKey, error)
	RevokeKey(ctx context.Context, id string) error
	// ListKeys returns the live (not revoked) keys whose name starts with prefix.
	ListKeys(ctx context.Context, prefix string) ([]KeyInfo, error)
}

// Release is how ai reaches the release domain (the airelease adapter):
// release records the app's use of the AI in the settings it deploys and
// hands the sealed key to the cluster in the same transaction.
type Release interface {
	Target(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (Target, error)
	// Enable joins the caller's transaction. Asking for what is already in
	// force changes nothing.
	Enable(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, d Delivery) (Applied, error)
	// Disable turns the app's AI off (no revision when it already is).
	Disable(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, by identity.UserID) (Applied, error)
	// Settled: the cluster was told the revision and no deployment is in
	// progress (or nothing is online), so the app no longer needs the keys
	// an earlier revision named.
	Settled(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, revision int64) (bool, error)
}
