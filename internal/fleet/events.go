package fleet

import (
	"sort"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the fleet's public contract. Payloads carry facts and IDs;
// add fields freely, rename or remove one only in a new version. Heartbeats,
// disconnects and what agents observe are not events (they are
// observations; the deployment events carry their outcome).
const (
	EventCreated            = "cluster.created"
	EventTokenIssued        = "cluster.token_issued"
	EventEnrolled           = "cluster.enrolled"
	EventCertificateRenewed = "cluster.certificate_renewed"
	EventConnected          = "cluster.connected"
	EventRevoked            = "cluster.revoked"
	EventAppApplied         = "cluster.app_applied"
)

// SubjectKind prefixes the fleet's event subjects ("cluster/<id>").
const SubjectKind = "cluster"

// ClusterCreated v1.
type ClusterCreated struct {
	Cluster        ClusterID `json:"cluster_id"`
	Name           string    `json:"name"`
	TokenExpiresAt string    `json:"token_expires_at"`
}

// TokenIssued v1: a new enrolment token replaced the pending one.
type TokenIssued struct {
	Cluster        ClusterID `json:"cluster_id"`
	TokenExpiresAt string    `json:"token_expires_at"`
}

// Enrolled v1: the agent traded the token for its certificate.
type Enrolled struct {
	Cluster   ClusterID `json:"cluster_id"`
	Serial    string    `json:"certificate_serial"`
	ExpiresAt string    `json:"certificate_expires_at"`
}

// CertificateRenewed v1.
type CertificateRenewed struct {
	Cluster   ClusterID `json:"cluster_id"`
	Serial    string    `json:"certificate_serial"`
	ExpiresAt string    `json:"certificate_expires_at"`
}

// Connected v1: an agent opened a tunnel (like a login: from where, which
// version, what the cluster offers).
type Connected struct {
	Cluster           ClusterID `json:"cluster_id"`
	Address           string    `json:"address"`
	AgentVersion      string    `json:"agent_version"`
	KubernetesVersion string    `json:"kubernetes_version"`
}

// Revoked v1.
type Revoked struct {
	Cluster ClusterID `json:"cluster_id"`
	Name    string    `json:"name"`
}

// AppApplied v1: a new desired revision of an app went to its cluster.
// Env values are not in the trail; their names are.
type AppApplied struct {
	Cluster     ClusterID `json:"cluster_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
	Image       string    `json:"image"`
	EnvKeys     []string  `json:"env_keys"`
	// Placed is true when this placed the environment on the cluster.
	Placed bool `json:"placed"`
}

const timeLayout = "2006-01-02T15:04:05Z07:00"

func NewClusterCreated(org identity.OrganizationID, actor identity.UserID, c Cluster) event.Event {
	return event.New(EventCreated, 1, org, actor, event.Subject(SubjectKind, c.ID), "Registró el clúster "+c.Name,
		ClusterCreated{Cluster: c.ID, Name: c.Name, TokenExpiresAt: c.TokenExpiresAt.UTC().Format(timeLayout)})
}

func NewTokenIssued(org identity.OrganizationID, actor identity.UserID, c Cluster, t Token) event.Event {
	return event.New(EventTokenIssued, 1, org, actor, event.Subject(SubjectKind, c.ID),
		"Generó un token de conexión para el clúster "+c.Name,
		TokenIssued{Cluster: c.ID, TokenExpiresAt: t.ExpiresAt.UTC().Format(timeLayout)})
}

func NewEnrolled(org identity.OrganizationID, c Cluster, cert Issued) event.Event {
	return event.New(EventEnrolled, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"El clúster "+c.Name+" se conectó a Odyssey por primera vez",
		Enrolled{Cluster: c.ID, Serial: cert.Serial, ExpiresAt: cert.ExpiresAt.UTC().Format(timeLayout)})
}

func NewCertificateRenewed(org identity.OrganizationID, c Cluster, cert Issued) event.Event {
	return event.New(EventCertificateRenewed, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"El clúster "+c.Name+" renovó su certificado",
		CertificateRenewed{Cluster: c.ID, Serial: cert.Serial, ExpiresAt: cert.ExpiresAt.UTC().Format(timeLayout)})
}

func NewConnected(org identity.OrganizationID, c Cluster, info Agent, address string) event.Event {
	return event.New(EventConnected, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"El agente del clúster "+c.Name+" se conectó",
		Connected{Cluster: c.ID, Address: address, AgentVersion: info.Version, KubernetesVersion: info.Capabilities.KubernetesVersion})
}

func NewRevoked(org identity.OrganizationID, actor identity.UserID, c Cluster) event.Event {
	return event.New(EventRevoked, 1, org, actor, event.Subject(SubjectKind, c.ID), "Desconectó el clúster "+c.Name,
		Revoked{Cluster: c.ID, Name: c.Name})
}

func NewAppApplied(org identity.OrganizationID, c Cluster, a App, placed bool) event.Event {
	keys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return event.New(EventAppApplied, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"Envió la revisión "+strconv.FormatInt(a.Revision, 10)+" de "+a.Name+" al clúster "+c.Name,
		AppApplied{Cluster: c.ID, Project: a.Project.String(), Environment: a.Environment.String(), Name: a.Name,
			Revision: a.Revision, Image: a.Image, EnvKeys: keys, Placed: placed})
}
