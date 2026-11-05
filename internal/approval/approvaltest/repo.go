// Package approvaltest holds an in-memory approval.Repository for the tests of
// the service, the HTTP handler and the adapters (the Postgres one has its
// own tests).
package approvaltest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// Repo is the in-memory repository; Rows is exposed so tests can age a
// request (ExpiresAt) or read it directly.
type Repo struct {
	mu   sync.Mutex
	Rows []approval.Request
}

func NewRepo() *Repo { return &Repo{} }

var _ approval.Repository = (*Repo)(nil)

func open(x approval.Request) bool { return x.Status == approval.StatusPending || x.Run != "" }

func (f *Repo) Insert(_ context.Context, r approval.Request) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.Rows {
		if x.Organization == r.Organization && x.Key == r.Key && open(x) {
			return false, nil
		}
	}
	f.Rows = append(f.Rows, r)
	return true, nil
}

func (f *Repo) find(org identity.OrganizationID, ok func(approval.Request) bool) (approval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.Rows {
		if x.Organization == org && ok(x) {
			return x, nil
		}
	}
	return approval.Request{}, errx.NotFound("approval not found")
}

func (f *Repo) FindOpen(_ context.Context, org identity.OrganizationID, key string) (approval.Request, error) {
	return f.find(org, func(x approval.Request) bool { return x.Key == key && open(x) })
}

func (f *Repo) Find(_ context.Context, org identity.OrganizationID, id approval.ID) (approval.Request, error) {
	return f.find(org, func(x approval.Request) bool { return x.ID == id })
}

func (f *Repo) FindForUpdate(ctx context.Context, org identity.OrganizationID, id approval.ID) (approval.Request, error) {
	return f.Find(ctx, org, id)
}

func (f *Repo) FindByRun(_ context.Context, org identity.OrganizationID, run string) (approval.Request, error) {
	return f.find(org, func(x approval.Request) bool { return x.Run == run })
}

func (f *Repo) List(_ context.Context, org identity.OrganizationID, v approval.Viewer, fl approval.Filter, p query.Pagination) (query.Paginated[approval.Request], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []approval.Request
	for i := len(f.Rows) - 1; i >= 0; i-- { // newest first
		x := f.Rows[i]
		if x.Organization != org || !v.Sees(x) {
			continue
		}
		if fl.Mine && x.RequestedBy != v.User || fl.Decidable && !v.Can(x) {
			continue
		}
		if len(fl.Status) > 0 && !has(fl.Status, x.Status) {
			continue
		}
		out = append(out, x)
	}
	return query.NewPaginated(out, len(out), p), nil
}

func has(all []approval.Status, s approval.Status) bool {
	for _, x := range all {
		if x == s {
			return true
		}
	}
	return false
}

func (f *Repo) ListExpired(_ context.Context, now time.Time, limit int) ([]approval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []approval.Request
	for _, x := range f.Rows {
		if x.Status == approval.StatusPending && !x.ExpiresAt.After(now) {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Repo) Decide(_ context.Context, org identity.OrganizationID, id approval.ID, s approval.Status, by *identity.UserID, note string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.Rows {
		if x.Organization == org && x.ID == id && x.Status == approval.StatusPending {
			f.Rows[i].Status, f.Rows[i].DecidedBy, f.Rows[i].DecidedAt, f.Rows[i].DecisionNote = s, by, &at, note
			return true, nil
		}
	}
	return false, nil
}

func (f *Repo) Consume(_ context.Context, org identity.OrganizationID, id approval.ID, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.Rows {
		if x.Organization == org && x.ID == id && x.Status == approval.StatusApproved && x.ConsumedAt == nil {
			f.Rows[i].ConsumedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (f *Repo) SetExecutionError(_ context.Context, org identity.OrganizationID, id approval.ID, m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.Rows {
		if x.Organization == org && x.ID == id {
			f.Rows[i].ExecutionError = m
			return nil
		}
	}
	return errx.NotFound("approval not found")
}
