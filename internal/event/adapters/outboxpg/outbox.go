// Package outboxpg is the Postgres transactional outbox: Publish stores
// events (and one delivery per subscriber) in the caller's transaction, and
// the Dispatcher delivers them to the subscribers' handlers, each delivery in
// its own transaction, with retries, backoff and dead-lettering.
package outboxpg

import (
	"context"
	"embed"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
)

// Schema is the Postgres schema (and migration owner) of the outbox.
const Schema = "event"

// Channel is the NOTIFY channel that wakes dispatchers after a commit.
const Channel = "odyssey_outbox"

//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations are the outbox's *.up.sql files.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// Outbox is the event.Publisher over Postgres.
type Outbox struct{ bus *event.Bus }

// New returns a publisher that creates deliveries for bus's subscribers.
func New(bus *event.Bus) *Outbox { return &Outbox{bus: bus} }

var _ event.Publisher = (*Outbox)(nil)

// Publish inserts the events and their deliveries into the transaction in
// ctx; listeners are notified when it commits. It fails outside a transaction.
func (o *Outbox) Publish(ctx context.Context, events ...event.Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, ok := txn.Tx(ctx)
	if !ok {
		return errx.Internal("event: publish outside a transaction")
	}
	for i := range events {
		e := events[i]
		if !event.ValidType(e.Type) || e.ID == uuid.Nil || e.Organization.IsZero() {
			return errx.Internal("event: invalid event " + e.Type)
		}
		event.Stamp(ctx, &e)
		var causation any
		if e.CausationID != uuid.Nil {
			causation = e.CausationID
		}
		var seq int64
		err := tx.QueryRowxContext(ctx,
			`INSERT INTO event.outbox (id, organization_id, type, version, actor_id, subject, summary, payload,
			                           occurred_at, correlation_id, causation_id, client)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING seq`,
			e.ID, e.Organization, e.Type, e.Version, e.Actor, e.Subject, e.Summary, []byte(e.Payload),
			e.OccurredAt, e.CorrelationID, causation, e.Client).Scan(&seq)
		if err != nil {
			return errx.Wrap(err, "publish "+e.Type, errx.TypeInternal)
		}
		for _, s := range o.bus.For(e.Type) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO event.deliveries (event_id, subscriber, seq, organization_id) VALUES ($1, $2, $3, $4)`,
				e.ID, s.Name, seq, e.Organization); err != nil {
				return errx.Wrap(err, "publish "+e.Type+" to "+s.Name, errx.TypeInternal)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_notify($1, '')`, Channel); err != nil {
		return errx.Wrap(err, "notify outbox", errx.TypeInternal)
	}
	return nil
}
