// Package projectsvc implements project.Commands and project.Queries.
package projectsvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type Service struct {
	repo   project.Repository
	tx     txn.Runner
	events event.Publisher
}

func New(repo project.Repository, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, tx: tx, events: events}
}

var (
	_ project.Commands = (*Service)(nil)
	_ project.Queries  = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, organization identity.OrganizationID, input project.Create) (project.ID, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return project.ID{}, err
	}
	id := project.NewID()
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Create(ctx, organization, id, input); err != nil {
			return err
		}
		return s.events.Publish(ctx, project.NewCreated(organization, input.CreatedBy, id, text.Clean(input.Name), input.Slug))
	})
	if err != nil {
		return project.ID{}, err
	}
	return id, nil
}

// Update changes name/description. A patch equal to the current values is a
// no-op (no write, no event); an archived project refuses changes.
func (s *Service) Update(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.Update) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		current, err := s.repo.FindForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if current.Archived() {
			return project.ArchivedError()
		}
		changes := input.Changes(current)
		if changes.Empty() {
			return nil
		}
		if err := s.repo.Update(ctx, organization, id, changes); err != nil {
			return err
		}
		return s.events.Publish(ctx, project.NewUpdated(organization, input.UpdatedBy, current, changes))
	})
}

// Archive retires the project. Archiving an archived project is a no-op.
func (s *Service) Archive(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.Archive) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		current, err := s.repo.FindForUpdate(ctx, organization, id)
		if err != nil || current.Archived() {
			return err
		}
		if err := s.repo.Archive(ctx, organization, id, input); err != nil {
			return err
		}
		return s.events.Publish(ctx, project.NewArchived(organization, input.ArchivedBy, current))
	})
}

func (s *Service) Find(ctx context.Context, organization identity.OrganizationID, id project.ID) (project.Project, error) {
	return s.repo.Find(ctx, organization, id)
}

func (s *Service) List(ctx context.Context, organization identity.OrganizationID, filter project.Filter, page query.Pagination) (query.Paginated[project.Project], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[project.Project]{}, err
	}
	if len(filter.Status) == 0 {
		filter.Status = []project.Status{project.StatusActive}
	}
	return s.repo.List(ctx, organization, filter, page.Normalize())
}
