package outboxpg_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type payload struct {
	N int `json:"n"`
}

func newEvent(org identity.OrganizationID, n int) event.Event {
	return event.New("widget.changed", 1, org, identity.UserID{}, "widget/x", "Widget cambiado", payload{N: n})
}

func setup(t *testing.T, bus *event.Bus, opts outboxpg.Options) (*sqlx.DB, *outboxpg.Outbox, *outboxpg.Dispatcher) {
	t.Helper()
	db := pgtest.DB(t, migrate.Set{Owner: outboxpg.Schema, Files: outboxpg.Migrations()})
	if opts.Backoff == nil {
		opts.Backoff = func(int) time.Duration { return 0 } // retry at once: deterministic tests
	}
	opts.Logger = slog.New(slog.DiscardHandler)
	return db, outboxpg.New(bus), outboxpg.NewDispatcher(db, bus, opts)
}

func publish(t *testing.T, db *sqlx.DB, out *outboxpg.Outbox, ctx context.Context, events ...event.Event) {
	t.Helper()
	if err := (txn.Postgres{DB: db}).Do(ctx, func(ctx context.Context) error { return out.Publish(ctx, events...) }); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sqlx.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Get(&n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPublishIsAtomicWithTheWrite(t *testing.T) {
	bus := event.NewBus()
	bus.Add(event.Subscription{Name: "a", Handle: func(context.Context, event.Event) error { return nil }})
	db, out, _ := setup(t, bus, outboxpg.Options{})
	ctx := context.Background()
	org := identity.NewOrganizationID()

	if err := out.Publish(ctx, newEvent(org, 1)); err == nil {
		t.Fatal("publish outside a transaction must fail")
	}
	boom := errors.New("write failed")
	err := txn.Postgres{DB: db}.Do(ctx, func(ctx context.Context) error {
		if err := out.Publish(ctx, newEvent(org, 1)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count(t, db, `SELECT count(*) FROM event.outbox`) != 0 || count(t, db, `SELECT count(*) FROM event.deliveries`) != 0 {
		t.Fatalf("a rolled-back write must leave no event: %v", err)
	}

	ctx = event.WithMeta(ctx, event.Meta{CorrelationID: "req-9", Client: "console"})
	publish(t, db, out, ctx, newEvent(org, 1))
	var got struct {
		Correlation string `db:"correlation_id"`
		Client      string `db:"client"`
	}
	if err := db.Get(&got, `SELECT correlation_id, client FROM event.outbox`); err != nil || got.Correlation != "req-9" || got.Client != "console" {
		t.Fatalf("provenance %+v %v", got, err)
	}
}

func TestDispatcherRetriesIsolatesAndBuries(t *testing.T) {
	var mu sync.Mutex
	okSeen, flakySeen := 0, 0
	bus := event.NewBus()
	bus.Add(event.Subscription{Name: "ok", Handle: func(context.Context, event.Event) error {
		mu.Lock()
		okSeen++
		mu.Unlock()
		return nil
	}})
	bus.Add(event.Subscription{Name: "flaky", Handle: func(context.Context, event.Event) error {
		mu.Lock()
		defer mu.Unlock()
		flakySeen++
		if flakySeen < 3 {
			return errors.New("temporary")
		}
		return nil
	}})
	bus.Add(event.Subscription{Name: "broken", Handle: func(context.Context, event.Event) error { panic("bug") }})
	db, out, d := setup(t, bus, outboxpg.Options{MaxAttempts: 4})
	ctx := context.Background()
	publish(t, db, out, ctx, newEvent(identity.NewOrganizationID(), 1))

	if _, err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if okSeen != 1 || flakySeen != 3 {
		t.Fatalf("ok=%d flaky=%d", okSeen, flakySeen)
	}
	stats, err := d.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["ok"]["done"] != 1 || stats["flaky"]["done"] != 1 || stats["broken"]["dead"] != 1 {
		t.Fatalf("stats %v", stats)
	}
	var lastError string
	_ = db.Get(&lastError, `SELECT last_error FROM event.deliveries WHERE subscriber = 'broken'`)
	if lastError == "" {
		t.Fatal("a dead delivery keeps its error")
	}
	if dead, err := d.Dead(ctx, 10); err != nil || len(dead) != 1 || dead[0].Subscriber != "broken" || dead[0].Attempts != 4 {
		t.Fatalf("dead %+v %v", dead, err)
	}
	if n, err := d.RetryDead(ctx, "broken"); err != nil || n != 1 {
		t.Fatalf("retry dead: %d %v", n, err)
	}
	// purge keeps events with undelivered subscribers
	if n, _ := d.Purge(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("purged an event still pending for broken: %d", n)
	}
}

func TestHandlerWritesCommitWithTheDelivery(t *testing.T) {
	bus := event.NewBus()
	var db *sqlx.DB
	fail := true
	bus.Add(event.Subscription{Name: "writer", Handle: func(ctx context.Context, e event.Event) error {
		if _, err := txn.Q(ctx, db).ExecContext(ctx, `INSERT INTO event.side_effects VALUES ($1)`, e.ID); err != nil {
			return err
		}
		if fail {
			fail = false
			return errors.New("after the write")
		}
		return nil
	}})
	db, out, d := setup(t, bus, outboxpg.Options{})
	if _, err := db.Exec(`CREATE TABLE event.side_effects (event_id uuid PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publish(t, db, out, ctx, newEvent(identity.NewOrganizationID(), 1))
	if _, err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	// the first attempt's insert rolled back with it; the retry wrote once
	if n := count(t, db, `SELECT count(*) FROM event.side_effects`); n != 1 {
		t.Fatalf("side effects: %d", n)
	}
	if n := count(t, db, `SELECT attempts FROM event.deliveries WHERE subscriber = 'writer'`); n != 2 {
		t.Fatalf("attempts: %d", n)
	}
}

// A handler whose SQL fails leaves the transaction aborted; the failed
// attempt is still recorded (with the delivery still locked) and retried.
func TestHandlerSQLErrorIsRecordedAndRetried(t *testing.T) {
	bus := event.NewBus()
	var db *sqlx.DB
	attempts := 0
	bus.Add(event.Subscription{Name: "sql", Ordered: true, Handle: func(ctx context.Context, e event.Event) error {
		attempts++
		if attempts == 1 {
			_, err := txn.Q(ctx, db).ExecContext(ctx, `INSERT INTO event.no_such_table VALUES (1)`)
			return err
		}
		return nil
	}})
	db, out, d := setup(t, bus, outboxpg.Options{})
	ctx := context.Background()
	publish(t, db, out, ctx, newEvent(identity.NewOrganizationID(), 1))
	for range 2 { // an ordered head that failed waits for the next pass
		if _, err := d.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var row struct {
		Status    string  `db:"status"`
		Attempts  int     `db:"attempts"`
		LastError *string `db:"last_error"`
	}
	if err := db.Get(&row, `SELECT status, attempts, last_error FROM event.deliveries WHERE subscriber = 'sql'`); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || row.Status != "done" || row.Attempts != 2 {
		t.Fatalf("handler ran %d times, delivery %+v", attempts, row)
	}
}

func TestOrderedSubscriberNeverSkips(t *testing.T) {
	var got []int
	failN := 2 // event n=2 fails until released
	bus := event.NewBus()
	bus.Add(event.Subscription{Name: "trail", Ordered: true, Handle: func(_ context.Context, e event.Event) error {
		p, _ := event.Decode[payload](e)
		if p.N == failN {
			return errors.New("not yet")
		}
		got = append(got, p.N)
		return nil
	}})
	db, out, d := setup(t, bus, outboxpg.Options{MaxAttempts: 2})
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	publish(t, db, out, ctx, newEvent(org, 1), newEvent(org, 2), newEvent(org, 3))
	publish(t, db, out, ctx, newEvent(other, 10))

	if _, err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	// org stops at its dead head (2), never delivering 3 before it; the
	// other organization is not blocked. Order across organizations is free.
	if !equal(only(got, 1, 2, 3), []int{1}) || !equal(only(got, 10), []int{10}) {
		t.Fatalf("delivered %v", got)
	}
	failN = 0
	if _, err := d.RetryDead(ctx, "trail"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if !equal(only(got, 1, 2, 3), []int{1, 2, 3}) || len(got) != 4 {
		t.Fatalf("after retry %v", got)
	}
}

// only keeps the values of xs that are in keep, in order.
func only(xs []int, keep ...int) []int {
	var out []int
	for _, x := range xs {
		for _, k := range keep {
			if x == k {
				out = append(out, x)
			}
		}
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunWakesOnNotify(t *testing.T) {
	done := make(chan struct{}, 1)
	bus := event.NewBus()
	bus.Add(event.Subscription{Name: "fast", Handle: func(context.Context, event.Event) error {
		done <- struct{}{}
		return nil
	}})
	db := pgtest.DB(t, migrate.Set{Owner: outboxpg.Schema, Files: outboxpg.Migrations()})
	var name string
	if err := db.Get(&name, `SELECT current_database()`); err != nil {
		t.Fatal(err)
	}
	out := outboxpg.New(bus)
	d := outboxpg.NewDispatcher(db, bus, outboxpg.Options{PollInterval: time.Hour, ListenURL: pgtest.URL(t, name)})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- d.Run(ctx) }()
	time.Sleep(200 * time.Millisecond) // let LISTEN start; the first pass found nothing
	publish(t, db, out, context.Background(), newEvent(identity.NewOrganizationID(), 1))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("NOTIFY did not wake the dispatcher (poll interval is 1h)")
	}
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
