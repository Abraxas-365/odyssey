package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/google/uuid"
)

// TokenTTL is how long an enrolment token works.
const TokenTTL = 24 * time.Hour

// tokenPrefix makes a leaked token recognisable (secret scanners, logs).
const tokenPrefix = "odyssey-enroll-"

// Token is an enrolment token with what the agent needs to reach and trust
// the gateway. Value is shown once and stored only as a hash.
type Token struct {
	Value     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// Gateway is the host:port the agent dials.
	Gateway string `json:"gateway"`
	// CAPin is "sha256:<hex>" of the fleet CA's public key: the agent trusts
	// the gateway only if its chain ends at that CA (nothing else to copy).
	CAPin string `json:"ca_pin"`
}

// NewToken generates the token of a cluster: it carries the organization
// and cluster it enrols (so the lookup filters by organization like every
// other) and 32 random bytes.
func NewToken(organization identity.OrganizationID, cluster ClusterID) (value string, hash []byte) {
	b := make([]byte, 64)
	org, id := organization.UUID(), cluster.UUID()
	copy(b, org[:])
	copy(b[16:], id[:])
	if _, err := rand.Read(b[32:]); err != nil {
		panic("fleet: crypto/rand: " + err.Error()) // the OS has no entropy: nothing sensible to do
	}
	value = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return value, HashToken(value)
}

// HashToken is what is stored of a token.
func HashToken(value string) []byte {
	h := sha256.Sum256([]byte(value))
	return h[:]
}

// ParseToken reads the organization and cluster a token claims; whether it
// is valid is decided by comparing its hash with the stored one.
func ParseToken(value string) (identity.OrganizationID, ClusterID, error) {
	invalid := InvalidToken()
	raw, ok := strings.CutPrefix(strings.TrimSpace(value), tokenPrefix)
	if !ok {
		return identity.OrganizationID{}, ClusterID{}, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != 64 {
		return identity.OrganizationID{}, ClusterID{}, invalid
	}
	org, _ := uuid.FromBytes(b[:16])
	id, _ := uuid.FromBytes(b[16:32])
	if org == uuid.Nil || id == uuid.Nil {
		return identity.OrganizationID{}, ClusterID{}, invalid
	}
	o, err := identity.ParseOrganizationID(org.String())
	if err != nil {
		return identity.OrganizationID{}, ClusterID{}, invalid
	}
	return o, identity.FromUUID[tag](id), nil
}

// InvalidToken is the error of a token that is unknown, used or expired
// (one answer for all three: a caller learns nothing by probing).
func InvalidToken() error {
	return errx.Unauthorized("El token de conexión no es válido o ya venció. Genera uno nuevo desde la consola.").WithCode(CodeInvalidToken)
}

// Rejected is the error of an agent whose certificate is no longer accepted.
func Rejected() error {
	return errx.Forbidden("El certificado de este clúster ya no es válido: vuelve a conectarlo desde la consola.").WithCode(CodeRevoked)
}

// Superseded is the error of a tunnel another one of the same cluster took
// over (a reconnect, a second agent, a new enrolment); its certificate may
// still be fine.
func Superseded() error {
	return errx.Conflict("another connection of this cluster took over").WithCode(CodeSuperseded)
}

// Issued is a certificate the fleet CA signed for an agent.
type Issued struct {
	PEM       []byte
	CAPEM     []byte
	Serial    string
	ExpiresAt time.Time
}

// Enrollment is the answer to an agent that enrolled.
type Enrollment struct {
	Organization identity.OrganizationID
	Cluster      ClusterID
	Certificate  Issued
}

// Recover names the cluster whose certificate an agent lost the answer
// for: by its enrolment token (spent or not: it only names the cluster) or
// by its ids.
type Recover struct {
	Token        string
	Organization string
	Cluster      string
}

// Validate checks the input and resolves the cluster it names.
func (r Recover) Validate() error {
	_, _, err := r.Target()
	return err
}

// Target is the cluster the input names.
func (r Recover) Target() (identity.OrganizationID, ClusterID, error) {
	if r.Token != "" {
		return ParseToken(r.Token)
	}
	org, err := identity.ParseOrganizationID(r.Organization)
	if err != nil {
		return identity.OrganizationID{}, ClusterID{}, errx.Validation("organization_id is not valid")
	}
	id, err := ParseClusterID(r.Cluster)
	if err != nil {
		return identity.OrganizationID{}, ClusterID{}, errx.Validation("cluster_id is not valid")
	}
	return org, id, nil
}

// NothingToRecover is the answer when no current certificate of the
// cluster is for the key (one answer for every case: probing teaches nothing).
func NothingToRecover() error {
	return errx.NotFound("there is no current certificate of this cluster for this key").WithCode(CodeNothingToRecover)
}

// Trust is the public side of the fleet CA.
type Trust struct {
	CAPEM []byte
	// Pin is "sha256:<hex>" of the CA's SubjectPublicKeyInfo.
	Pin string
}

// Serial is how certificate serials are written down (lowercase hex).
func Serial(n *big.Int) string { return n.Text(16) }
