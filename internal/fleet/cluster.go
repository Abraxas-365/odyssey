// Package fleet is the clusters Odyssey runs apps on. A cluster is
// enrolled once, with a one-time token, and from then on its agent keeps an
// outbound mutual-TLS tunnel to odyssey-gateway: the control plane never
// holds credentials of the customer's clusters, and a cluster behind a
// firewall needs no inbound port.
//
// The desired state of every app is stored here (an environment is pinned
// to the cluster it was first placed on) and pushed through the tunnel; what
// the agent observes is stored back. A cut tunnel loses nothing: the agent
// gets the whole desired state again when it reconnects, and apps keep
// running meanwhile.
package fleet

import (
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// Status is the lifecycle of a cluster.
type Status string

const (
	// StatusPending: created, waiting for its agent to enrol with the token.
	StatusPending Status = "pending"
	// StatusActive: enrolled; its agent may connect.
	StatusActive Status = "active"
	// StatusRevoked: its certificate is no longer accepted. Final.
	StatusRevoked Status = "revoked"
)

func (s Status) Valid() bool {
	return s == StatusPending || s == StatusActive || s == StatusRevoked
}

// StatusMessage is the 400 text for a value outside the set.
const StatusMessage = "status must be pending, active or revoked"

// Liveness of a connected agent: the gateway refreshes LastSeenAt every
// HeartbeatEvery while the tunnel is up; an agent unseen for longer than
// OnlineWithin is offline (its gateway died without saying so).
const (
	HeartbeatEvery = 30 * time.Second
	OnlineWithin   = 90 * time.Second
	// ReconnectQuiet: a tunnel opened within this long of the cluster's
	// last sighting, by the same agent version, is a reconnect, not news (no
	// cluster.connected event; a looping agent must not flood the trail).
	ReconnectQuiet = 10 * time.Minute
)

// Certificate lifetime: an agent renews when two thirds of it have passed,
// so a cluster offline for up to ten days still reconnects on its own.
const CertificateTTL = 30 * 24 * time.Hour

// RenewalOverlap is how long the certificate an agent renewed from stays
// accepted (at most): enough for an agent whose renewal answer was lost
// to retry with it (it retries every 10 min), short enough that a copy of
// an old key is soon useless. An agent that loses the answer and then stays
// away longer than this needs a new token: the price of that bound.
const RenewalOverlap = time.Hour

// RenewAfter: a renewal of the current certificate before this share of
// its life has passed is refused (an agent renews at two thirds; a looping
// one must not flood the CA and the audit trail).
const RenewAfter = CertificateTTL / 2

// RenewRetryAfter: a renewal from the previous certificate (a lost answer)
// is refused until the current one is this old, so the overlap allows a
// handful of retries, not a flood.
const RenewRetryAfter = 5 * time.Minute

// MaxCSR bounds a certificate request (a P-256 one is a few hundred bytes).
const MaxCSR = 4096

// Error codes of the fleet domain.
const (
	// CodeNoCluster: the organization has no enrolled cluster to place an app on.
	CodeNoCluster = "NO_CLUSTER"
	// CodeClusterRevoked: the app's cluster was revoked.
	CodeClusterRevoked = "CLUSTER_REVOKED"
	// CodePlacementMoved: a concurrent deployment moved the environment off
	// its revoked cluster first (transient: the workflow retries the apply).
	CodePlacementMoved = "PLACEMENT_MOVED"
	// CodeInvalidToken: an enrolment token that is unknown, used or expired.
	CodeInvalidToken = "INVALID_ENROLLMENT_TOKEN"
	// CodeRevoked: a request from an agent whose certificate is not accepted.
	CodeRevoked = "CLUSTER_CERTIFICATE_REJECTED"
	// CodeSuperseded: a tunnel another connection of its cluster replaced.
	CodeSuperseded = "TUNNEL_SUPERSEDED"
	// CodeFleetDisabled: this deployment has no fleet gateway configured.
	CodeFleetDisabled = "FLEET_DISABLED"
	// CodeRenewTooEarly: a renewal of a certificate that is still young.
	CodeRenewTooEarly = "CERTIFICATE_RENEWAL_TOO_EARLY"
)

// Cluster is one Kubernetes cluster of an organization.
type Cluster struct {
	ID           ClusterID               `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Name         string                  `json:"name" db:"name"`
	Status       Status                  `json:"status" db:"status"`
	// Agent is what its agent said when it last connected.
	Agent Agent `json:"agent" db:"-"`
	// Certificate is the client certificate it holds now (zero before enrolment).
	Certificate CertificateInfo `json:"certificate" db:"-"`
	// Connected is true while a gateway holds its tunnel and has heard from
	// it within OnlineWithin (computed with the database clock).
	Connected bool `json:"connected" db:"connected"`
	// TokenExpiresAt is when the pending enrolment token stops working (nil
	// when none is pending).
	TokenExpiresAt *time.Time      `json:"token_expires_at" db:"token_expires_at"`
	CreatedBy      identity.UserID `json:"created_by" db:"created_by"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	EnrolledAt     *time.Time      `json:"enrolled_at" db:"enrolled_at"`
	LastSeenAt     *time.Time      `json:"last_seen_at" db:"last_seen_at"`
	RevokedAt      *time.Time      `json:"revoked_at" db:"revoked_at"`
}

// Agent describes the agent of a cluster and what the cluster offers.
type Agent struct {
	Version      string       `json:"version"`
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities are what a cluster offers apps (placement and blueprints
// read them).
type Capabilities struct {
	KubernetesVersion string `json:"kubernetes_version"`
	// APIs are the optional platform API groups installed (CNPG, Kyverno…).
	APIs           []string `json:"apis"`
	IngressClasses []string `json:"ingress_classes"`
	StorageClasses []string `json:"storage_classes"`
}

// Limits of what an agent reports (it is outside the trust boundary of
// the control plane: a compromised cluster must not fill the database).
const (
	MaxReported     = 200 // items per capability list
	MaxReportedText = 200 // characters per reported string
	MaxReportApps   = 1000
)

// Validate bounds what an agent says of itself.
func (a Agent) Validate() error {
	if len(a.Capabilities.APIs) > MaxReported || len(a.Capabilities.IngressClasses) > MaxReported || len(a.Capabilities.StorageClasses) > MaxReported {
		return errx.Validation("too many capabilities reported")
	}
	for _, s := range append(append(append([]string{a.Version, a.Capabilities.KubernetesVersion}, a.Capabilities.APIs...),
		a.Capabilities.IngressClasses...), a.Capabilities.StorageClasses...) {
		if utf8.RuneCountInString(s) > MaxReportedText || text.HasForbiddenControl(s) {
			return errx.Validation("a reported capability is not valid")
		}
	}
	return nil
}

// CertificateInfo identifies a client certificate without its contents.
type CertificateInfo struct {
	Serial    string     `json:"serial"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Name rules.
const (
	MaxName     = 60
	NameMessage = "name must be 1-60 characters"
)

// Create registers a cluster and issues its first enrolment token.
type Create struct {
	Name      string          `json:"name"`
	CreatedBy identity.UserID `json:"-"` // taken from the token, never the body
}

func (c Create) Validate() error {
	if err := text.Required("name", c.Name, MaxName); err != nil {
		return err
	}
	if c.CreatedBy.IsZero() {
		return errx.Validation("created_by is required")
	}
	return nil
}

// IssueToken replaces the pending enrolment token of a cluster (the agent
// was not installed in time, or it must enrol again after losing its key:
// enrolling again invalidates the previous certificate).
type IssueToken struct {
	IssuedBy identity.UserID `json:"-"`
}

func (i IssueToken) Validate() error {
	if i.IssuedBy.IsZero() {
		return errx.Validation("issued_by is required")
	}
	return nil
}

// Revoke stops accepting the cluster's certificate: its tunnel is closed and
// no app is placed there again. Apps already running there keep running
// (the agent no longer gets changes); moving them is a later step.
type Revoke struct {
	RevokedBy identity.UserID `json:"-"`
}

func (r Revoke) Validate() error {
	if r.RevokedBy.IsZero() {
		return errx.Validation("revoked_by is required")
	}
	return nil
}

// Filter narrows a list of clusters.
type Filter struct {
	Status []Status
}

func (f Filter) Validate() error {
	for _, s := range f.Status {
		if !s.Valid() {
			return errx.Validation(StatusMessage).WithField("status")
		}
	}
	return nil
}

// Created is a new cluster with its enrolment token, shown once.
type Created struct {
	Cluster Cluster `json:"cluster"`
	Token   Token   `json:"enrollment"`
}

// AgentRef is an agent as its certificate names it: the gateway takes it
// from the verified client certificate, never from a message.
type AgentRef struct {
	Organization identity.OrganizationID
	Cluster      ClusterID
	Serial       string
}

// Session identifies one tunnel of an agent; a newer tunnel of the same
// cluster (another gateway replica, a reconnect) supersedes it.
type Session = identity.ID[sessionTag]

type sessionTag struct{}

func (sessionTag) IDName() string { return "session_id" }

func NewSession() Session { return identity.NewID[sessionTag]() }

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidAppName reports whether s may name an app in a cluster (a DNS label).
func ValidAppName(s string) bool { return namePattern.MatchString(s) }
