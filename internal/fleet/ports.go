package fleet

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Commands are what platform people do with clusters, implemented by
// fleetsvc.Service.
type Commands interface {
	// Create registers a cluster and returns its first enrolment token.
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Created, error)
	// IssueToken replaces the cluster's enrolment token.
	IssueToken(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input IssueToken) (Token, error)
	Revoke(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, input Revoke) error
}

// Queries are the read operations, implemented by fleetsvc.Service.
type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) (Cluster, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Cluster], error)
}

// Dispatch is what other domains use to run apps (release, through its
// releasefleet adapter), implemented by fleetsvc.Service.
type Dispatch interface {
	// Apply stores the desired app and has it sent to its cluster, placing
	// the environment on the organization's default cluster the first time.
	// It is idempotent; a revision older than the stored one changes nothing.
	Apply(ctx context.Context, app App) error
	// Status is what the agent last reported of the app (ObservedRevision 0
	// when it has reported nothing yet).
	Status(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (AppStatus, error)
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
	// desired revision; the rest is ignored.
	Report(ctx context.Context, agent AgentRef, session Session, report Report) error
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
	Revoke(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, revokedBy identity.UserID) error
	SetSession(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, info Agent) error

	// Observations: what agents and gateways see, not decisions anybody
	// makes, so they publish no event (the architecture rule knows the
	// Observe prefix).
	//
	// ObserveHeartbeat refreshes the tunnel while it is the cluster's and
	// its certificate (serial) is still accepted.
	ObserveHeartbeat(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, serial string) (bool, error)
	ObserveDisconnect(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session) error
	// ObserveApps stores a report in one statement if session is still the
	// cluster's tunnel (false otherwise).
	ObserveApps(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, session Session, report Report) (bool, error)

	// FindPlacement is the cluster an environment runs on (NotFound before
	// its first app).
	FindPlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (ClusterID, error)
	// Place pins an environment to a cluster; a concurrent first placement
	// wins and Place returns the cluster that won.
	Place(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, cluster ClusterID) (ClusterID, error)
	// MovePlacement re-pins an environment from a revoked cluster to
	// another: its desired apps move with it (and are signalled there);
	// what the old cluster reported is dropped.
	MovePlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, from, to ClusterID) error
	// PutApp stores the desired app unless the stored revision is the same
	// or newer, and signals the gateways in the same transaction. It
	// reports whether it changed anything.
	PutApp(ctx context.Context, app App) (bool, error)
	ListApps(ctx context.Context, organization identity.OrganizationID, cluster ClusterID) ([]App, error)
	FindApp(ctx context.Context, organization identity.OrganizationID, cluster ClusterID, environment project.EnvironmentID, name string) (App, error)
	FindAppStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (AppStatus, error)
}
