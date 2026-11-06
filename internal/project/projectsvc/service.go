// Package projectsvc implements project.Commands and project.Queries.
package projectsvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
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
	// runtime says whether the app's clusters can run several services
	// (wired after the fleet is built; nil allows only Simple services).
	runtime project.Runtime
}

func New(repo project.Repository, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, tx: tx, events: events}
}

// UseRuntime wires the fleet's answer to "can these clusters run several
// services" (the fleet module is built after this one).
func (s *Service) UseRuntime(r project.Runtime) { s.runtime = r }

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
	// Every project is born with its production environment: there is always
	// somewhere to deploy, and its ID is stable from the first minute.
	production := project.Environment{ID: project.NewEnvironmentID(), Organization: organization, Project: id,
		Slug: project.ProductionSlug, Kind: project.KindProduction}
	name := text.Clean(input.Name)
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Create(ctx, organization, id, input); err != nil {
			return err
		}
		if err := s.repo.CreateEnvironment(ctx, production); err != nil {
			return err
		}
		if err := s.repo.ReplaceServices(ctx, organization, id, project.DefaultServices(organization, id)); err != nil {
			return err
		}
		return s.events.Publish(ctx,
			project.NewCreated(organization, input.CreatedBy, id, name, input.Slug),
			project.NewEnvironmentCreated(organization, input.CreatedBy, production, name))
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

// SetTier moves the app to another tier (IT's call; the projects:tier
// permission gates the route). The tier it already has is a no-op.
func (s *Service) SetTier(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.SetTier) error {
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
		if current.Tier == input.Tier {
			return nil
		}
		if err := s.repo.SetTier(ctx, organization, id, input); err != nil {
			return err
		}
		return s.events.Publish(ctx, project.NewTierChanged(organization, input.UpdatedBy, current, input.Tier))
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

// SetServices replaces the app's services. The project row is locked, so
// two replacements serialize and an archived app refuses; the capability
// check runs on the environments as they are at that moment.
func (s *Service) SetServices(ctx context.Context, organization identity.OrganizationID, id project.ID, input project.SetServices) ([]project.Service, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return nil, err
	}
	next := input.Services(organization, id)
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		current, err := s.repo.FindForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if current.Archived() {
			return project.ArchivedError()
		}
		before, err := s.repo.ListServices(ctx, organization, id)
		if err != nil {
			return err
		}
		if project.SameServices(before, next) {
			return nil
		}
		if !project.Simple(next) {
			if err := s.canRun(ctx, organization, id); err != nil {
				return err
			}
		}
		if err := s.repo.ReplaceServices(ctx, organization, id, next); err != nil {
			return err
		}
		return s.events.Publish(ctx, project.NewServicesChanged(organization, input.UpdatedBy, current, before, next))
	})
	if err != nil {
		return nil, err
	}
	return s.repo.ListServices(ctx, organization, id)
}

// canRun refuses a topology the app's clusters cannot run.
func (s *Service) canRun(ctx context.Context, organization identity.OrganizationID, id project.ID) error {
	unavailable := errx.Business(project.ServicesUnavailableMessage).WithCode(project.CodeServicesUnavailable)
	if s.runtime == nil {
		return unavailable
	}
	envs, err := s.repo.ListEnvironments(ctx, organization, id, project.EnvironmentFilter{})
	if err != nil {
		return err
	}
	ids := make([]project.EnvironmentID, len(envs))
	for i, e := range envs {
		ids[i] = e.ID
	}
	ok, err := s.runtime.CanRunServices(ctx, organization, ids)
	if err != nil {
		return err
	}
	if !ok {
		return unavailable
	}
	return nil
}

// Services answers NotFound for an unknown project.
func (s *Service) Services(ctx context.Context, organization identity.OrganizationID, id project.ID) ([]project.Service, error) {
	if _, err := s.repo.Find(ctx, organization, id); err != nil {
		return nil, err
	}
	return s.repo.ListServices(ctx, organization, id)
}

// FindBySlug resolves an app by its URL slug.
func (s *Service) FindBySlug(ctx context.Context, organization identity.OrganizationID, slug string) (project.Project, error) {
	return s.repo.FindBySlug(ctx, organization, slug)
}

// Hold joins the caller's transaction (it is a read for another domain's
// write, so it opens none of its own).
func (s *Service) Hold(ctx context.Context, organization identity.OrganizationID, id project.ID) (project.Project, error) {
	return s.repo.FindForShare(ctx, organization, id)
}

func (s *Service) FindEnvironment(ctx context.Context, organization identity.OrganizationID, id project.ID, environment project.EnvironmentID) (project.Environment, error) {
	return s.repo.FindEnvironment(ctx, organization, id, environment)
}

func (s *Service) FindEnvironmentBySlug(ctx context.Context, organization identity.OrganizationID, id project.ID, slug string) (project.Environment, error) {
	return s.repo.FindEnvironmentBySlug(ctx, organization, id, slug)
}

// ListEnvironments answers NotFound for an unknown project (an empty list
// would hide a wrong ID).
func (s *Service) ListEnvironments(ctx context.Context, organization identity.OrganizationID, id project.ID, filter project.EnvironmentFilter) ([]project.Environment, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.repo.Find(ctx, organization, id); err != nil {
		return nil, err
	}
	return s.repo.ListEnvironments(ctx, organization, id, filter)
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

// FindMany and EnvironmentsOf are the batch reads of the inventory and the
// attention list: one query for any number of apps, never more than
// MaxBatch ids (the caller pages).
func (s *Service) FindMany(ctx context.Context, organization identity.OrganizationID, ids []project.ID) ([]project.Project, error) {
	if len(ids) == 0 {
		return []project.Project{}, nil
	}
	if len(ids) > project.MaxBatch {
		return nil, errx.Validation("too many projects in one read")
	}
	return s.repo.FindMany(ctx, organization, ids)
}

func (s *Service) EnvironmentsOf(ctx context.Context, organization identity.OrganizationID, ids []project.ID) ([]project.Environment, error) {
	if len(ids) == 0 {
		return []project.Environment{}, nil
	}
	if len(ids) > project.MaxBatch {
		return nil, errx.Validation("too many projects in one read")
	}
	return s.repo.ListEnvironmentsOf(ctx, organization, ids)
}
