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
	EventAppsMoved          = "cluster.apps_moved"
	EventAppApplied         = "cluster.app_applied"
	EventAppRetired         = "cluster.app_retired"
	EventAppRemoved         = "cluster.app_removed"
	EventBuildSent          = "cluster.build_sent"
	EventBuildReleased      = "cluster.build_released"
	EventSecretSent         = "cluster.secret_sent"
	EventSecretDelivered    = "cluster.secret_delivered"
	EventSecretFailed       = "cluster.secret_failed"
	EventResourceApplied    = "cluster.resource_applied"
	EventResourceRemoved    = "cluster.resource_removed"
	EventEgressChanged      = "cluster.egress_changed"
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

// EgressChanged v1: TI changed what the apps of a cluster may reach.
type EgressChanged struct {
	Cluster    ClusterID  `json:"cluster_id"`
	Name       string     `json:"name"`
	Mode       EgressMode `json:"mode"`
	Allow      []string   `json:"allow"`
	Generation int64      `json:"generation"`
	// PreviousMode and PreviousAllow are what it was before.
	PreviousMode  EgressMode `json:"previous_mode"`
	PreviousAllow []string   `json:"previous_allow"`
}

func NewEgressChanged(org identity.OrganizationID, actor identity.UserID, c Cluster, before EgressSetting, after EgressSetting) event.Event {
	what := "Cambió la salida a internet del clúster " + c.Name
	if after.Mode == EgressRestricted {
		what = "Limitó la salida a internet del clúster " + c.Name + " a las direcciones permitidas"
	} else if before.Mode == EgressRestricted {
		what = "Abrió la salida a internet del clúster " + c.Name
	}
	return event.New(EventEgressChanged, 1, org, actor, event.Subject(SubjectKind, c.ID), what,
		EgressChanged{Cluster: c.ID, Name: c.Name, Mode: after.Mode, Allow: after.Allow, Generation: after.Generation,
			PreviousMode: before.Mode, PreviousAllow: before.Allow})
}

