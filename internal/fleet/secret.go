package fleet

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

// Secrets (2a). An app's secret variables travel as references in App.Secrets;
// a managed value travels once, sealed in the browser to its cluster's key
// (pkg/secretbox), as a SecretDelivery the gateway pushes until the agent
// acknowledges it. The control plane can never open it.

// Secret sources (proto SecretRef.source).
const (
	SecretManaged  = "managed"
	SecretExternal = "external"
)

// AppSecret is one secret variable of an app: never a value.
type AppSecret struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Version and KeyID of a managed value: the agent waits for this
	// version; KeyID is the cluster key it was sealed to.
	Version int64  `json:"version,omitempty"`
	KeyID   string `json:"key_id,omitempty"`
	// Store, Key and Property locate an external value.
	Store    string `json:"store,omitempty"`
	Key      string `json:"key,omitempty"`
	Property string `json:"property,omitempty"`
}

type deliveryTag struct{}

func (deliveryTag) IDName() string { return "secret_delivery_id" }

// SecretDeliveryID names one delivery (the agent's ack echoes it).
type SecretDeliveryID = identity.ID[deliveryTag]

func NewSecretDeliveryID() SecretDeliveryID { return identity.NewID[deliveryTag]() }
func ParseSecretDeliveryID(raw string) (SecretDeliveryID, error) {
	return identity.ParseID[deliveryTag](raw)
}

// SecretDelivery is a managed value on its way to a cluster. Only the
// newest version of a secret waits: a newer one replaces it.
type SecretDelivery struct {
	ID           SecretDeliveryID        `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	Cluster      ClusterID               `json:"cluster_id" db:"cluster_id"`
	App          string                  `json:"app" db:"app"`
	Name         string                  `json:"name" db:"name"`
	Version      int64                   `json:"version" db:"version"`
	KeyID        string                  `json:"key_id" db:"key_id"`
	Sealed       []byte                  `json:"-" db:"sealed"`
	CreatedBy    identity.UserID         `json:"created_by" db:"created_by"`
	CreatedAt    time.Time               `json:"created_at" db:"created_at"`
}

// SecretDeliveryTTL is how long a value waits for its cluster: after that
// (the cluster was offline for a week) it must be loaded again.
const SecretDeliveryTTL = 7 * 24 * time.Hour

// Validate guards the fleet's invariants of a delivery.
func (d SecretDelivery) Validate() error {
	switch {
	case d.Organization.IsZero() || d.Project.IsZero() || d.Environment.IsZero() || d.Cluster.IsZero():
		return errx.Validation("secret delivery needs organization, project, environment and cluster")
	case !ValidAppName(d.App):
		return errx.Validation("secret delivery app must be a DNS label")
	case !envVarPattern.MatchString(d.Name):
		return errx.Validation("secret name must be an environment variable name")
	case d.Version < 1:
		return errx.Validation("secret version must be positive")
	case d.KeyID == "" || len(d.KeyID) > 64:
		return errx.Validation("secret key id is required")
	case len(d.Sealed) < secretbox.MinSealed || len(d.Sealed) > secretbox.MaxSealed:
		return errx.Validation("sealed secret has an impossible size")
	case d.CreatedAt.IsZero():
		return errx.Validation("secret delivery needs its time (the TTL counts from it)")
	}
	return nil
}

// SecretAck is the agent's answer to a delivery.
type SecretAck struct {
	Delivery SecretDeliveryID
	// Error is empty when the cluster stored the value (or a newer one).
	Error string
}

// MaxAckError bounds the agent's words.
const MaxAckError = 500

// SecretTarget is what the cluster an environment runs on (or would first
// run on: the organization's default) offers for secrets.
type SecretTarget struct {
	// Cluster is zero when the organization has no cluster for it.
	Cluster  ClusterID
	Key      *SecretKey
	External bool
	Stores   []string
	// Revoked: the environment's cluster was revoked (it offers nothing
	// until the environment moves).
	Revoked bool
	// Failures are the environment's secrets whose last delivery did not
	// arrive.
	Failures []SecretFailure
}

// SecretFailure is the last delivery of a secret that did not arrive: the
// agent could not store it, or nobody took it within SecretDeliveryTTL. A
// newer delivery of the secret, or the cluster storing one, clears it.
type SecretFailure struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Environment  project.EnvironmentID   `db:"environment_id"`
	Cluster      ClusterID               `db:"cluster_id"`
	App          string                  `db:"app"`
	Name         string                  `db:"name"`
	Version      int64                   `db:"version"`
	Error        string                  `db:"error"`
	FailedAt     time.Time               `db:"failed_at"`
}

// Failure is d's failure with error (words for a person, bounded).
func (d SecretDelivery) Failure(error string) SecretFailure {
	return SecretFailure{Organization: d.Organization, Environment: d.Environment, Cluster: d.Cluster, App: d.App,
		Name: d.Name, Version: d.Version, Error: error}
}

// CodeSecretStoreUnknown: the external secret names a store its cluster
// does not have.
const CodeSecretStoreUnknown = "SECRET_STORE_UNKNOWN"

// CheckSecrets is Apply's guard: a cluster that cannot hold an app's secrets
// must not get it (the agent would wait for a value that never comes).
func (c Capabilities) CheckSecrets(secrets []AppSecret) error {
	for _, s := range secrets {
		switch s.Source {
		case SecretManaged:
			if c.SecretKey == nil {
				return errx.Business(SecretsUnavailableMessage).WithCode(CodeSecretsUnavailable)
			}
			if s.KeyID != c.SecretKey.KeyID {
				return errx.Business("El clúster de este entorno cambió su llave: vuelve a cargar el secreto "+s.Name+" en la consola.").
					WithCode(CodeSecretKeyChanged).WithDetail("name", s.Name)
			}
		case SecretExternal:
			if !c.ExternalSecrets() {
				return errx.Business(ExternalSecretsUnavailableMessage).WithCode(CodeExternalSecretsUnavailable)
			}
			if !containsString(c.SecretStores, s.Store) {
				return errx.Business("El clúster no tiene el almacén de secretos "+s.Store+" (secreto "+s.Name+"). Pide a TI que lo revise.").
					WithCode(CodeSecretStoreUnknown).WithDetail("name", s.Name)
			}
		}
	}
	return nil
}

func containsString(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
