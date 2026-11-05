// Package resourcesvc implements resource.Commands and resource.Queries.
//
// Locks are taken in one order so changes never deadlock: the environment's
// resource count (Create), then the environment's settings (Create's
// connection, Connect, Disconnect and Delete, through resource.Usage), then
// the resource row. Delete and Connect serialize on the settings lock, which
// is what makes "not in use" stay true while the resource is marked deleting.
package resourcesvc

import (
	"context"
	"slices"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type Service struct {
	repo     resource.Repository
	cluster  resource.Cluster
	usage    resource.Usage
	projects project.Queries
	tx       txn.Runner
	events   event.Publisher
}

func New(repo resource.Repository, cluster resource.Cluster, usage resource.Usage, projects project.Queries,
	tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, cluster: cluster, usage: usage, projects: projects, tx: tx, events: events}
}

var (
	_ resource.Commands = (*Service)(nil)
	_ resource.Queries  = (*Service)(nil)
)

// place resolves the project and environment of a request.
func (s *Service) place(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Project, project.Environment, error) {
	p, err := s.projects.Find(ctx, org, id)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	e, err := s.projects.FindEnvironment(ctx, org, id, env)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, e, nil
}

func names(p project.Project, e project.Environment) resource.Names {
	return resource.Names{Project: p.Name, Environment: e.Slug}
}

// view puts a stored resource together with what the cluster reports and
// whether the app uses it.
func (s *Service) view(ctx context.Context, r resource.Resource, connected []resource.Connection) (resource.View, error) {
	o, err := s.cluster.Observed(ctx, r.Organization, r.Environment, r.Name)
	if err != nil {
		return resource.View{}, err
	}
	var conn *resource.Connection
	for i := range connected {
		if connected[i].Name == r.Name {
			conn = &connected[i]
		}
	}
	return resource.NewView(r, o, conn), nil
}

func (s *Service) viewOf(ctx context.Context, p project.Project, r resource.Resource) (resource.View, error) {
	connected, err := s.usage.Connections(ctx, r.Organization, p.ID, r.Environment)
	if err != nil {
		return resource.View{}, err
	}
	return s.view(ctx, r, connected)
}

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input resource.Create) (resource.Result, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return resource.Result{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	if p.Archived() {
		return resource.Result{}, project.ArchivedError()
	}
	r := resource.Resource{ID: resource.NewID(), Organization: org, Project: id, Environment: e.ID, Name: input.Name, Kind: input.Kind,
		StorageGi: input.StorageGi, Generation: 1, Status: resource.StatusActive, CreatedBy: input.RequestedBy}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		n, err := s.repo.Count(ctx, org, e.ID)
		if err != nil {
			return err
		}
		if n >= resource.MaxPerEnvironment {
			return resource.ErrLimit()
		}
		if err := s.repo.Create(ctx, r); err != nil {
			return err
		}
		// A cluster that cannot prepare the kind refuses here and the row
		// above is rolled back: nothing waits for a resource that never comes.
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		if err := s.events.Publish(ctx, resource.NewCreated(org, input.RequestedBy, r, names(p, e))); err != nil {
			return err
		}
		if input.Connect == nil {
			return nil
		}
		applied, err = s.usage.Connect(ctx, org, id, e.ID, resource.Connection{Name: r.Name, Prefix: input.Connect.Prefix}, input.RequestedBy)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	stored, err := s.repo.Find(ctx, org, e.ID, r.Name)
	if err != nil {
		return resource.Result{}, err
	}
	v, err := s.viewOf(ctx, p, stored)
	if err != nil {
		return resource.Result{}, err
	}
	return resource.Result{View: v, Revision: applied.Revision, Deployment: applied.Deployment}, nil
}

func (s *Service) Resize(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, input resource.Resize) (resource.View, error) {
	if err := input.Validate(); err != nil {
		return resource.View{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	if p.Archived() {
		return resource.View{}, project.ArchivedError()
	}
	var r resource.Resource
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.repo.FindForUpdate(ctx, org, e.ID, name)
		if err != nil {
			return err
		}
		r = cur
		switch {
		case cur.Status == resource.StatusDeleting:
			return resource.ErrDeleting(name)
		case input.StorageGi == cur.StorageGi:
			return nil
		case input.StorageGi < cur.StorageGi:
			return errx.InvalidField("storage_gi", "a resource only grows: it cannot go below "+resourceSize(cur.StorageGi))
		}
		from := cur.StorageGi
		r.StorageGi, r.Generation = input.StorageGi, cur.Generation+1
		if err := s.repo.Resize(ctx, org, e.ID, name, r.StorageGi, r.Generation); err != nil {
			return err
		}
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		return s.events.Publish(ctx, resource.NewResized(org, input.RequestedBy, r, from, names(p, e)))
	})
	if err != nil {
		return resource.View{}, err
	}
	return s.viewOf(ctx, p, r)
}

func resourceSize(gi int) string { return strconv.Itoa(gi) + " GiB" }

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, input resource.Delete) (resource.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	var r resource.Resource
	var connected []resource.Connection
	// Connecting a resource takes the same settings lock: a connection made
	// meanwhile either is seen here or waits and finds the resource deleting.
	err = s.usage.WithConnectionsLocked(ctx, org, id, e.ID, func(ctx context.Context, conns []resource.Connection) error {
		connected = conns
		cur, err := s.repo.FindForUpdate(ctx, org, e.ID, name)
		if err != nil {
			return err
		}
		r = cur
		if cur.Status == resource.StatusDeleting {
			return nil // already on its way out
		}
		for _, c := range conns {
			if c.Name == name {
				return resource.ErrInUse(name)
			}
		}
		r.Status, r.Generation = resource.StatusDeleting, cur.Generation+1
		if err := s.repo.MarkDeleting(ctx, org, e.ID, name, r.Generation); err != nil {
			return err
		}
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		return s.events.Publish(ctx, resource.NewDeleted(org, input.RequestedBy, r, names(p, e)))
	})
	if err != nil {
		return resource.View{}, err
	}
	return s.view(ctx, r, connected)
}