// AppsMoved v1: TI sent the apps of a revoked cluster to another (subject:
// the cluster they left).
type AppsMoved struct {
	Cluster      ClusterID `json:"cluster_id"`
	To           ClusterID `json:"to_cluster_id"`
	ToName       string    `json:"to_name"`
	Environments []string  `json:"environment_ids"`
	Apps         int       `json:"apps"`
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

// AppRetired v1: a retired app (its project archived) was stored for its
// cluster to remove.
type AppRetired struct {
	Cluster     ClusterID `json:"cluster_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
}

// AppRemoved v1: the cluster reported a retired app gone; it is forgotten.
type AppRemoved struct {
	Cluster     ClusterID `json:"cluster_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
}

const timeLayout = "2006-01-02T15:04:05Z07:00"

// BuildSent v1: a build went to the cluster of its environment.
type BuildSent struct {
	Cluster     ClusterID `json:"cluster_id"`
	Build       BuildID   `json:"build_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	SourceSHA   string    `json:"source_sha256"`
	Repository  string    `json:"image_repository"`
	// Placed is true when this placed the environment on the cluster.
	Placed bool `json:"placed"`
}

// BuildReleased v1: the cluster was told to drop a build: cancelled while
// it ran, or done with once it finished (Finished).
type BuildReleased struct {
	Cluster  ClusterID `json:"cluster_id"`
	Build    BuildID   `json:"build_id"`
	Finished bool      `json:"finished"`
}

func NewBuildSent(org identity.OrganizationID, c Cluster, b DesiredBuild, placed bool) event.Event {
	return event.New(EventBuildSent, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"Envió una construcción al clúster "+c.Name,
		BuildSent{Cluster: c.ID, Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String(),
			SourceSHA: b.SourceSHA256, Repository: b.ImageRepository, Placed: placed})
}

func NewBuildReleased(org identity.OrganizationID, b DesiredBuild) event.Event {
	what := "Canceló una construcción en el clúster"
	if b.Final {
		what = "Liberó una construcción terminada en el clúster"
	}
	return event.New(EventBuildReleased, 1, org, identity.UserID{}, event.Subject(SubjectKind, b.Cluster), what,
		BuildReleased{Cluster: b.Cluster, Build: b.ID, Finished: b.Final})
}

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

func NewAppsMoved(org identity.OrganizationID, actor identity.UserID, from, to Cluster, moved Moved) event.Event {
	envs := make([]string, len(moved.Environments))
	for i, e := range moved.Environments {
		envs[i] = e.String()
	}
	return event.New(EventAppsMoved, 1, org, actor, event.Subject(SubjectKind, from.ID),
		"Movió "+appCount(moved.Apps)+" del clúster "+from.Name+" a "+to.Name,
		AppsMoved{Cluster: from.ID, To: to.ID, ToName: to.Name, Environments: envs, Apps: moved.Apps})
}

func appCount(n int) string {
	if n == 1 {
		return "1 app"
	}
	return strconv.Itoa(n) + " apps"
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

func NewAppRetired(org identity.OrganizationID, c Cluster, r Retirement) event.Event {
	return event.New(EventAppRetired, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"Pidió al clúster "+c.Name+" retirar "+r.Name+" (app archivada)",
		AppRetired{Cluster: c.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Revision: r.Revision})
}

func NewAppRemoved(org identity.OrganizationID, c Cluster, a App) event.Event {
	return event.New(EventAppRemoved, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"El clúster "+c.Name+" retiró "+a.Name,
		AppRemoved{Cluster: c.ID, Project: a.Project.String(), Environment: a.Environment.String(), Name: a.Name, Revision: a.Revision})
}

// SecretSent v1: a sealed managed value went to the cluster (never the
// value: name, version and the key it was sealed to).
type SecretSent struct {
	Cluster     ClusterID        `json:"cluster_id"`
	Delivery    SecretDeliveryID `json:"delivery_id"`
	Project     string           `json:"project_id"`
	Environment string           `json:"environment_id"`
	App         string           `json:"app"`
	Name        string           `json:"name"`
	Version     int64            `json:"version"`
	KeyID       string           `json:"key_id"`
	// Placed is true when this placed the environment on the cluster.
	Placed bool `json:"placed"`
}

func NewSecretSent(org identity.OrganizationID, actor identity.UserID, c Cluster, d SecretDelivery, placed bool) event.Event {
	return event.New(EventSecretSent, 1, org, actor, event.Subject(SubjectKind, c.ID),
		"Envió el secreto "+d.Name+" de "+d.App+" al clúster "+c.Name,
		SecretSent{Cluster: c.ID, Delivery: d.ID, Project: d.Project.String(), Environment: d.Environment.String(), App: d.App,
			Name: d.Name, Version: d.Version, KeyID: d.KeyID, Placed: placed})
}

// SecretAcked v1 is the payload of cluster.secret_delivered (the cluster
// stored the value) and cluster.secret_failed (it could not, or nobody took
// it in time: Error says why; the person loads it again).
type SecretAcked struct {
	Cluster     ClusterID        `json:"cluster_id"`
	Delivery    SecretDeliveryID `json:"delivery_id"`
	Project     string           `json:"project_id"`
	Environment string           `json:"environment_id"`
	App         string           `json:"app"`
	Name        string           `json:"name"`
	Version     int64            `json:"version"`
	Error       string           `json:"error,omitempty"`
}

func NewSecretAcked(org identity.OrganizationID, d SecretDelivery, failure string) event.Event {
	typ, what := EventSecretDelivered, "El clúster guardó el secreto "+d.Name+" de "+d.App
	if failure != "" {
		typ, what = EventSecretFailed, "El clúster no pudo guardar el secreto "+d.Name+" de "+d.App
	}
	return event.New(typ, 1, org, identity.UserID{}, event.Subject(SubjectKind, d.Cluster), what,
		SecretAcked{Cluster: d.Cluster, Delivery: d.ID, Project: d.Project.String(), Environment: d.Environment.String(), App: d.App,
			Name: d.Name, Version: d.Version, Error: failure})
}

// ResourceApplied v1: a new desired generation of a resource (create, resize
// or delete) went to its cluster. Nothing about its credentials is here.
type ResourceApplied struct {
	Cluster     ClusterID `json:"cluster_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Generation  int64     `json:"generation"`
	StorageGi   int       `json:"storage_gi"`
	Deleted     bool      `json:"deleted"`
	// Placed is true when this placed the environment on the cluster.
	Placed bool `json:"placed"`
}

func NewResourceApplied(org identity.OrganizationID, c Cluster, r DesiredResource, placed bool) event.Event {
	what := "Envió"
	if r.Deleted {
		what = "Envió el borrado de"
	}
	return event.New(EventResourceApplied, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		what+" el recurso "+r.Name+" al clúster "+c.Name,
		ResourceApplied{Cluster: c.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind),
			Generation: r.Generation, StorageGi: r.StorageGi, Deleted: r.Deleted, Placed: placed})
}

// ResourceRemoved v1: the cluster reported a resource that was being deleted
// gone (its data erased); the resource domain forgets it.
type ResourceRemoved struct {
	Cluster     ClusterID `json:"cluster_id"`
	Project     string    `json:"project_id"`
	Environment string    `json:"environment_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
}

func NewResourceRemoved(org identity.OrganizationID, c Cluster, r DesiredResource) event.Event {
	return event.New(EventResourceRemoved, 1, org, identity.UserID{}, event.Subject(SubjectKind, c.ID),
		"El clúster "+c.Name+" borró el recurso "+r.Name,
		ResourceRemoved{Cluster: c.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind)})
}
