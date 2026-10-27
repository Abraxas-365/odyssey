// Package auditsvc implements audit.Commands and audit.Queries.
package auditsvc

import (
	"context"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type Service struct {
	repo audit.Repository
	tx   txn.Runner
	now  func() time.Time
}

func New(repo audit.Repository, tx txn.Runner) *Service {
	return &Service{repo: repo, tx: tx, now: time.Now}
}

var (
	_ audit.Commands = (*Service)(nil)
	_ audit.Queries  = (*Service)(nil)
)

// Record appends e to its organization's chain. The head row lock
// serializes writers per organization; a re-delivered event is a no-op.
// The trail is the end of the line: recording publishes nothing.
func (s *Service) Record(ctx context.Context, e event.Event) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		head, err := s.repo.LockHead(ctx, e.Organization)
		if err != nil {
			return err
		}
		seen, err := s.repo.Exists(ctx, e.Organization, e.ID)
		if err != nil || seen {
			return err
		}
		entry, err := audit.FromEvent(e, head, s.now())
		if err != nil {
			return err
		}
		return s.repo.Append(ctx, entry)
	})
}

func (s *Service) List(ctx context.Context, organization identity.OrganizationID, filter audit.Filter, page query.Pagination) (query.Paginated[audit.Entry], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[audit.Entry]{}, err
	}
	return s.repo.List(ctx, organization, filter, page.Normalize())
}

// Verify re-hashes the organization's whole chain and checks it reaches the
// recorded head, so removing the newest entries is detected too. The head is
// read first: entries appended during the walk lie beyond it and are fine.
func (s *Service) Verify(ctx context.Context, organization identity.OrganizationID) (audit.Verification, error) {
	head, err := s.repo.Head(ctx, organization)
	if err != nil {
		return audit.Verification{}, err
	}
	var v audit.Verification
	broken := func(seq int64) {
		if v.BrokenAt == nil {
			v.BrokenAt = &seq
		}
	}
	prev := ""
	err = s.repo.Walk(ctx, organization, func(e audit.Entry) error {
		v.Entries++
		hash, err := e.ComputeHash()
		if err != nil || e.Seq != v.Entries || e.PrevHash != prev || hash != e.Hash ||
			(e.Seq == head.Seq && e.Hash != head.Hash) {
			broken(e.Seq)
		}
		prev = e.Hash
		return nil
	})
	if err != nil {
		return audit.Verification{}, err
	}
	if v.Entries < head.Seq { // the tail was cut off
		broken(v.Entries + 1)
	}
	v.Valid = v.BrokenAt == nil
	return v, nil
}
