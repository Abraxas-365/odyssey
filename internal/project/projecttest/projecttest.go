// Package projecttest has an in-memory project.Repository for unit tests of
// the service and the HTTP adapter.
package projecttest

import (
	"context"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// Repo is a project.Repository in memory, scoped by organization.
type Repo struct {
	mu   sync.Mutex
	rows map[project.ID]project.Project
}

func NewRepo() *Repo { return &Repo{rows: map[project.ID]project.Project{}} }

var _ project.Repository = (*Repo)(nil)

func (m *Repo) Create(_ context.Context, org identity.OrganizationID, id project.ID, in project.Create) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.rows {
		if p.Organization == org && p.Slug == in.Slug {
			return errx.Conflict("a project with this slug already exists")
		}
	}
	now := time.Now()
	m.rows[id] = project.Project{ID: id, Organization: org, Name: text.Clean(in.Name), Slug: in.Slug,
		Description: text.Clean(in.Description), Status: project.StatusActive, CreatedBy: in.CreatedBy,
		UpdatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now}
	return nil
}

func (m *Repo) Find(_ context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	if !ok || p.Organization != org {
		return project.Project{}, errx.NotFound("project not found")
	}
	return p, nil
}

func (m *Repo) FindForUpdate(ctx context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	return m.Find(ctx, org, id)
}

func (m *Repo) List(_ context.Context, org identity.OrganizationID, f project.Filter, p query.Pagination) (query.Paginated[project.Project], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []project.Project{}
	for _, r := range m.rows {
		for _, s := range f.Status {
			if r.Organization == org && r.Status == s {
				items = append(items, r)
			}
		}
	}
	return query.NewPaginated(items, len(items), p), nil
}

func (m *Repo) Update(ctx context.Context, org identity.OrganizationID, id project.ID, in project.Update) error {
	p, err := m.Find(ctx, org, id)
	if err != nil {
		return err
	}
	if in.Name != nil {
		p.Name = text.Clean(*in.Name)
	}
	if in.Description != nil {
		p.Description = text.Clean(*in.Description)
	}
	p.UpdatedBy, p.UpdatedAt = in.UpdatedBy, time.Now()
	m.mu.Lock()
	m.rows[id] = p
	m.mu.Unlock()
	return nil
}

func (m *Repo) Archive(ctx context.Context, org identity.OrganizationID, id project.ID, in project.Archive) error {
	p, err := m.Find(ctx, org, id)
	if err != nil {
		return err
	}
	now := time.Now()
	p.Status, p.ArchivedAt, p.UpdatedBy = project.StatusArchived, &now, in.ArchivedBy
	m.mu.Lock()
	m.rows[id] = p
	m.mu.Unlock()
	return nil
}
