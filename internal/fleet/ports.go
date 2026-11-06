package fleet

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Commands are what platform people do with clusters, implemented by
// fleetsvc.Service.
type Commands interface {
	// Create registers a cluster and returns its first enrolment token.
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Created, error)
	// IssueToken replaces the cluster's enrolment token.
	IssueToken(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input IssueToken) (Token, error)
	Revoke(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input Revoke) error
	// Move sends every app of a revoked cluster to an active one.
	Move(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input Move) (Moved, error)
	// SetEgress replaces the cluster's egress policy and has it sent to its
	// agent (a policy identical to the stored one changes nothing and
	// publishes nothing). Business CodeClusterRevoked for a revoked cluster.
	SetEgress(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input SetEgress) (EgressStatus, error)
	// SweepSecrets drops secret deliveries no cluster took within
	// SecretDeliveryTTL (a scheduled task: a cluster that never reconnects
	// would otherwise keep them forever).
	SweepSecrets(ctx context.Context) (int, error)
}

// Queries are the read operations, implemented by fleetsvc.Service.
type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (Cluster, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Cluster], error)
	// Egress is the cluster's egress policy and how its agent has taken it.
	Egress(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (EgressStatus, error)
	// Placements is, for each environment (at most MaxPlacements) that runs
	// on a cluster, which one: one query for the inventory. An environment
	// not placed yet is absent. It changes nothing.
	Placements(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (map[project.EnvironmentID]ClusterID, error)
}

// Dispatch is what other domains use to run apps (release, through its
// releasefleet adapter), implemented by fleetsvc.Service.
type Dispatch interface {
	// Apply stores the desired app and has it sent to its cluster, placing
	// the environment on the organization's default cluster the first time.
	// It is idempotent; a revision older than the stored one changes nothing.
	Apply(ctx context.Context, app App) error
	// Retire marks the environment's app retired (App.Deleted) at r.Revision
	// and has its cluster remove it; the sealed values still waiting for it
	// are dropped. It joins the caller's transaction, never places an
	// environment (one never placed has nothing to remove: false) and keeps
	// the retirement for a cluster that is revoked or cannot retire yet. A
	// revision not above the stored one changes nothing (false).
	Retire(ctx context.Context, r Retirement) (bool, error)
	// Status is what the agent last reported of the app (ObservedRevision 0
	// when it has reported nothing yet).
	Status(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (AppStatus, error)
	// CanProtect reports whether the cluster the environment runs on lists
	// CapabilityAccess, i.e. can serve team and organization apps behind the
	// sign-in. An environment not placed yet is judged by the organization's
	// default cluster (where its first Apply would put it); false when there is
	// none, or its cluster was revoked. It changes nothing (no placement).
	CanProtect(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (bool, error)
	// CanRunServices reports whether the cluster of every environment (an
	// environment not placed yet: the organization's default) lists
	// CapabilityServices; false when one has no cluster or a revoked one. At
	// most MaxPlacements environments. It changes nothing.
	CanRunServices(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (bool, error)
	// SecretTarget is what the environment's cluster (or, unplaced, the
	// organization's default) offers for secrets. It changes nothing; a zero
	// Cluster when there is none or it was revoked.
	SecretTarget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (SecretTarget, error)
	// EgressTarget is the egress policy of the cluster the environment runs
	// on (or, unplaced, the organization's default) and how its agent took
	// it. It changes nothing; a zero Cluster when there is none or it was
	// revoked.
	EgressTarget(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (EgressTarget, error)
	// DeliverSecret stores a sealed managed value for the environment's
	// cluster and has it sent (replacing an older version still waiting). It
	// joins the caller's transaction. The value must be sealed to that
	// cluster's current key (Business CodeSecretKeyChanged otherwise) and the
	// environment placed or placeable (like Apply).
	DeliverSecret(ctx context.Context, delivery SecretDelivery) error
}

// Resources is what the resource domain uses to run databases, caches and
// volumes in clusters (its resourcefleet adapter), implemented by
// fleetsvc.Service.
type Resources interface {
	// ApplyResource stores the desired resource for the cluster of its
	// environment (placing the environment on the default cluster the first
	// time, like Apply) and has it sent. It fails closed with Business
	// CodeResourcesUnavailable when the cluster does not list the kind. It
	// joins the caller's transaction; a generation older than the stored
	// one changes nothing.
	ApplyResource(ctx context.Context, resource DesiredResource) error
	// ResourceStatus is what the agent last reported (ObservedGeneration 0
	// and no phase when it has reported nothing yet).
	ResourceStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (ResourceStatus, error)
	// ResourceKinds are the kinds the environment's cluster (or, unplaced,
	// the organization's default) can provision; none when there is no
	// cluster or it was revoked. It changes nothing.
	ResourceKinds(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]resourcekind.Kind, error)
}

// Builds is what the build domain uses to build in clusters (its buildfleet
// adapter), implemented by fleetsvc.Service. Every method is idempotent.
type Builds interface {
	// DispatchBuild stores the build for the cluster of its environment
	// (placing the environment on first use, like Apply) and has it sent.
	// A cluster that announced no builder refuses it (Business, CodeNoBuilder);
	// dispatching the same build again changes nothing.
	DispatchBuild(ctx context.Context, build DesiredBuild) error
	// CancelBuild has the cluster stop the build and drop it (a no-op for an
	// unknown build). It is also how a finished build is let go.
	CancelBuild(ctx context.Context, organization identity.OrganizationID, build BuildID) error
	// BuildStatus is what the agent last reported (Phase BuildPending when
	// nothing yet, or when the build is unknown).
	BuildStatus(ctx context.Context, organization identity.OrganizationID, build BuildID) (BuildStatus, error)
	// BuildLogs are the stored lines after seq, in order, at most limit.
	BuildLogs(ctx context.Context, organization identity.OrganizationID, build BuildID, after int64, limit int) (LogPage, error)
	// ForgetBuildLogs drops the stored lines once the log is archived
	// elsewhere; lines sent later are not stored.
	ForgetBuildLogs(ctx context.Context, organization identity.OrganizationID, build BuildID) error
	// BuildScan is where the scan of the build's image is: Pending until the
	// agent reports it, Unsupported when the build's cluster does not list
	// CapabilityScan (or the build is unknown), Done with the outcome.
	BuildScan(ctx context.Context, organization identity.OrganizationID, build BuildID) (BuildScan, error)
}

// Agents is what the gateway does on behalf of connected agents,
// implemented by fleetsvc.Service. The AgentRef always comes from the
// verified client certificate.
type Agents interface {
	// Enroll trades a one-time token and a CSR for the cluster's certificate.
	Enroll(ctx context.Context, token string, csr *x509.CertificateRequest) (Enrollment, error)
	// Authenticate accepts the certificate of an active cluster: the current
	// one or the one it replaced last (an agent mid-renewal, for at most
	// RenewalOverlap).
	Authenticate(ctx context.Context, agent AgentRef) error
	// Renew issues a new certificate for a new key. It accepts the
	// previous certificate too: an agent whose last renewal answer was lost
	// still holds it and must be able to retry.
	Renew(ctx context.Context, agent AgentRef, csr *x509.CertificateRequest) (Issued, error)
	// Recover returns the cluster's current certificate to the holder of
	// its key (csr is signed with it): an agent whose Enroll or Renew
	// answer was lost. It signs and changes nothing.
	Recover(ctx context.Context, input Recover, csr *x509.CertificateRequest) (Enrollment, error)
	// Connect records a new tunnel (it supersedes any other of the cluster).
	Connect(ctx context.Context, agent AgentRef, session Session, info Agent, address string) error
	// Heartbeat keeps the tunnel's cluster online; it fails with Rejected
	// once the cluster is revoked, the tunnel's certificate is no longer
	// accepted or another tunnel superseded this one.
	Heartbeat(ctx context.Context, agent AgentRef, session Session) error
	Disconnect(ctx context.Context, agent AgentRef, session Session) error
	// Desired is the cluster's whole desired state.
	Desired(ctx context.Context, agent AgentRef) ([]App, error)
	// DesiredApp is one app of the cluster's desired state (false when it is
	// not, or no longer, on this cluster).
	DesiredApp(ctx context.Context, agent AgentRef, environment project.EnvironmentID, name string) (App, bool, error)
	// Report stores what the agent observed, from the tunnel session (it
	// fails with Rejected when the tunnel is no longer the cluster's). Only
	// apps desired on the agent's own cluster are taken, at most at their
	// desired revision; the rest is ignored. A retired app reported gone
	// (phase deleted) at its revision is forgotten and published as
	// cluster.app_removed.
	Report(ctx context.Context, agent AgentRef, session Session, report Report) error

	// DesiredResources are the cluster's desired resources (the gateway
	// sends them before the apps that use them).
	DesiredResources(ctx context.Context, agent AgentRef) ([]DesiredResource, error)
	// DesiredResource is one of them (false when it is no longer this cluster's).
	DesiredResource(ctx context.Context, agent AgentRef, environment project.EnvironmentID, name string) (DesiredResource, bool, error)
	// ReportResources stores what the agent observed of resources, like
	// Report. A resource reported gone (phase deleted) that was being
	// deleted is forgotten and published as cluster.resource_removed.
	ReportResources(ctx context.Context, agent AgentRef, session Session, report ResourceReport) error
	// DesiredBuilds are the cluster's builds of the last BuildResendWithin
	// (wanted, cancelled and finished: the gateway decides what to send).
	DesiredBuilds(ctx context.Context, agent AgentRef) ([]DesiredBuild, error)
	// DesiredBuild is one of them (false when it is not this cluster's).
	DesiredBuild(ctx context.Context, agent AgentRef, build BuildID) (DesiredBuild, bool, error)
	// BuildSource authorizes a source download: the build must be wanted on
	// the agent's own cluster (NotFound otherwise, whoever's it is).
	BuildSource(ctx context.Context, agent AgentRef, build BuildID) (DesiredBuild, error)
	// ReportBuilds stores build statuses, like Report: only builds desired
	// on the agent's own cluster are taken, and a final phase never changes.
	ReportBuilds(ctx context.Context, agent AgentRef, session Session, statuses []BuildStatus) error
	// ReportScans stores the outcomes of image scans, like ReportBuilds: only
	// builds desired on the agent's own cluster are taken, and only when the
	// scanned digest is the one the build reported (an agent cannot attach a
	// scan to an image it did not build).
	ReportScans(ctx context.Context, agent AgentRef, session Session, scans []ScanStatus) error
	// LogBuild stores lines of a build of the agent's own cluster, dropping
	// the ones already stored and the ones beyond MaxBuildLogBytes.
	LogBuild(ctx context.Context, agent AgentRef, session Session, log BuildLog) error
	// DesiredSecrets are the managed values waiting for the agent's cluster.
	DesiredSecrets(ctx context.Context, agent AgentRef) ([]SecretDelivery, error)
	// DesiredSecret is one of them (false when it is gone or not this cluster's).
	DesiredSecret(ctx context.Context, agent AgentRef, delivery SecretDeliveryID) (SecretDelivery, bool, error)
	// AckSecret ends a delivery of the agent's own cluster while session is
	// its tunnel: the sealed value is deleted, stored or not (a failure is
	// published: the person loads it again).
	AckSecret(ctx context.Context, agent AgentRef, session Session, ack SecretAck) error
	// DesiredPolicy is the cluster's egress policy at its current generation
	// (the default, generation 0, until TI changes it).
	DesiredPolicy(ctx context.Context, agent AgentRef) (Policy, error)
	// AckPolicy stores the agent's answer to a policy while session is its
	// tunnel (an unknown or older generation is ignored).
	AckPolicy(ctx context.Context, agent AgentRef, session Session, ack PolicyAck) error
}

// Signals delivers the changes committed by any process (the worker applies
// apps; the API revokes clusters) to the gateways holding the tunnels.
// Delivery is best effort: Listen calls lost when it reconnected and may
// have missed some, and the gateway resends everything then.
type Signals interface {
	Listen(ctx context.Context, signal func(Signal), lost func()) error
}

// Authority is the fleet's certificate authority (adapter fleetca).
type Authority interface {
	// Trust is the CA's public side (what agents pin).
	Trust() (Trust, error)
	// Sign issues a client certificate naming uri for the CSR's key.
	Sign(csr *x509.CertificateRequest, uri string, now time.Time, ttl time.Duration) (Issued, error)
}

// Repository is the persistence contract; only the service depends on it.
// Every statement filters by organization.
type Repository interface {
	CreateCluster(ctx context.Context, cluster Cluster, tokenHash []byte) error
	FindCluster(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (Cluster, error)
	// FindClusterForUpdate locks the row until the transaction ends.
	FindClusterForUpdate(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (Cluster, error)
	// FindClusterForShare holds the row against FindClusterForUpdate (a
	// revocation or a move) until the transaction ends; heartbeats are not
	// held up.
	FindClusterForShare(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (Cluster, error)
	ListClusters(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Cluster], error)
	// FindDefaultCluster is the active cluster new environments go to (the
	// oldest enrolled); NotFound when there is none.
	FindDefaultCluster(ctx context.Context, organization identity.OrganizationID) (Cluster, error)
	SetToken(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, hash []byte, expiresAt time.Time) error
	// ConsumeToken spends the token if it matches and has not expired, in
	// the same statement (two agents racing with one token: one wins).
	ConsumeToken(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, hash []byte) (Cluster, error)
	// SetCertificate makes cert the cluster's certificate on enrolment (the
	// previous ones stop being accepted).
	SetCertificate(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, cert Issued) error
	// RenewCertificate makes cert the cluster's certificate; presented (the
	// one the agent renewed with) stays accepted until previousUntil.
	RenewCertificate(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, cert Issued, presented string, previousUntil time.Time) error
	// FindAcceptedSerials are the serials an active cluster may present
	// (current first).
	FindAcceptedSerials(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]string, error)
	// FindCertificate is the PEM of an active cluster's current certificate
	// (NotFound when there is none).
	FindCertificate(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]byte, error)
	Revoke(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, revokedBy identity.UserID) error
	SetSession(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, info Agent) error
	// FindEgress is the cluster's egress policy and what its agent answered
	// (NotFound for another organization's cluster).
	FindEgress(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (EgressSetting, error)
	// PutEgress stores the policy of a cluster not revoked, raises its
	// generation (returned), and signals its gateway.
	PutEgress(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, egress Egress) (int64, error)

	// Observations: what agents and gateways see, not decisions anybody
	// makes, so they publish no event (the architecture rule knows the
	// Observe prefix).
	//
	// ObserveHeartbeat refreshes the tunnel while it is the cluster's and
	// its certificate (serial) is still accepted.
	ObserveHeartbeat(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, serial string) (bool, error)
	// ObservePolicy stores the agent's answer to a policy while session is
	// the cluster's tunnel; an answer for a generation that was never sent,
	// or older than the one answered, is dropped.
	ObservePolicy(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, ack PolicyAck) (bool, error)
	ObserveDisconnect(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session) error
	// ObserveApps stores a report in one statement if session is still the
	// cluster's tunnel (false otherwise). A retired app reported "deleted"
	// at its revision is removed (desired state and status) and returned.
	ObserveApps(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, report Report) (bool, []App, error)

	// FindPlacement is the cluster an environment runs on (NotFound before
	// its first app).
	FindPlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (ClusterID, error)
	// ListPlacementsOf is FindPlacement for several environments at once.
	ListPlacementsOf(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (map[project.EnvironmentID]ClusterID, error)
	// Place pins an environment to a cluster; a concurrent first placement
	// wins and Place returns the cluster that won.
	Place(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, cluster ClusterID) (ClusterID, error)
	// MovePlacement re-pins an environment from a revoked cluster to
	// another: its desired apps move with it (and are signalled there);
	// what the old cluster reported is dropped. It returns how many apps
	// moved (NotFound when the environment is not on from).
	MovePlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, from, to ClusterID) (int, error)
	// ListPlacements are the environments pinned to a cluster.
	ListPlacements(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]project.EnvironmentID, error)
	// PutApp stores the desired app unless the stored revision is the same
	// or newer, and signals the gateways in the same transaction. It
	// reports whether it changed anything.
	PutApp(ctx context.Context, app App) (bool, error)
	// RetireApp marks the stored app retired (App.Deleted) at r.Revision
	// unless its revision is the same or newer, drops the sealed values still
	// waiting for it and signals its cluster's gateway, in the caller's
	// transaction. It returns the retired app (false: nothing changed).
	RetireApp(ctx context.Context, r Retirement) (App, bool, error)
	ListApps(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]App, error)
	FindApp(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, environment project.EnvironmentID, name string) (App, error)
	FindAppStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (AppStatus, error)

	// PutBuild stores a new desired build and signals its cluster's gateway
	// in the same transaction (false: it was already stored).
	PutBuild(ctx context.Context, build DesiredBuild) (bool, error)
	// CancelBuild marks the build no longer wanted and signals (false, with
	// the build, when it already was; NotFound when unknown).
	CancelBuild(ctx context.Context, organization identity.OrganizationID, build BuildID) (DesiredBuild, bool, error)
	FindBuild(ctx context.Context, organization identity.OrganizationID, build BuildID) (DesiredBuild, error)
	// ListBuilds are the cluster's builds changed after since.
	ListBuilds(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, since time.Time) ([]DesiredBuild, error)
	// FindBuildStatus is the last report, with Image filled (NotFound before one).
	FindBuildStatus(ctx context.Context, organization identity.OrganizationID, build BuildID) (BuildStatus, error)
	// ObserveBuilds stores statuses of builds desired on this cluster while
	// session is its tunnel (false otherwise); a final phase never changes.
	ObserveBuilds(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, statuses []BuildStatus) (bool, error)
	// ObserveScans stores scans of builds desired on this cluster while
	// session is its tunnel (false otherwise), keeping a scan only when its
	// digest is the one the build's succeeded status reported. A newer report
	// of the same build replaces the stored one (a rescan).
	ObserveScans(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, scans []ScanStatus) (bool, error)
	// FindBuildScan is the stored scan (NotFound before one).
	FindBuildScan(ctx context.Context, organization identity.OrganizationID, build BuildID) (ScanStatus, error)
	// ObserveBuildLog stores new lines of a build desired on this cluster
	// while session is its tunnel (false otherwise), up to maxBytes per build
	// (a line costs at least minLine).
	ObserveBuildLog(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, log BuildLog, maxBytes, minLine int64) (bool, error)
	// ListBuildLog pages the stored lines (NotFound for an unknown build).
	ListBuildLog(ctx context.Context, organization identity.OrganizationID, build BuildID, after int64, limit int) (LogPage, error)
	// ObserveLogsArchived drops the stored lines; later ones are not stored.
	ObserveLogsArchived(ctx context.Context, organization identity.OrganizationID, build BuildID) error

	// PutSecretDelivery stores the delivery, replacing one of the same app
	// and name with an older version, and signals its cluster's gateway
	// (false: a same or newer version is already waiting).
	PutSecretDelivery(ctx context.Context, delivery SecretDelivery) (bool, error)
	// ListSecretDeliveries are the cluster's deliveries created after since.
	ListSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, since time.Time) ([]SecretDelivery, error)
	FindSecretDelivery(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, delivery SecretDeliveryID) (SecretDelivery, error)
	// DeleteSecretDelivery removes a delivery of the cluster while session is
	// its tunnel (false when there is none, or the tunnel is not current).
	DeleteSecretDelivery(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, delivery SecretDeliveryID) (SecretDelivery, bool, error)
	// DeleteSecretDeliveries drops the cluster's deliveries created before
	// cutoff (never acknowledged in time) and returns them.
	DeleteSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, cutoff time.Time) ([]SecretDelivery, error)
	// DropSecretDeliveries drops every delivery of the cluster (it was
	// revoked: nobody can open them any more) and returns them.
	DropSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]SecretDelivery, error)
	// ExpireSecretDeliveries drops, across organizations, up to limit
	// deliveries created before cutoff (clusters that never came back) and
	// returns them. Concurrent sweeps skip each other's rows.
	ExpireSecretDeliveries(ctx context.Context, cutoff time.Time, limit int) ([]SecretDelivery, error)
	// RecordSecretFailure stores f as its secret's failure unless a newer
	// version already failed. A newer delivery (PutSecretDelivery) clears it.
	RecordSecretFailure(ctx context.Context, failure SecretFailure) error
	// ClearSecretFailure drops the secret's failure up to version (the
	// cluster stored that version, or a newer one).
	ClearSecretFailure(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, app, name string, version int64) error
	ListSecretFailures(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) ([]SecretFailure, error)

	// PutResource stores the desired resource unless the stored generation
	// is the same or newer (or it is another cluster's), signalling the
	// cluster's gateway; false when nothing changed.
	PutResource(ctx context.Context, resource DesiredResource) (bool, error)
	ListResources(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]DesiredResource, error)
	FindResource(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, environment project.EnvironmentID, name string) (DesiredResource, error)
	FindResourceStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (ResourceStatus, error)
	// ObserveResources upserts the reported statuses of resources desired on
	// this cluster while session is its tunnel (false when it is not). A
	// status never goes back to an older generation; one beyond the desired
	// generation is dropped. A resource being deleted whose report says
	// "deleted" at its generation is removed (desired state and status) and
	// returned.
	ObserveResources(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, report ResourceReport) (bool, []DesiredResource, error)
}
