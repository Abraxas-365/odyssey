// Package txn makes a database transaction travel in the context, so a
// service can make several repository calls (and publish events) atomically
// without the repositories' ports knowing about transactions.
//
// The service owns the boundary:
//
//	err := s.tx.Do(ctx, func(ctx context.Context) error {
//		if err := s.repo.Create(ctx, org, x); err != nil {
//			return err
//		}
//		return s.events.Publish(ctx, domain.NewCreated(org, actor, x))
//	})
//
// and repositories run every statement on txn.Q(ctx, r.db), which is the
// transaction in ctx when there is one and the pool otherwise. A nested Do
// joins the outer transaction.
package txn

import (
	"context"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/jmoiron/sqlx"
)

// Runner runs fn atomically: fn's error (or panic) rolls everything back.
type Runner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Querier is what both *sqlx.DB and *sqlx.Tx offer; repositories use it.
type Querier interface {
	sqlx.ExtContext
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	QueryRowxContext(ctx context.Context, query string, args ...any) *sqlx.Row
}

var (
	_ Querier = (*sqlx.DB)(nil)
	_ Querier = (*sqlx.Tx)(nil)
)

type ctxKey struct{}

// Postgres is the Runner over a sqlx pool.
type Postgres struct{ DB *sqlx.DB }

var _ Runner = Postgres{}

func (p Postgres) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := Tx(ctx); ok {
		return fn(ctx)
	}
	tx, err := p.DB.BeginTxx(ctx, nil)
	if err != nil {
		return errx.Wrap(err, "begin transaction", errx.TypeInternal)
	}
	hooks := &rollbackHooks{}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			hooks.run()
		}
	}()
	if err := fn(context.WithValue(With(ctx, tx), hooksKey{}, hooks)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errx.Wrap(err, "commit transaction", errx.TypeInternal)
	}
	committed = true
	return nil
}

// OnRollback registers fn to run if the transaction carried by ctx does not
// commit (fn's error or panic, or a failed COMMIT), after the rollback. It is
// for compensating side effects outside the database, such as deleting a blob
// written before the row that references it. Inside a nested Do the hook
// belongs to the OUTER transaction: it fires on the outcome that actually
// counts (e.g. the Idempotency-Key middleware's commit), not the inner step.
//
// It reports false when ctx has no transaction from Postgres.Do (Passthrough,
// or a transaction attached with With): then nothing is registered, so the
// caller must also compensate on its own error. Make fn idempotent
// (sync.OnceFunc) when both paths may run it, and give it a context that
// survives the request (context.WithoutCancel).
func OnRollback(ctx context.Context, fn func()) bool {
	hooks, ok := ctx.Value(hooksKey{}).(*rollbackHooks)
	if !ok {
		return false
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	hooks.fns = append(hooks.fns, fn)
	return true
}

type hooksKey struct{}

type rollbackHooks struct {
	mu  sync.Mutex
	fns []func()
}

// run calls the hooks newest first, like defers.
func (h *rollbackHooks) run() {
	h.mu.Lock()
	fns := h.fns
	h.fns = nil
	h.mu.Unlock()
	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
}

// With returns ctx carrying tx. Only Runners (and the event dispatcher, which
// runs handlers inside its delivery transaction) should call it.
func With(ctx context.Context, tx *sqlx.Tx) context.Context {
	return context.WithValue(ctx, ctxKey{}, tx)
}

// Tx is the transaction carried by ctx, if any.
func Tx(ctx context.Context) (*sqlx.Tx, bool) {
	tx, ok := ctx.Value(ctxKey{}).(*sqlx.Tx)
	return tx, ok && tx != nil
}

// Q is the transaction in ctx, or db when there is none.
func Q(ctx context.Context, db *sqlx.DB) Querier {
	if tx, ok := Tx(ctx); ok {
		return tx
	}
	return db
}

// Passthrough runs fn without a transaction: for unit tests with fake
// repositories (and a fake event publisher).
type Passthrough struct{}

var _ Runner = Passthrough{}

func (Passthrough) Do(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }
