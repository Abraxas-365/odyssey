// Package catalogtest has an in-memory catalog.Repository for unit tests of
// the service and of the domains that ask the catalog.
package catalogtest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Repo is a catalog.Repository in memory, scoped by organization.
type Repo struct {
	mu       sync.Mutex
	rows     map[string]catalog.Offering // organization/recipe
	versions []catalog.OrgRecipe
}

func NewRepo() *Repo { return &Repo{rows: map[string]catalog.Offering{}} }

var _ catalog.Repository = (*Repo)(nil)

func key(org identity.OrganizationID, recipe string) string { return org.String() + "/" + recipe }

func (m *Repo) List(_ context.Context, org identity.OrganizationID) ([]catalog.Offering, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []catalog.Offering
	for _, o := range m.rows {
		if o.Organization == org {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b catalog.Offering) int { return strings.Compare(a.Recipe, b.Recipe) })
	return out, nil
}

func (m *Repo) Find(_ context.Context, org identity.OrganizationID, recipe string) (catalog.Offering, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.rows[key(org, recipe)]
	if !ok {
		return catalog.Offering{}, catalog.NotFound(recipe)
	}
	return o, nil
}

func (m *Repo) LockCatalog(context.Context, identity.OrganizationID) error { return nil }

func (m *Repo) Update(_ context.Context, o catalog.Offering) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[key(o.Organization, o.Recipe)]
	if !ok {
		return catalog.NotFound(o.Recipe)
	}
	o.Revision, o.CreatedAt = cur.Revision+1, cur.CreatedAt
	m.rows[key(o.Organization, o.Recipe)] = o
	return nil
}

func (m *Repo) SeedFactory(_ context.Context, offerings []catalog.Offering) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	added := 0
	for _, o := range offerings {
		if _, ok := m.rows[key(o.Organization, o.Recipe)]; !ok {
			m.rows[key(o.Organization, o.Recipe)] = o
			added++
		}
	}
	return added, nil
}

// AddOffering inserts an offering; one that exists is a conflict.
func (m *Repo) AddOffering(_ context.Context, o catalog.Offering) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[key(o.Organization, o.Recipe)]; ok {
		return errx.Conflict("offering exists")
	}
	o.Revision, o.UpdatedAt = 1, o.CreatedAt
	m.rows[key(o.Organization, o.Recipe)] = o
	return nil
}

// Publish keeps a version; one that exists is a conflict, as in Postgres.
func (m *Repo) Publish(_ context.Context, o catalog.OrgRecipe) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.versions {
		if v.Organization == o.Organization && v.Recipe == o.Recipe && v.Version == o.Version {
			return errx.Conflict("version exists")
		}
	}
	m.versions = append(m.versions, o)
	return nil
}

func (m *Repo) Published(_ context.Context, org identity.OrganizationID) ([]catalog.OrgRecipe, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []catalog.OrgRecipe{}
	for _, v := range m.versions {
		if v.Organization == org {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b catalog.OrgRecipe) int {
		if c := strings.Compare(a.Recipe, b.Recipe); c != 0 {
			return c
		}
		return a.Version - b.Version
	})
	return out, nil
}
