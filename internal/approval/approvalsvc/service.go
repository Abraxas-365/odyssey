// Package approvalsvc implements approval.Commands and approval.Queries.
package approvalsvc

import (
	"context"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type Service struct {
	repo   approval.Repository
	tx     txn.Runner
	events event.Publisher
	now    func() time.Time // the clock (tests set it)
}

func New(repo approval.Repository, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, tx: tx, events: events, now: time.Now}
}

var (
	_ approval.Commands = (*Service)(nil)
	_ approval.Queries  = (*Service)(nil)
)

// expireBatch bounds the requests one Expire transaction handles.
const expireBatch = 100

func (s *Service) Open(ctx context.Context, org identity.OrganizationID, input approval.Open) (approval.Request, error) {
	if err := input.Validate(); err != nil {
		return approval.Request{}, err
	}
	input = input.Normalized()
	payload, err := approval.Canonical(input.Payload)
	if err != nil {
		return approval.Request{}, err
	}
	key := approval.KeyOf(input, payload)
	var out approval.Request
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if held, err := s.repo.FindOpen(ctx, org, key); err == nil {
			out = held
			return nil
		} else if !errx.IsType(err, errx.TypeNotFound) {
			return err
		}
		now := s.now()
		r := approval.Request{ID: approval.NewID(), Organization: org, Action: input.Action, Project: input.Project,
			Environment: input.Environment, Summary: input.Summary, Payload: payload,
			RequestedBy: input.Requester.User, RequestedSystem: input.Requester.System,
			RequesterPermissions: input.Requester.Permissions, Reason: input.Reason, Rules: input.Rules,
			Approvers: input.Approvers, Status: approval.StatusPending, ExpiresAt: now.Add(approval.TTL),
			Run: input.Run, RunStep: input.RunStep, CreatedAt: now, Key: key}
		inserted, err := s.repo.Insert(ctx, r)
		if err != nil {
			return err
		}
		if !inserted { // a concurrent ask won: that request is the one
			held, err := s.repo.FindOpen(ctx, org, key)
			out = held
			return err
		}
		out = r
		return s.events.Publish(ctx, approval.NewRequested(r))
	})
	return out, err
}

// visible reads a request the viewer may see (theirs, or one they could
// decide) and locks it; any other is 404 so nothing leaks.
func (s *Service) visible(ctx context.Context, org identity.OrganizationID, id approval.ID, v approval.Viewer) (approval.Request, error) {
	r, err := s.repo.FindForUpdate(ctx, org, id)
	if err != nil {
		return approval.Request{}, err
	}
	if !v.Sees(r) {
		return approval.Request{}, errx.NotFound("approval not found")
	}
	return r, nil
}

func (s *Service) Approve(ctx context.Context, org identity.OrganizationID, id approval.ID, input approval.Decide) (approval.Request, error) {
	if err := input.Validate(); err != nil {
		return approval.Request{}, err
	}
	return s.decide(ctx, org, id, input, approval.StatusApproved)
}

func (s *Service) Reject(ctx context.Context, org identity.OrganizationID, id approval.ID, input approval.Decide) (approval.Request, error) {
	if err := input.Validate(); err != nil {
		return approval.Request{}, err
	}
	return s.decide(ctx, org, id, input, approval.StatusRejected)
}

func (s *Service) decide(ctx context.Context, org identity.OrganizationID, id approval.ID, input approval.Decide, to approval.Status) (approval.Request, error) {
	var out approval.Request
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.visible(ctx, org, id, input.By)
		if err != nil {
			return err
		}
		if r.Status.Decided() {
			return approval.ErrDecided(r.Status)
		}
		if !input.By.User.IsZero() && input.By.User == r.RequestedBy {
			return approval.ErrSelf()
		}
		if !r.CanBeDecidedBy(input.By.Permissions) {
			return errx.Forbidden("No tienes el permiso para decidir esta solicitud.")
		}
		now := s.now()
		if r.ExpiredAt(now) { // the schedule has not got to it yet
			return approval.ErrDecided(approval.StatusExpired)
		}
		by := input.By.User
		if ok, err := s.repo.Decide(ctx, org, id, to, &by, input.Note, now); err != nil {
			return err
		} else if !ok {
			return approval.ErrDecided(r.Status)
		}
		r.Status, r.DecidedBy, r.DecidedAt, r.DecisionNote = to, &by, &now, input.Note
		out = r
		if to == approval.StatusApproved {
			return s.events.Publish(ctx, approval.NewApproved(r, by, input.Note))
		}
		return s.events.Publish(ctx, approval.NewRejected(r, by, input.Note))
	})
	return out, err
}

func (s *Service) Cancel(ctx context.Context, org identity.OrganizationID, id approval.ID, input approval.Cancel) (approval.Request, error) {
	var out approval.Request
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.visible(ctx, org, id, input.By)
		if err != nil {
			return err
		}
		if input.By.User.IsZero() || input.By.User != r.RequestedBy {
			return errx.Forbidden("Solo quien hizo la solicitud puede cancelarla.")
		}
		if r.Status.Decided() {
			return approval.ErrDecided(r.Status)
		}
		now := s.now()
		if ok, err := s.repo.Decide(ctx, org, id, approval.StatusCancelled, nil, "", now); err != nil {
			return err
		} else if !ok {
			return approval.ErrDecided(r.Status)
		}
		r.Status, r.DecidedAt = approval.StatusCancelled, &now
		out = r
		return s.events.Publish(ctx, approval.NewCancelled(r, input.By.User))
	})
	return out, err
}

