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
	"unicode/utf8"

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

// Normalize fills the defaults (one replica, health at "/").
func (c Config) Normalize() Config {
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

// Deploy asks to run an image in an environment: it creates a release and a
// deployment of it, superseding the deployment in progress there.
type Deploy struct {
	Image string `json:"image"`
	Config
	RequestedBy identity.UserID `json:"-"` // from the token
}

func (d Deploy) Normalize() Deploy {
	d.Image = strings.TrimSpace(d.Image)
	d.Config = d.Config.Normalize()
	return d
}

func (d Deploy) Validate() error {
	if !ValidImage(d.Image) {
		return errx.InvalidField("image", ImageMessage)
	}
	return d.Config.Validate()
}

// Rollback asks to serve again the release of an earlier ready deployment.
type Rollback struct {
	RequestedBy identity.UserID `json:"-"`
}

func (Rollback) Validate() error { return nil }
