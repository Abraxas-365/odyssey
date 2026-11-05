package project

import (
	"regexp"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// Status is the lifecycle of a project. An archived project keeps its
// history (builds, releases, audit) but takes no changes and no deploys.
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusArchived }

// StatusMessage is the 400 text for a value outside the set.
const StatusMessage = "status must be active or archived"

// Tier is how much an app matters to the company; IT's rules (internal/policy)
// ask for it ("a sandbox app may be public, a production one needs approval").
// Anyone with projects:write creates an app, only IT (projects:tier) moves it.
type Tier string

const (
	TierSandbox    Tier = "sandbox"
	TierInternal   Tier = "internal"
	TierProduction Tier = "production"

	// DefaultTier is every new app's tier.
	DefaultTier = TierInternal
)

func (t Tier) Valid() bool { return t == TierSandbox || t == TierInternal || t == TierProduction }

// TierMessage is the 400 text for a value outside the set.
const TierMessage = "tier must be sandbox, internal or production"

// SetTier is IT's change of an app's tier.
type SetTier struct {
	Tier      Tier            `json:"tier"`
	UpdatedBy identity.UserID `json:"-"` // taken from the token, never the body
}

func (s SetTier) Validate() error {
	if !s.Tier.Valid() {
		return errx.InvalidField("tier", TierMessage)
	}
	if s.UpdatedBy.IsZero() {
		return errx.Validation("editor is required")
	}
	return nil
}

// CodeArchived is the error code of a change refused because the project is archived.
const CodeArchived = "PROJECT_ARCHIVED"

// Slug rules: a DNS label, because the slug becomes part of the app's
// hostname (<slug>.<org domain>) and of Kubernetes object names.
const (
	MinSlug     = 3
	MaxSlug     = 40
	SlugMessage = "slug must be 3-40 characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen"
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// ValidSlug reports whether s is an acceptable slug.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) && !strings.Contains(s, "--") }

// Project is one app (or group of services) a team ships.
type Project struct {
	ID           ID                      `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Name         string                  `json:"name" db:"name"`
	Slug         string                  `json:"slug" db:"slug"`
	Description  string                  `json:"description" db:"description"`
	Status       Status                  `json:"status" db:"status"`
	Tier         Tier                    `json:"tier" db:"tier"`
	CreatedBy    identity.UserID         `json:"created_by" db:"created_by"`
	UpdatedBy    identity.UserID         `json:"updated_by" db:"updated_by"`
	CreatedAt    time.Time               `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time               `json:"updated_at" db:"updated_at"`
	ArchivedAt   *time.Time              `json:"archived_at" db:"archived_at"`
}

// Archived reports whether the project takes no more changes.
func (p Project) Archived() bool { return p.Status == StatusArchived }

// Create registers a project. The slug is fixed at creation (it is in
// hostnames and object names); when absent it is derived from the name.
type Create struct {
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	CreatedBy   identity.UserID `json:"-"` // taken from the token, never the body
}

// Normalize fills derived fields (the slug from the name). The service calls
// it before Validate.
func (c Create) Normalize() Create {
	c.Slug = strings.TrimSpace(c.Slug)
	if c.Slug == "" {
		c.Slug = Slugify(c.Name)
	}
	return c
}

func (c Create) Validate() error {
	if err := text.Required("name", c.Name, text.MaxName); err != nil {
		return err
	}
	if !ValidSlug(c.Slug) {
		return errx.InvalidField("slug", SlugMessage)
	}
	if err := text.Optional("description", c.Description, text.MaxDescription); err != nil {
		return err
	}
	if c.CreatedBy.IsZero() {
		return errx.Validation("creator is required")
	}
	return nil
}

// Update changes mutable fields; absent means unchanged.
type Update struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	UpdatedBy   identity.UserID `json:"-"` // taken from the token, never the body
}

func (u Update) Validate() error {
	if u.Name != nil {
		if err := text.Required("name", *u.Name, text.MaxName); err != nil {
			return err
		}
	}
	if u.Description != nil {
		if err := text.Optional("description", *u.Description, text.MaxDescription); err != nil {
			return err
		}
	}
	if u.UpdatedBy.IsZero() {
		return errx.Validation("editor is required")
	}
	return nil
}

// Changes drops the fields equal (after trimming) to current; an empty
// result is a no-op PATCH.
func (u Update) Changes(current Project) Update {
	if u.Name != nil && text.Clean(*u.Name) == current.Name {
		u.Name = nil
	}
	if u.Description != nil && text.Clean(*u.Description) == current.Description {
		u.Description = nil
	}
	return u
}

// Empty reports a patch that changes nothing.
func (u Update) Empty() bool { return u.Name == nil && u.Description == nil }

// Archive retires a project. It is not a delete: history stays.
type Archive struct {
	ArchivedBy identity.UserID
}

func (a Archive) Validate() error {
	if a.ArchivedBy.IsZero() {
		return errx.Validation("archiving user is required")
	}
	return nil
}

// Filter narrows List. Pagination.Search matches name and slug. Repeated
// statuses mean OR; none means active only.
type Filter struct {
	Status []Status
}

func (f Filter) Validate() error {
	for _, s := range f.Status {
		if !s.Valid() {
			return errx.InvalidField("status", StatusMessage)
		}
	}
	return nil
}

// ArchivedError is the 422 for a change to an archived project.
func ArchivedError() error {
	return errx.Business("the project is archived and takes no changes").WithCode(CodeArchived)
}

// Slugify derives a slug from a display name: ASCII-folded, lowercase,
// non-alphanumerics collapsed to single hyphens, cut to MaxSlug. The result
// may still be invalid (too short, starting with a digit); Validate reports it.
func Slugify(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if f, ok := fold[r]; ok {
			r = f
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		case b.Len() > 0 && !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > MaxSlug {
		s = strings.TrimRight(s[:MaxSlug], "-")
	}
	return s
}

// fold maps the accented letters of Spanish and Portuguese names to ASCII.
var fold = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ç': 'c',
}