func (s *Service) CancelRun(ctx context.Context, org identity.OrganizationID, run, step string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.FindByRun(ctx, org, run, step)
		if errx.IsType(err, errx.TypeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		ok, err := s.repo.Decide(ctx, org, r.ID, approval.StatusCancelled, nil, "", now)
		if err != nil {
			return err
		}
		if !ok {
			// Not pending any more. An approve that won the race against this
			// cancel left an approval nobody will use (the run goes on without
			// it): used up, so it never lingers as approved in the Bandeja.
			if r, err = s.repo.FindForUpdate(ctx, org, r.ID); err != nil || !r.Usable() {
				return err
			}
			return s.consume(ctx, r)
		}
		r.Status, r.DecidedAt = approval.StatusCancelled, &now
		return s.events.Publish(ctx, approval.NewCancelled(r, identity.UserID{}))
	})
}

func (s *Service) Redeem(ctx context.Context, org identity.OrganizationID, id approval.ID, grant approval.Grant) error {
	payload, err := approval.Canonical(grant.Payload)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.FindForUpdate(ctx, org, id)
		if errx.IsType(err, errx.TypeNotFound) {
			return approval.ErrNotUsable()
		}
		if err != nil {
			return err
		}
		a := actor.From(ctx)
		sameActor := (!a.User.IsZero() && a.User == r.RequestedBy) || (a.User.IsZero() && a.System != "" && a.System == r.RequestedSystem)
		if !r.Usable() || !sameActor || r.Action != grant.Action || r.Project != grant.Project ||
			r.Environment != grant.Environment || !approval.SamePayload(r.Payload, payload) {
			return approval.ErrNotUsable()
		}
		return s.consume(ctx, r)
	})
}

func (s *Service) Consume(ctx context.Context, org identity.OrganizationID, id approval.ID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.FindForUpdate(ctx, org, id)
		if err != nil {
			return err
		}
		if r.ConsumedAt != nil {
			return nil // used already: the one use happened
		}
		if r.Status != approval.StatusApproved {
			return approval.ErrNotUsable()
		}
		return s.consume(ctx, r)
	})
}

func (s *Service) consume(ctx context.Context, r approval.Request) error {
	ok, err := s.repo.Consume(ctx, r.Organization, r.ID, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return approval.ErrNotUsable()
	}
	return s.events.Publish(ctx, approval.NewConsumed(r))
}

func (s *Service) Fail(ctx context.Context, org identity.OrganizationID, id approval.ID, message string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.FindForUpdate(ctx, org, id)
		if err != nil {
			return err
		}
		if err := s.repo.SetExecutionError(ctx, org, id, message); err != nil {
			return err
		}
		return s.events.Publish(ctx, approval.NewExecutionFailed(r, message))
	})
}

func (s *Service) Expire(ctx context.Context) (int, error) {
	total := 0
	for {
		n, last := 0, false
		err := s.tx.Do(ctx, func(ctx context.Context) error {
			now := s.now()
			due, err := s.repo.ListExpired(ctx, now, expireBatch)
			if err != nil {
				return err
			}
			n, last = 0, len(due) < expireBatch
			for _, r := range due {
				ok, err := s.repo.Decide(ctx, r.Organization, r.ID, approval.StatusExpired, nil, "", now)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				r.Status, r.DecidedAt = approval.StatusExpired, &now
				if err := s.events.Publish(ctx, approval.NewExpired(r)); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		total += n
		if err != nil || last {
			return total, err
		}
	}
}

// ---- queries ----

func (s *Service) view(r approval.Request, v approval.Viewer) approval.View {
	return approval.View{Request: r, Mine: !v.User.IsZero() && v.User == r.RequestedBy,
		CanDecide: r.Status == approval.StatusPending && !r.ExpiredAt(s.now()) && v.Can(r)}
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id approval.ID, v approval.Viewer) (approval.View, error) {
	r, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return approval.View{}, err
	}
	if !v.Sees(r) {
		return approval.View{}, errx.NotFound("approval not found")
	}
	return s.view(r, v), nil
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, v approval.Viewer, filter approval.Filter, page query.Pagination) (query.Paginated[approval.View], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[approval.View]{}, err
	}
	page = page.Normalize()
	got, err := s.repo.List(ctx, org, v, filter, page)
	if err != nil {
		return query.Paginated[approval.View]{}, err
	}
	items := make([]approval.View, len(got.Items))
	for i, r := range got.Items {
		items[i] = s.view(r, v)
	}
	return query.NewPaginated(items, got.Page.Total, page), nil
}

func (s *Service) Get(ctx context.Context, org identity.OrganizationID, id approval.ID) (approval.Request, error) {
	return s.repo.Find(ctx, org, id)
}
