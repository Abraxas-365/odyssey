package project

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// EnvironmentKind is what an environment is for. Policies (approvals, scan
// gates, who may deploy) key on it, never on the name.
type EnvironmentKind string

const (
	KindProduction EnvironmentKind = "production"
	KindStaging    EnvironmentKind = "staging"
	KindPreview    EnvironmentKind = "preview"
)

func (k EnvironmentKind) Valid() bool {
	return k == KindProduction || k == KindStaging || k == KindPreview
}

// EnvironmentKindMessage is the 400 text for a value outside the set.
const EnvironmentKindMessage = "kind must be production, staging or preview"

// ProductionSlug is the environment every project is born with.
const ProductionSlug = "production"

// Environment is one place a project runs (production, staging, a PR
// preview). The project owns its identity; what runs in it (the current
// release, deployments) belongs to the release domain.
type Environment struct {
	ID           EnvironmentID           `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      ID                      `json:"project_id" db:"project_id"`
	// Slug is a DNS label: it becomes part of the app's hostname and of
	// Kubernetes object names (rules of the project slug).
	Slug      string          `json:"slug" db:"slug"`
	Kind      EnvironmentKind `json:"kind" db:"kind"`
	CreatedAt time.Time       `json:"created_at" db:"created_at"`
}

// EnvironmentFilter narrows ListEnvironments; zero fields match everything.
type EnvironmentFilter struct {
	Kind []EnvironmentKind
}

func (f EnvironmentFilter) Validate() error {
	for _, k := range f.Kind {
		if !k.Valid() {
			return errx.InvalidField("kind", EnvironmentKindMessage)
		}
	}
	return nil
}
