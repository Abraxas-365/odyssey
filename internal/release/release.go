// Package release is what runs where: a Release is an immutable image plus
// its configuration; a Deployment is one attempt to make a release the one
// serving an environment. The deploy workflow (releasesvc) drives a
// deployment through the Cluster port; only its promote step changes what an
// environment serves.
package release

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// Release is an image pinned by digest plus the configuration it runs with.
// It never changes: a rollback deploys an older release again.
type Release struct {
	ID           ID                      `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Image        string                  `json:"image" db:"image"`
	Config       Config                  `json:"config" db:"-"`
	CreatedBy    identity.UserID         `json:"created_by" db:"created_by"`
	CreatedAt    time.Time               `json:"created_at" db:"created_at"`
}

// Config is how a release runs. It holds no secrets: those are references
// resolved by the cluster (phase 2), never values stored here.
type Config struct {
	Port       int               `json:"port"`
	HealthPath string            `json:"health_path"`
	Replicas   int               `json:"replicas"`
	Env        map[string]string `json:"env"`
}

// Limits of a release's configuration.
const (
	MaxImage    = 512
	MaxReplicas = 10
	MaxEnv      = 100
	MaxEnvValue = 4096
	MaxHealth   = 200
	// DefaultPort is the port an app listens on when the request names none:
	// the agent also sets $PORT to it, which builds (Railpack) honor. One
	// default for every client (console, API, MCP).
	DefaultPort = 8080
	// ReservedEnvPrefix is set by the platform (ODYSSEY_URL, ...).
	ReservedEnvPrefix = "ODYSSEY_"
)

// ImageMessage is the 400 text for an image that is not pinned by digest.
const ImageMessage = "image must be a repository pinned by digest: registry/repository@sha256:<64 hex>"

var (
	imagePattern  = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// ValidImage reports whether s is a repository pinned by digest. Tags move;
// a release must name exactly one image forever.
func ValidImage(s string) bool { return len(s) <= MaxImage && imagePattern.MatchString(s) }

// Normalize fills the defaults (port 8080, one replica, health at "/").
func (c Config) Normalize() Config {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	c.HealthPath = strings.TrimSpace(c.HealthPath)
	if c.HealthPath == "" {
		c.HealthPath = "/"
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	if c.Env == nil {
		c.Env = map[string]string{}
	}
	return c
}

func (c Config) Validate() error {
	switch {
	case c.Port < 1 || c.Port > 65535:
		return errx.InvalidField("port", "port must be between 1 and 65535")
	case !strings.HasPrefix(c.HealthPath, "/") || utf8.RuneCountInString(c.HealthPath) > MaxHealth ||
		strings.ContainsAny(c.HealthPath, " \t\r\n?#"):
		return errx.InvalidField("health_path", "health_path must be a path starting with / (no spaces, query or fragment), up to 200 characters")
	case c.Replicas < 1 || c.Replicas > MaxReplicas:
		return errx.InvalidField("replicas", "replicas must be between 1 and 10")
	case len(c.Env) > MaxEnv:
		return errx.InvalidField("env", "env holds at most 100 variables")
	}
	for k, v := range c.Env {
		switch {
		case !envKeyPattern.MatchString(k):
			return errx.InvalidField("env", "env names are letters, digits and _, not starting with a digit (up to 128): "+k)
		case strings.HasPrefix(strings.ToUpper(k), ReservedEnvPrefix):
			return errx.InvalidField("env", "env names starting with ODYSSEY_ are set by the platform: "+k)
		case len(v) > MaxEnvValue || strings.ContainsRune(v, 0):
			return errx.InvalidField("env", "env values are up to 4096 bytes without NUL: "+k)
		}
	}
	return nil
}

// MaxMessage bounds a version's message, in characters.
const MaxMessage = 200

// CleanMessage is what a version's message is stored as: one line, without
// control characters, its runs of spaces collapsed ("" stays "").
func CleanMessage(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}

// validMessage checks a cleaned message.
func validMessage(s string) error {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxMessage {
		return errx.InvalidField("message", "message must be one line of up to 200 characters")
	}
	return nil
}

// Deploy asks to run an image in an environment: it creates a release and a
// deployment of it, superseding the deployment in progress there. Message is
// what changed, in the words of whoever asked (optional).
type Deploy struct {
	Image   string `json:"image"`
	Message string `json:"message"`
	Config
	RequestedBy identity.UserID `json:"-"` // from the token
}

func (d Deploy) Normalize() Deploy {
	d.Image = strings.TrimSpace(d.Image)
	d.Message = CleanMessage(d.Message)
	d.Config = d.Config.Normalize()
	return d
}

func (d Deploy) Validate() error {
	if !ValidImage(d.Image) {
		return errx.InvalidField("image", ImageMessage)
	}
	if err := validMessage(d.Message); err != nil {
		return err
	}
	return d.Config.Validate()
}

// DeploySource asks to publish an uploaded folder: the deployment first
// builds it (in the environment's own cluster) and then runs the image like
// Deploy does. Name names the source ("" takes the uploaded file's); Message
// is what changed, in the words of whoever asked (optional).
type DeploySource struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Config
	RequestedBy identity.UserID `json:"-"` // from the token
}

func (d DeploySource) Normalize() DeploySource {
	d.Name = strings.TrimSpace(d.Name)
	d.Message = CleanMessage(d.Message)
	d.Config = d.Config.Normalize()
	return d
}

func (d DeploySource) Validate() error {
	if len(d.Name) > MaxSourceName || !utf8.ValidString(d.Name) {
		return errx.InvalidField("name", "name must be up to 200 bytes of text")
	}
	if err := validMessage(d.Message); err != nil {
		return err
	}
	return d.Config.Validate()
}

// MaxSourceName bounds a source's display name.
const MaxSourceName = 200

// DeployUploaded asks to publish a source already uploaded to the build
// domain (a commit imported from git): DeploySource without the upload. The
// deployment shows the source's name and commit.
type DeployUploaded struct {
	Source build.SourceID `json:"source_id"`
	Config
	RequestedBy identity.UserID `json:"-"`
	// Via names the requester when it is not the caller's own client (the
	// source domain's pushes: ViaGitHub); "" derives it from the request.
	Via string `json:"-"`
}

func (d DeployUploaded) Normalize() DeployUploaded {
	d.Config = d.Config.Normalize()
	return d
}

func (d DeployUploaded) Validate() error {
	if d.Source.IsZero() {
		return errx.InvalidField("source_id", "source_id is required")
	}
	return d.Config.Validate()
}

// Rollback asks to serve again the release of an earlier ready deployment.
type Rollback struct {
	RequestedBy identity.UserID `json:"-"`
}

func (Rollback) Validate() error { return nil }
