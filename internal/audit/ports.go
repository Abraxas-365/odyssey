package audit

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/google/uuid"
)

// Commands are the writes, implemented by auditsvc.Service.
type Commands interface {
	// Record appends e to its organization's chain (idempotent by event ID).
	Record(ctx context.Context, e event.Event) error
}

// Queries are the reads, implemented by auditsvc.Service.
type Queries interface {
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Entry], error)
	Verify(ctx context.Context, organization identity.OrganizationID) (Verification, error)
}

// Repository persists the chain; only the service depends on it.
type Repository interface {
	// LockHead returns the organization's chain tip, locked until the
	// transaction in ctx ends (call it inside txn.Runner.Do).
	LockHead(ctx context.Context, organization identity.OrganizationID) (Head, error)
	// Head reads the chain tip without locking (zero when there is none).
	Head(ctx context.Context, organization identity.OrganizationID) (Head, error)
	Exists(ctx context.Context, organization identity.OrganizationID, id uuid.UUID) (bool, error)
	// Append inserts the entry and moves the head to it.
	Append(ctx context.Context, entry Entry) error
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Entry], error)
	// Walk calls fn with every entry of the organization in chain order.
	Walk(ctx context.Context, organization identity.OrganizationID, fn func(Entry) error) error
}
