package outboxpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Options tune the dispatcher; zero values take the defaults.
type Options struct {
	// MaxAttempts before a delivery is dead (default 10). A dead delivery
	// waits for RetryDead; for an ordered subscriber it holds back its
	// organization.
	MaxAttempts int
	// Backoff is the wait after the n-th failed attempt (default 2^n s, max 10 min).
	Backoff func(attempt int) time.Duration
	// Batch is the most deliveries per subscriber in one pass (default 100).
	Batch int
	// PollInterval is the fallback wake-up when no NOTIFY arrives (default 2s).
	PollInterval time.Duration
	// ListenURL, when set, is the database URL used to LISTEN for commits so
	// deliveries start within milliseconds; empty means poll only.
	ListenURL string
	Logger    *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 10
	}
	if o.Backoff == nil {
		o.Backoff = func(n int) time.Duration {
			if n > 9 {
				return 10 * time.Minute
			}
			return min(time.Duration(1<<n)*time.Second, 10*time.Minute)
		}
	}
	if o.Batch <= 0 {
		o.Batch = 100
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Dispatcher delivers pending events to the bus's subscribers. Any number
// of replicas may run: unordered deliveries are claimed with SKIP LOCKED and
// ordered ones under a per-(subscriber, organization) advisory lock.
type Dispatcher struct {
	db   *sqlx.DB
	bus  *event.Bus
	tx   txn.Runner
	opts Options
}

// NewDispatcher seals bus: every subscription must be registered before.
func NewDispatcher(db *sqlx.DB, bus *event.Bus, opts Options) *Dispatcher {
	bus.Seal()
	return &Dispatcher{db: db, bus: bus, tx: txn.Postgres{DB: db}, opts: opts.withDefaults()}
}

// Run delivers until ctx is done, woken by NOTIFY (when ListenURL is set)
// or by the poll interval. In-flight deliveries roll back on shutdown and
// are retried by the next run.
func (d *Dispatcher) Run(ctx context.Context) error {
	var notify <-chan *pq.Notification
	if d.opts.ListenURL != "" {
		l := pq.NewListener(d.opts.ListenURL, time.Second, time.Minute, func(_ pq.ListenerEventType, err error) {
			if err != nil {
				d.opts.Logger.Warn("outbox listener", "error", err)
			}
		})
		defer l.Close()
		if err := l.Listen(Channel); err != nil {
			return errx.Wrap(err, "listen "+Channel, errx.TypeInternal)
		}
		notify = l.Notify
	}
	tick := time.NewTicker(d.opts.PollInterval)
	defer tick.Stop()
	for {
		n, err := d.DrainOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.opts.Logger.Error("outbox dispatch", "error", err)
		}
		if n > 0 && err == nil {
			continue // there may be more: go again before sleeping
		}
		select {
		case <-ctx.Done():
			return nil
		case <-notify:
		case <-tick.C:
		}
	}
}

