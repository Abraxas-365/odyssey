// Package catalogtest has an in-memory catalog.Repository for unit tests of
// the service and of the domains that ask the catalog.
package catalogtest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Repo is a catalog.Repository in memory, scoped by organization.
type Repo struct {
	mu   sync.Mutex
	rows map[string]catalog.Offering // organization/recipe
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