func (s *Service) Connect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input resource.Connect) (resource.Result, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return resource.Result{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.Find(ctx, org, e.ID, input.Name)
		if err != nil {
			return err
		}
		if r.Status == resource.StatusDeleting {
			return resource.ErrDeleting(input.Name)
		}
		applied, err = s.usage.Connect(ctx, org, id, e.ID, resource.Connection{Name: input.Name, Prefix: input.Prefix}, input.RequestedBy)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	return s.result(ctx, p, e, input.Name, applied)
}

func (s *Service) Disconnect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, by identity.UserID) (resource.Result, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := s.repo.Find(ctx, org, e.ID, name); err != nil {
			return err
		}
		applied, err = s.usage.Disconnect(ctx, org, id, e.ID, name, by)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	return s.result(ctx, p, e, name, applied)
}

func (s *Service) result(ctx context.Context, p project.Project, e project.Environment, name string, applied resource.Applied) (resource.Result, error) {
	r, err := s.repo.Find(ctx, p.Organization, e.ID, name)
	if err != nil {
		return resource.Result{}, err
	}
	v, err := s.viewOf(ctx, p, r)
	if err != nil {
		return resource.Result{}, err
	}
	return resource.Result{View: v, Revision: applied.Revision, Deployment: applied.Deployment}, nil
}

// Forget drops the row of a resource the cluster erased. Only a deleting
// resource is dropped: a report can never remove one that is in use.
func (s *Service) Forget(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, ok, err := s.repo.Forget(ctx, org, env, name)
		if err != nil || !ok {
			return err
		}
		return s.events.Publish(ctx, resource.NewRemoved(org, r))
	})
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (resource.Listing, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Listing{}, err
	}
	rows, err := s.repo.List(ctx, org, e.ID)
	if err != nil {
		return resource.Listing{}, err
	}
	connected, err := s.usage.Connections(ctx, org, p.ID, e.ID)
	if err != nil {
		return resource.Listing{}, err
	}
	out := resource.Listing{Items: make([]resource.View, 0, len(rows))}
	for _, r := range rows {
		v, err := s.view(ctx, r, connected)
		if err != nil {
			return resource.Listing{}, err
		}
		out.Items = append(out.Items, v)
	}
	if out.Kinds, err = s.cluster.Kinds(ctx, org, e.ID); err != nil {
		return resource.Listing{}, err
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string) (resource.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	r, err := s.repo.Find(ctx, org, e.ID, name)
	if err != nil {
		return resource.View{}, err
	}
	return s.viewOf(ctx, p, r)
}

func (s *Service) Named(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, names []string) ([]resource.View, error) {
	rows, err := s.repo.List(ctx, org, env)
	if err != nil {
		return nil, err
	}
	out := make([]resource.View, 0, len(names))
	for _, r := range rows {
		if !slices.Contains(names, r.Name) {
			continue
		}
		v, err := s.view(ctx, r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) (resource.Resource, error) {
	return s.repo.Find(ctx, org, env, name)
}