// DrainOnce makes one pass over every subscriber and reports how many
// deliveries succeeded. Tests call it (or Drain) instead of Run.
func (d *Dispatcher) DrainOnce(ctx context.Context) (int, error) {
	total := 0
	for _, s := range d.bus.All() {
		var n int
		var err error
		if s.Ordered {
			n, err = d.drainOrdered(ctx, s)
		} else {
			n, err = d.drainUnordered(ctx, s)
		}
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Drain repeats DrainOnce until a pass delivers nothing.
func (d *Dispatcher) Drain(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := d.DrainOnce(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

type row struct {
	ID           uuid.UUID               `db:"id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Type         string                  `db:"type"`
	Version      int                     `db:"version"`
	Actor        identity.UserID         `db:"actor_id"`
	Subject      string                  `db:"subject"`
	Summary      string                  `db:"summary"`
	Payload      []byte                  `db:"payload"`
	OccurredAt   time.Time               `db:"occurred_at"`
	Correlation  string                  `db:"correlation_id"`
	Causation    uuid.NullUUID           `db:"causation_id"`
	Client       string                  `db:"client"`
	Status       string                  `db:"status"`
	NextAt       time.Time               `db:"next_at"`
	Attempts     int                     `db:"attempts"`
}

func (r row) event() event.Event {
	return event.Event{
		ID: r.ID, Type: r.Type, Version: r.Version, Organization: r.Organization, Actor: r.Actor,
		Subject: r.Subject, Summary: r.Summary, Payload: r.Payload, OccurredAt: r.OccurredAt.UTC(),
		Meta: event.Meta{CorrelationID: r.Correlation, CausationID: r.Causation.UUID, Client: r.Client},
	}
}

const selectDelivery = `SELECT o.id, o.organization_id, o.type, o.version, o.actor_id, o.subject, o.summary,
       o.payload, o.occurred_at, o.correlation_id, o.causation_id, o.client, d.status, d.next_at, d.attempts
  FROM event.deliveries d JOIN event.outbox o ON o.id = d.event_id`

// drainUnordered delivers due deliveries oldest first; replicas skip the
// rows another one holds.
func (d *Dispatcher) drainUnordered(ctx context.Context, s event.Subscription) (int, error) {
	delivered := 0
	for range d.opts.Batch {
		var out outcome
		err := d.tx.Do(ctx, func(ctx context.Context) error {
			var r row
			err := txn.Q(ctx, d.db).GetContext(ctx, &r, selectDelivery+`
				 WHERE d.subscriber = $1 AND d.status = 'pending' AND d.next_at <= now()
				 ORDER BY d.seq LIMIT 1 FOR UPDATE OF d SKIP LOCKED`, s.Name)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return errx.Wrap(err, "claim delivery", errx.TypeInternal)
			}
			out, err = d.deliver(ctx, s, r)
			return err
		})
		if err != nil || out == none {
			return delivered, err
		}
		if out == done {
			delivered++
		}
	}
	return delivered, nil
}

// drainOrdered delivers each organization's events in seq order (seq is
// taken at insert, so events committed concurrently by unrelated
// transactions may interleave; writes to one entity are serialized by its
// row lock and keep their order). The oldest undelivered event gates its
// organization: while it backs off or is dead, nothing later is delivered
// (no gaps); other organizations go on.
func (d *Dispatcher) drainOrdered(ctx context.Context, s event.Subscription) (int, error) {
	var orgs []identity.OrganizationID
	if err := d.db.SelectContext(ctx, &orgs,
		`SELECT DISTINCT organization_id FROM event.deliveries
		  WHERE subscriber = $1 AND status = 'pending' AND next_at <= now()`, s.Name); err != nil {
		return 0, errx.Wrap(err, "list organizations to deliver", errx.TypeInternal)
	}
	delivered := 0
	for _, org := range orgs {
		for range d.opts.Batch {
			var out outcome
			err := d.tx.Do(ctx, func(ctx context.Context) error {
				var locked bool
				if err := txn.Q(ctx, d.db).GetContext(ctx, &locked,
					`SELECT pg_try_advisory_xact_lock(hashtext($1 || '/' || $2::text))`, s.Name, org); err != nil {
					return errx.Wrap(err, "lock organization stream", errx.TypeInternal)
				}
				if !locked {
					return nil // another replica is delivering this organization
				}
				var r row
				err := txn.Q(ctx, d.db).GetContext(ctx, &r, selectDelivery+`
					 WHERE d.subscriber = $1 AND d.organization_id = $2 AND d.status <> 'done'
					 ORDER BY d.seq LIMIT 1 FOR UPDATE OF d`, s.Name, org)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return errx.Wrap(err, "claim delivery", errx.TypeInternal)
				}
				if r.Status != "pending" || r.NextAt.After(time.Now()) {
					return nil // the head is dead or backing off: the organization waits
				}
				out, err = d.deliver(ctx, s, r)
				return err
			})
			if err != nil {
				return delivered, err
			}
			if out != done {
				break // nothing to do, or the head failed: this organization waits
			}
			delivered++
		}
	}
	return delivered, nil
}

type outcome int

const (
	none outcome = iota
	done
	failed
)

// deliver runs the handler on a claimed delivery inside a savepoint. On
// success the handler's writes and the "done" mark commit together; on
// failure only the handler's writes roll back, and the failed attempt is
// recorded in the same transaction, while the row (and the organization's
// lock) is still held, so no replica can retry it before its backoff.
func (d *Dispatcher) deliver(ctx context.Context, s event.Subscription, r row) (outcome, error) {
	q := txn.Q(ctx, d.db)
	if _, err := q.ExecContext(ctx, `SAVEPOINT delivery`); err != nil {
		return none, errx.Wrap(err, "delivery savepoint", errx.TypeInternal)
	}
	handleErr := d.handle(ctx, s, r)
	if handleErr == nil {
		return done, d.markDone(ctx, s, r)
	}
	if _, err := q.ExecContext(ctx, `ROLLBACK TO SAVEPOINT delivery`); err != nil {
		return none, errx.Wrap(err, "rollback delivery", errx.TypeInternal)
	}
	return failed, d.markFailed(ctx, s, r, handleErr)
}

// handle runs the subscriber's handler; a panic becomes an error so one bad
// handler cannot kill the dispatcher.
func (d *Dispatcher) handle(ctx context.Context, s event.Subscription, r row) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errx.Internal(fmt.Sprintf("handler panic: %v", p))
		}
	}()
	return s.Handle(event.WithCause(ctx, r.event()), r.event())
}

func (d *Dispatcher) markDone(ctx context.Context, s event.Subscription, r row) error {
	_, err := txn.Q(ctx, d.db).ExecContext(ctx,
		`UPDATE event.deliveries SET status = 'done', done_at = now(), attempts = attempts + 1, last_error = NULL
		  WHERE event_id = $1 AND subscriber = $2`, r.ID, s.Name)
	if err != nil {
		return errx.Wrap(err, "mark delivery done", errx.TypeInternal)
	}
	return nil
}

// markFailed records a failed attempt and schedules the retry, or buries it.
func (d *Dispatcher) markFailed(ctx context.Context, s event.Subscription, r row, cause error) error {
	attempts := r.Attempts + 1
	status := "pending"
	if attempts >= d.opts.MaxAttempts {
		status = "dead"
	}
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	_, err := txn.Q(ctx, d.db).ExecContext(ctx,
		`UPDATE event.deliveries SET attempts = $3, status = $4,
		        next_at = now() + make_interval(secs => $5::bigint / 1e6), last_error = $6
		  WHERE event_id = $1 AND subscriber = $2`,
		r.ID, s.Name, attempts, status, d.opts.Backoff(attempts).Microseconds(), msg)
	if err != nil {
		return errx.Wrap(err, "record failed delivery", errx.TypeInternal)
	}
	log := d.opts.Logger.Warn
	if status == "dead" {
		log = d.opts.Logger.Error
	}
	log("event delivery failed", "subscriber", s.Name, "event", r.ID, "type", r.Type,
		"organization", r.Organization, "attempt", attempts, "status", status, "error", msg)
	return nil
}

// RetryDead puts a subscriber's dead deliveries back in the queue (after
// the cause was fixed) and reports how many.
func (d *Dispatcher) RetryDead(ctx context.Context, subscriber string) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE event.deliveries SET status = 'pending', attempts = 0, next_at = now()
		  WHERE subscriber = $1 AND status = 'dead'`, subscriber)
	if err != nil {
		return 0, errx.Wrap(err, "retry dead deliveries", errx.TypeInternal)
	}
	return res.RowsAffected()
}

// Purge deletes events older than the cutoff whose deliveries all succeeded
// (subscribers such as the audit trail keep their own copy), in batches so
// no single statement holds locks for long.
func (d *Dispatcher) Purge(ctx context.Context, olderThan time.Time) (int64, error) {
	var total int64
	for {
		res, err := d.db.ExecContext(ctx,
			`DELETE FROM event.outbox WHERE id IN (
			   SELECT o.id FROM event.outbox o WHERE o.occurred_at < $1
			      AND NOT EXISTS (SELECT 1 FROM event.deliveries d WHERE d.event_id = o.id AND d.status <> 'done')
			    LIMIT 1000)`, olderThan)
		if err != nil {
			return total, errx.Wrap(err, "purge outbox", errx.TypeInternal)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, errx.Wrap(err, "purge outbox", errx.TypeInternal)
		}
		total += n
		if n < 1000 || ctx.Err() != nil {
			return total, nil
		}
	}
}

// Dead lists dead deliveries (oldest first, at most limit) for operators.
func (d *Dispatcher) Dead(ctx context.Context, limit int) ([]DeadDelivery, error) {
	var out []DeadDelivery
	err := d.db.SelectContext(ctx, &out, `SELECT d.subscriber, d.event_id, o.type, d.organization_id, d.attempts,
	       coalesce(d.last_error, '') AS last_error, o.occurred_at
	  FROM event.deliveries d JOIN event.outbox o ON o.id = d.event_id
	 WHERE d.status = 'dead' ORDER BY d.seq LIMIT $1`, limit)
	if err != nil {
		return nil, errx.Wrap(err, "list dead deliveries", errx.TypeInternal)
	}
	return out, nil
}

// DeadDelivery is a delivery that exhausted its attempts. For an ordered
// subscriber it holds back its organization until RetryDead.
type DeadDelivery struct {
	Subscriber   string                  `db:"subscriber" json:"subscriber"`
	Event        uuid.UUID               `db:"event_id" json:"event_id"`
	Type         string                  `db:"type" json:"type"`
	Organization identity.OrganizationID `db:"organization_id" json:"organization_id"`
	Attempts     int                     `db:"attempts" json:"attempts"`
	LastError    string                  `db:"last_error" json:"last_error"`
	OccurredAt   time.Time               `db:"occurred_at" json:"occurred_at"`
}

// Stats counts deliveries by subscriber and status (for health and metrics).
func (d *Dispatcher) Stats(ctx context.Context) (map[string]map[string]int, error) {
	var rows []struct {
		Subscriber string `db:"subscriber"`
		Status     string `db:"status"`
		N          int    `db:"n"`
	}
	if err := d.db.SelectContext(ctx, &rows,
		`SELECT subscriber, status, count(*) AS n FROM event.deliveries GROUP BY 1, 2`); err != nil {
		return nil, errx.Wrap(err, "delivery stats", errx.TypeInternal)
	}
	out := map[string]map[string]int{}
	for _, r := range rows {
		if out[r.Subscriber] == nil {
			out[r.Subscriber] = map[string]int{}
		}
		out[r.Subscriber][r.Status] = r.N
	}
	return out, nil
}
