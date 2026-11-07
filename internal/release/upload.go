package release

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// An upload ticket lets whoever holds it publish one folder to one app: a
// coding agent asks for it (deploy_folder) and runs the command it gets,
// which streams the folder to PUT /uploads/{token}. The agent's own token
// never leaves its MCP client, and the ticket is good for one upload, for a
// few minutes, for the app, environment, user and configuration it names.

// UploadTicketTTL is how long a ticket can be used.
const UploadTicketTTL = 10 * time.Minute

// UploadTicketPrefix starts every ticket token (secret scanners know it).
const UploadTicketPrefix = "odu_"

// Error codes of a ticket that cannot be used.
const (
	CodeUploadTicketUsed    = "UPLOAD_TICKET_USED"    // it was used already (deployment_id in the details)
	CodeUploadTicketExpired = "UPLOAD_TICKET_EXPIRED" // its 10 minutes ran out
)

type uploadTicketTag struct{}

func (uploadTicketTag) IDName() string { return "upload_ticket_id" }

// UploadTicketID identifies an upload ticket (never its secret token).
type UploadTicketID = identity.ID[uploadTicketTag]

func NewUploadTicketID() UploadTicketID { return identity.NewID[uploadTicketTag]() }
func ParseUploadTicketID(raw string) (UploadTicketID, error) {
	return identity.ParseID[uploadTicketTag](raw)
}

// MustParseUploadTicketID is ParseUploadTicketID for tests and static init.
func MustParseUploadTicketID(raw string) UploadTicketID {
	return identity.MustParseID[uploadTicketTag](raw)
}

// IssueUpload asks for a ticket to publish a folder to an environment with a
// configuration. The client the ticket names comes from the request's
// provenance (event.Meta), never from the input. Message is what changed, in
// the words of whoever asked; the deployment the upload starts carries it.
// Rebuild makes that deployment build every service again (no reuse of an
// earlier image of the same code: a newer base image, a fix outside it).
type IssueUpload struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Rebuild bool   `json:"rebuild"`
	Config
	RequestedBy identity.UserID `json:"-"`
}

func (i IssueUpload) Normalize() IssueUpload {
	i.Name = strings.TrimSpace(i.Name)
	i.Message = CleanMessage(i.Message)
	i.Config = i.Config.Normalize()
	return i
}

func (i IssueUpload) Validate() error {
	if len(i.Name) > MaxSourceName || !utf8.ValidString(i.Name) {
		return errx.InvalidField("name", "name must be up to 200 bytes of text")
	}
	if err := validMessage(i.Message); err != nil {
		return err
	}
	return i.Config.Validate()
}

// UploadTicket is an issued ticket. Token is set only in what IssueUpload
// returns: the database keeps its SHA-256 (TokenHash), never the token.
type UploadTicket struct {
	ID           UploadTicketID          `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	Token        string                  `json:"-" db:"-"`
	TokenHash    []byte                  `json:"-" db:"token_hash"`
	Name         string                  `json:"name" db:"name"`
	Message      string                  `json:"message" db:"message"`
	// Rebuild is IssueUpload's: the deployment the upload starts reuses no image.
	Rebuild bool   `json:"rebuild" db:"rebuild"`
	Config  Config `json:"config" db:"-"`
	// Client is the provenance of the request that issued it ("mcp:<client>"):
	// the deployment the upload starts is attributed to it.
	Client      string          `json:"client" db:"client"`
	RequestedBy identity.UserID `json:"requested_by" db:"requested_by"`
	// Permissions are those of the token that asked, at that time (a
	// snapshot): the redeeming request carries no token, and IT's rules judge
	// the deployment as the person who asked, with these.
	Permissions []string   `json:"-" db:"-"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at" db:"expires_at"`
	UsedAt      *time.Time `json:"used_at" db:"used_at"`
	// Deployment is what the upload started (nil before, or when it was
	// refused).
	Deployment *DeploymentID `json:"deployment_id" db:"deployment_id"`
}

// Expired reports whether the ticket can no longer be used at now.
func (t UploadTicket) Expired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

// NewUploadToken returns a fresh ticket token and its hash.
func NewUploadToken() (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("release: no randomness for an upload ticket: " + err.Error())
	}
	token = UploadTicketPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashUploadToken(token)
}

// HashUploadToken is the SHA-256 tickets are looked up by. The token is 256
// random bits, so a plain hash is enough (nothing to brute-force).
func HashUploadToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ValidUploadToken reports whether s looks like a ticket token (a cheap
// check before touching the database).
func ValidUploadToken(s string) bool {
	rest, ok := strings.CutPrefix(s, UploadTicketPrefix)
	if !ok || len(rest) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(rest)
	return err == nil
}
