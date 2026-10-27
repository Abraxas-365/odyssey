// Package workflowpg is the workflow.Engine on Postgres: run state lives in
// the "workflow" schema and River (schema "river") schedules the steps.
// Every River job carries the run's revision; any transition (step done,
// signal, cancel) bumps it, so jobs that lost a race are discarded.
package workflowpg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
)

// Schema is the Postgres schema (and migration owner) of the run state.
const Schema = "workflow"

// RiverSchema holds River's own tables (migrated by MigrateRiver).
const RiverSchema = "river"

const queue = "workflow"

//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations are the run-state *.up.sql files.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrateRiver creates or upgrades River's tables. Call it after
// migrate.Apply, inside migrate.Lock so replicas don't race.
func MigrateRiver(ctx context.Context, db *sqlx.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+RiverSchema); err != nil {
		return errx.Wrap(err, "create river schema", errx.TypeInternal)
	}
	m, err := rivermigrate.New(riverdatabasesql.New(db.DB), &rivermigrate.Config{Schema: RiverSchema, Logger: quiet()})
	if err != nil {
		return errx.Wrap(err, "river migrator", errx.TypeInternal)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return errx.Wrap(err, "migrate river", errx.TypeInternal)
	}
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Options tune the engine; zero values take the defaults.
type Options struct {
	// Concurrency is how many steps run at once in this process (default 20).
	Concurrency int
	// Backoff is the wait before retrying a step after its n-th failure
	// (default 2^n s, max 10 min).
	Backoff func(attempt int) time.Duration
	// StepTimeout cancels one execution of a step (default 10 min). Long waits
	// belong in Poll/WaitFor, not in a blocked step.
	StepTimeout time.Duration
	// PollInterval is how often idle workers look for due steps (default 1s;
	// with ListenURL set, new work is also announced immediately).
	PollInterval time.Duration
	// ListenURL, when set, opens a dedicated connection for River's LISTEN.
	ListenURL string
	// StopTimeout is how long running steps may finish after Run's ctx ends
	// before they are cancelled (default 30s; they are retried later).
	StopTimeout time.Duration
	Logger      *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Concurrency <= 0 {
		o.Concurrency = 20
	}
	if o.Backoff == nil {
		o.Backoff = func(n int) time.Duration {
			if n > 9 {
				return 10 * time.Minute
			}
			return min(time.Duration(1<<n)*time.Second, 10*time.Minute)
		}
	}
	if o.StepTimeout <= 0 {
		o.StepTimeout = 10 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Engine implements workflow.Engine. The API process only starts and
// signals runs; the worker process also executes them (Run).
type Engine struct {
	db       *sqlx.DB
	registry *workflow.Registry
	opts     Options
	river    *river.Client[*sql.Tx]
	listener *pgxpool.Pool
}

var _ workflow.Engine = (*Engine)(nil)

// New builds the engine over registry (which it seals).
func New(ctx context.Context, db *sqlx.DB, registry *workflow.Registry, opts Options) (*Engine, error) {
	registry.Seal()
	e := &Engine{db: db, registry: registry, opts: opts.withDefaults()}
	workers := river.NewWorkers()
	river.AddWorker(workers, &stepWorker{e: e})
	river.AddWorker(workers, &scheduleWorker{e: e})

	var periodic []*river.PeriodicJob
	for _, s := range registry.Schedules() {
		periodic = append(periodic, river.NewPeriodicJob(river.PeriodicInterval(s.Every),
			func() (river.JobArgs, *river.InsertOpts) {
				return scheduleArgs{Name: s.Name}, &river.InsertOpts{Queue: queue, MaxAttempts: 3,
					UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: s.Every}}
			}, &river.PeriodicJobOpts{ID: s.Name}))
	}

	driver := riverdatabasesql.New(db.DB)
	if e.opts.ListenURL != "" {
		cfg, err := pgxpool.ParseConfig(e.opts.ListenURL)
		if err != nil {
			return nil, errx.Wrap(err, "parse workflow listen url", errx.TypeInternal)
		}
		cfg.MinConns, cfg.MaxConns = 0, 1
		if e.listener, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
			return nil, errx.Wrap(err, "open workflow listener", errx.TypeInternal)
		}
		driver = riverdatabasesql.NewWithPgxListener(db.DB, e.listener)
	}
	client, err := river.NewClient(driver, &river.Config{
		Schema:            RiverSchema,
		Queues:            map[string]river.QueueConfig{queue: {MaxWorkers: e.opts.Concurrency}},
		Workers:           workers,
		PeriodicJobs:      periodic,
		FetchPollInterval: e.opts.PollInterval,
		FetchCooldown:     min(river.FetchCooldownDefault, e.opts.PollInterval),
		PollOnly:          e.opts.ListenURL == "",
		JobTimeout:        e.opts.StepTimeout,
		// Detach running steps from Run's ctx: cancelling it starts a soft stop
		// (steps finish and settle) that escalates after StopTimeout.
		SoftStopTimeout: e.opts.StopTimeout,
		Logger:          e.opts.Logger.With("component", "river"),
	})
	if err != nil {
		e.Close()
		return nil, errx.Wrap(err, "river client", errx.TypeInternal)
	}
	e.river = client
	return e, nil
}

// Run executes steps until ctx is done, then lets running steps finish (up
// to Options.StopTimeout; the rest are retried by the next run).
func (e *Engine) Run(ctx context.Context) error {
	if err := e.river.Start(ctx); err != nil {
		return errx.Wrap(err, "start workflow engine", errx.TypeInternal)
	}
	<-ctx.Done()
	stop, cancel := context.WithTimeout(context.Background(), e.opts.StopTimeout+5*time.Second)
	defer cancel()
	if err := e.river.Stop(stop); err != nil {
		return e.river.StopAndCancel(context.Background())
	}
	return nil
}

// Close releases the listener connection (after Run returned).
func (e *Engine) Close() {
	if e.listener != nil {
		e.listener.Close()
	}
}

// ---- jobs ----

type stepArgs struct {
	Run uuid.UUID `json:"run"`
	Rev int64     `json:"rev"`
}

func (stepArgs) Kind() string { return "workflow.step" }

type scheduleArgs struct {
	Name string `json:"name"`
}

func (scheduleArgs) Kind() string { return "workflow.schedule" }

// schedule enqueues the job that moves run to revision rev after delay, in
// the transaction in ctx. A delay > 0 is honoured by River's scheduler, which
// promotes due jobs every ~5 s: waits are precise to a few seconds.
func (e *Engine) schedule(ctx context.Context, run uuid.UUID, rev int64, delay time.Duration) error {
	tx, ok := txn.Tx(ctx)
	if !ok {
		return errx.Internal("workflow: schedule outside a transaction")
	}
	opts := &river.InsertOpts{Queue: queue, MaxAttempts: 25}
	if delay > 0 {
		opts.ScheduledAt = time.Now().Add(delay)
	}
	if _, err := e.river.InsertTx(ctx, tx.Tx, stepArgs{Run: run, Rev: rev}, opts); err != nil {
		return errx.Wrap(err, "schedule workflow step", errx.TypeInternal)
	}
	return nil
}

// ---- Engine API ----

func (e *Engine) Start(ctx context.Context, s workflow.Start) (workflow.RunID, error) {
	if err := s.Validate(); err != nil {
		return workflow.RunID{}, err
	}
	def, ok := e.registry.Definition(s.Workflow)
	if !ok {
		return workflow.RunID{}, errx.Internal("workflow: unknown workflow " + s.Workflow)
	}
	if _, ok := txn.Tx(ctx); !ok {
		return workflow.RunID{}, errx.Internal("workflow: start outside a transaction")
	}
	input, err := json.Marshal(s.Input)
	if err != nil {
		return workflow.RunID{}, errx.Wrap(err, "workflow: marshal input", errx.TypeInternal)
	}
	correlation := event.MetaFrom(ctx).CorrelationID
	id := workflow.NewRunID()
	if correlation == "" {
		correlation = id.String()
	}
	q := txn.Q(ctx, e.db)
	var got workflow.RunID
	err = q.GetContext(ctx, &got, `INSERT INTO workflow.runs
		(id, organization_id, workflow, subject, status, step_name, input, started_by, correlation_id, idempotency_key)
		VALUES ($1, $2, $3, $4, 'running', $5, $6, $7, $8, $9)
		ON CONFLICT (organization_id, workflow, idempotency_key) WHERE idempotency_key <> '' DO NOTHING
		RETURNING id`,
		id, s.Organization, s.Workflow, s.Subject, def.Steps[0].Name, input, s.Actor, correlation, s.IdempotencyKey)
	if errors.Is(err, sql.ErrNoRows) { // the key was used: return that run
		err = q.GetContext(ctx, &got, `SELECT id FROM workflow.runs
			WHERE organization_id = $1 AND workflow = $2 AND idempotency_key = $3`, s.Organization, s.Workflow, s.IdempotencyKey)
		return got, pgerr.Map(err, "find workflow run")
	}
	if err != nil {
		return workflow.RunID{}, pgerr.Map(err, "start workflow run")
	}
	return id, e.schedule(ctx, id.UUID(), 1, 0)
}

func (e *Engine) Signal(ctx context.Context, org identity.OrganizationID, run workflow.RunID, signal string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return errx.Wrap(err, "workflow: marshal signal", errx.TypeInternal)
	}
	return txn.Postgres{DB: e.db}.Do(ctx, func(ctx context.Context) error {
		r, err := e.lock(ctx, run.UUID())
		if err != nil {
			return err
		}
		if r.Organization != org {
			return errx.NotFound("workflow run not found")
		}
		if r.Status.Finished() {
			return errx.Conflict("the workflow run already finished").WithCode("WORKFLOW_FINISHED")
		}
		if _, err := txn.Q(ctx, e.db).ExecContext(ctx,
			`INSERT INTO workflow.signals (run_id, name, payload) VALUES ($1, $2, $3)`, run, signal, raw); err != nil {
			return pgerr.Map(err, "store workflow signal")
		}
		if r.Status == workflow.StatusWaiting && r.WaitingFor == signal {
			// Wake it: the worker consumes one signal and moves on. Further
			// signals of the same name stay pending for a later WaitFor.
			return e.bump(ctx, r, 0)
		}
		return nil
	})
}

func (e *Engine) Cancel(ctx context.Context, org identity.OrganizationID, run workflow.RunID, reason string) error {
	return txn.Postgres{DB: e.db}.Do(ctx, func(ctx context.Context) error {
		r, err := e.lock(ctx, run.UUID())
		if err != nil {
			return err
		}
		if r.Organization != org {
			return errx.NotFound("workflow run not found")
		}
		if r.Status.Finished() {
			return nil
		}
		_, err = txn.Q(ctx, e.db).ExecContext(ctx, `UPDATE workflow.runs SET status = 'cancelled', error = $2,
			rev = rev + 1, waiting_for = '', wait_until = NULL, finished_at = now(), updated_at = now() WHERE id = $1`, r.ID, reason)
		return pgerr.Map(err, "cancel workflow run")
	})
}

func (e *Engine) Find(ctx context.Context, org identity.OrganizationID, run workflow.RunID) (workflow.Run, error) {
	var r runRow
	err := txn.Q(ctx, e.db).GetContext(ctx, &r, `SELECT `+runColumns+` FROM workflow.runs WHERE organization_id = $1 AND id = $2`, org, run)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Run{}, errx.NotFound("workflow run not found")
	}
	if err != nil {
		return workflow.Run{}, pgerr.Map(err, "find workflow run")
	}
	return r.run()
}

// ---- run state ----

const runColumns = `id, organization_id, workflow, subject, status, rev, step, step_name, attempt, step_started_at,
	input, outputs, error, waiting_for, wait_until, started_by, correlation_id, created_at, updated_at, finished_at`

type runRow struct {
	workflow.Run
	Rev           int64     `db:"rev"`
	StepStartedAt time.Time `db:"step_started_at"`
	OutputsRaw    []byte    `db:"outputs"`
	InputRaw      []byte    `db:"input"`
}

func (r runRow) run() (workflow.Run, error) {
	out := r.Run
	out.Input = r.InputRaw
	if err := json.Unmarshal(r.OutputsRaw, &out.Outputs); err != nil {
		return workflow.Run{}, errx.Wrap(err, "decode workflow outputs", errx.TypeInternal)
	}
	return out, nil
}

func (e *Engine) lock(ctx context.Context, id uuid.UUID) (runRow, error) {
	var r runRow
	err := txn.Q(ctx, e.db).GetContext(ctx, &r, `SELECT `+runColumns+` FROM workflow.runs WHERE id = $1 FOR UPDATE`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return runRow{}, errx.NotFound("workflow run not found")
	}
	return r, pgerr.Map(err, "lock workflow run")
}

// bump moves the run to its next revision (same step) and schedules it after delay.
func (e *Engine) bump(ctx context.Context, r runRow, delay time.Duration) error {
	if _, err := txn.Q(ctx, e.db).ExecContext(ctx,
		`UPDATE workflow.runs SET rev = rev + 1, updated_at = now() WHERE id = $1`, r.ID); err != nil {
		return pgerr.Map(err, "bump workflow run")
	}
	return e.schedule(ctx, r.ID.UUID(), r.Rev+1, delay)
}

// ---- execution ----

type stepWorker struct {
	river.WorkerDefaults[stepArgs]
	e *Engine
}

func (w *stepWorker) Work(ctx context.Context, job *river.Job[stepArgs]) error {
	return w.e.work(ctx, job.Args)
}

type scheduleWorker struct {
	river.WorkerDefaults[scheduleArgs]
	e *Engine
}

func (w *scheduleWorker) Work(ctx context.Context, job *river.Job[scheduleArgs]) error {
	for _, s := range w.e.registry.Schedules() {
		if s.Name == job.Args.Name {
			return s.Run(ctx)
		}
	}
	return river.JobCancel(errx.Internal("unknown schedule " + job.Args.Name))
}

// errStale ends a job whose revision is no longer current.
var errStale = errors.New("stale")

func (e *Engine) work(ctx context.Context, args stepArgs) error {
	tx := txn.Postgres{DB: e.db}
	// 1. read the run; settle what needs no step execution
	var (
		r       runRow
		def     workflow.Definition
		step    workflow.Step
		execute bool
	)
	err := tx.Do(ctx, func(ctx context.Context) error {
		var err error
		if r, err = e.lock(ctx, args.Run); err != nil {
			return err
		}
		if r.Rev != args.Rev || r.Status.Finished() {
			return errStale
		}
		var ok bool
		if def, ok = e.registry.Definition(r.Workflow); !ok || r.Step >= len(def.Steps) {
			if time.Since(r.UpdatedAt) < time.Hour {
				return river.JobSnooze(time.Minute) // a newer replica may know it (rolling deploy)
			}
			return e.fail(ctx, r, def, errx.Internal("workflow "+r.Workflow+" is not registered in this version"))
		}
		step = def.Steps[r.Step]
		if r.Attempt >= maxAttempts(step) { // the step already failed; only OnFailure is pending
			return e.fail(ctx, r, def, errors.New(r.Error))
		}
		if r.Status == workflow.StatusWaiting {
			return e.resume(ctx, r, def)
		}
		if step.Timeout > 0 && time.Since(r.StepStartedAt) > step.Timeout {
			return e.fail(ctx, r, def, errx.Internal("step "+step.Name+" timed out"))
		}
		execute = true
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	if err != nil || !execute {
		return err
	}

	// 2. run the step outside any transaction (it may take a while and call
	// other systems; services it calls open their own transactions)
	run, err := r.run()
	if err != nil {
		return err
	}
	state := &workflow.State{Run: run, Attempt: r.Attempt + 1}
	stepCtx := event.WithMeta(ctx, event.Meta{CorrelationID: r.Correlation})
	output, stepErr := e.execute(stepCtx, step, state)

	// 3. record the outcome, unless the run moved on meanwhile (cancel)
	err = tx.Do(ctx, func(ctx context.Context) error {
		cur, err := e.lock(ctx, args.Run)
		if err != nil {
			return err
		}
		if cur.Rev != args.Rev || cur.Status != workflow.StatusRunning {
			return errStale
		}
		return e.settle(ctx, cur, def, step, output, stepErr)
	})
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}

func (e *Engine) execute(ctx context.Context, step workflow.Step, s *workflow.State) (out any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errx.Internal(fmt.Sprintf("step %s panicked: %v", step.Name, p))
		}
	}()
	return step.Run(ctx, s)
}

// settle applies a step's outcome (in the transaction holding the run).
func (e *Engine) settle(ctx context.Context, r runRow, def workflow.Definition, step workflow.Step, output any, stepErr error) error {
	if stepErr == nil {
		return e.advance(ctx, r, def, output, 0)
	}
	c, ok := workflow.AsControl(stepErr)
	if !ok {
		attempts, limit := r.Attempt+1, maxAttempts(step)
		if attempts >= limit {
			return e.fail(ctx, r, def, stepErr)
		}
		e.opts.Logger.Warn("workflow step failed", "run", r.ID, "workflow", r.Workflow, "step", step.Name,
			"attempt", attempts, "error", stepErr)
		if _, err := txn.Q(ctx, e.db).ExecContext(ctx, `UPDATE workflow.runs SET attempt = $2, error = $3,
			updated_at = now() WHERE id = $1`, r.ID, attempts, truncate(stepErr.Error())); err != nil {
			return pgerr.Map(err, "record workflow step failure")
		}
		return e.bump(ctx, r, e.opts.Backoff(attempts))
	}
	switch c.Kind {
	case workflow.ControlPoll:
		return e.bump(ctx, r, c.Delay)
	case workflow.ControlAfter:
		return e.advance(ctx, r, def, c.Output, c.Delay)
	case workflow.ControlWait:
		consumed, payload, err := e.consume(ctx, r, c.Signal)
		if err != nil || consumed {
			if err != nil {
				return err
			}
			return e.advance(ctx, r, def, payload, 0)
		}
		// wait_until is on the database clock, as is River's scheduling, so a
		// worker with a skewed clock cannot strand the run.
		var timeout any
		if c.Delay > 0 {
			timeout = c.Delay.Microseconds()
		}
		if _, err := txn.Q(ctx, e.db).ExecContext(ctx, `UPDATE workflow.runs SET status = 'waiting', waiting_for = $2,
			wait_until = now() + make_interval(secs => $3::bigint / 1e6), rev = rev + 1, updated_at = now() WHERE id = $1`,
			r.ID, c.Signal, timeout); err != nil {
			return pgerr.Map(err, "park workflow run")
		}
		if timeout != nil { // the timeout check; a signal bumps the revision first
			return e.schedule(ctx, r.ID.UUID(), r.Rev+1, c.Delay)
		}
		return nil
	default:
		return e.fail(ctx, r, def, stepErr)
	}
}

// resume handles a waiting run's job: its signal arrived, or time ran out.
func (e *Engine) resume(ctx context.Context, r runRow, def workflow.Definition) error {
	consumed, payload, err := e.consume(ctx, r, r.WaitingFor)
	if err != nil {
		return err
	}
	if consumed {
		return e.advance(ctx, r, def, payload, 0)
	}
	if r.WaitUntil == nil {
		return nil // woken without a signal (it was consumed elsewhere): keep waiting
	}
	// Compare on the database clock; if the timeout is not due yet (the job
	// ran early), check again then instead of dropping the timeout.
	var left time.Duration
	if err := txn.Q(ctx, e.db).GetContext(ctx, &left,
		`SELECT GREATEST(0, (EXTRACT(EPOCH FROM ($1::timestamptz - now())) * 1e9))::bigint`, *r.WaitUntil); err != nil {
		return pgerr.Map(err, "check workflow timeout")
	}
	if left > 0 {
		return e.bump(ctx, r, left)
	}
	return e.fail(ctx, r, def, errx.Internal("timed out waiting for "+r.WaitingFor))
}

// consume takes the oldest pending signal named name.
func (e *Engine) consume(ctx context.Context, r runRow, name string) (bool, json.RawMessage, error) {
	var payload []byte
	err := txn.Q(ctx, e.db).GetContext(ctx, &payload, `UPDATE workflow.signals SET consumed_at = now()
		WHERE id = (SELECT id FROM workflow.signals WHERE run_id = $1 AND name = $2 AND consumed_at IS NULL
		            ORDER BY id LIMIT 1 FOR UPDATE)
		RETURNING payload`, r.ID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, pgerr.Map(err, "consume workflow signal")
	}
	return true, payload, nil
}

// advance saves the step's output and moves to the next step (after delay)
// or finishes the run.
func (e *Engine) advance(ctx context.Context, r runRow, def workflow.Definition, output any, delay time.Duration) error {
	raw, ok := output.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(output); err != nil {
			return e.fail(ctx, r, def, errx.Wrap(err, "marshal output of "+r.StepName, errx.TypeInternal))
		}
	}
	q := txn.Q(ctx, e.db)
	next := r.Step + 1
	if next == len(def.Steps) {
		_, err := q.ExecContext(ctx, `UPDATE workflow.runs SET status = 'succeeded', outputs = outputs || jsonb_build_object(step_name, $2::jsonb),
			rev = rev + 1, error = '', waiting_for = '', wait_until = NULL, finished_at = now(), updated_at = now() WHERE id = $1`, r.ID, []byte(raw))
		return pgerr.Map(err, "finish workflow run")
	}
	if _, err := q.ExecContext(ctx, `UPDATE workflow.runs SET status = 'running', outputs = outputs || jsonb_build_object(step_name, $2::jsonb),
		step = $3, step_name = $4, attempt = 0, error = '', waiting_for = '', wait_until = NULL, step_started_at = now(),
		rev = rev + 1, updated_at = now() WHERE id = $1`, r.ID, []byte(raw), next, def.Steps[next].Name); err != nil {
		return pgerr.Map(err, "advance workflow run")
	}
	return e.schedule(ctx, r.ID.UUID(), r.Rev+1, delay)
}

// fail marks the run failed and runs the definition's OnFailure in the same
// transaction. An OnFailure error rolls both back and River retries the job
// (re-running the failed step's settlement, never a finished step), so
// OnFailure must be idempotent like any step.
func (e *Engine) fail(ctx context.Context, r runRow, def workflow.Definition, cause error) error {
	q := txn.Q(ctx, e.db)
	if def.OnFailure != nil {
		if _, err := q.ExecContext(ctx, `SAVEPOINT workflow_fail`); err != nil {
			return pgerr.Map(err, "workflow savepoint")
		}
	}
	if _, err := q.ExecContext(ctx, `UPDATE workflow.runs SET status = 'failed', error = $2, rev = rev + 1,
		waiting_for = '', wait_until = NULL, finished_at = now(), updated_at = now() WHERE id = $1`, r.ID, truncate(cause.Error())); err != nil {
		return pgerr.Map(err, "fail workflow run")
	}
	if def.OnFailure == nil {
		e.opts.Logger.Error("workflow run failed", "run", r.ID, "workflow", r.Workflow, "step", r.StepName, "error", cause)
		return nil
	}
	run, err := r.run()
	if err != nil {
		return err
	}
	hookErr := def.OnFailure(event.WithMeta(ctx, event.Meta{CorrelationID: r.Correlation}),
		&workflow.State{Run: run, Attempt: r.Attempt + 1}, cause)
	if hookErr == nil {
		e.opts.Logger.Error("workflow run failed", "run", r.ID, "workflow", r.Workflow, "step", r.StepName, "error", cause)
		return nil
	}
	// Undo the marking and whatever OnFailure wrote, but remember that the step
	// is done failing (attempt = its limit, error = the cause): the retry runs
	// only OnFailure again, never the step.
	if _, err := q.ExecContext(ctx, `ROLLBACK TO SAVEPOINT workflow_fail`); err != nil {
		return pgerr.Map(err, "workflow rollback to savepoint")
	}
	failures := r.Attempt + 1
	if r.Step < len(def.Steps) {
		failures = max(failures, maxAttempts(def.Steps[r.Step]))
	}
	e.opts.Logger.Error("workflow OnFailure failed; retrying it", "run", r.ID, "workflow", r.Workflow, "error", hookErr, "cause", cause)
	if _, err := q.ExecContext(ctx, `UPDATE workflow.runs SET attempt = $2, error = $3, updated_at = now() WHERE id = $1`,
		r.ID, failures, truncate(cause.Error())); err != nil {
		return pgerr.Map(err, "record workflow failure")
	}
	return e.bump(ctx, r, e.opts.Backoff(failures))
}

func maxAttempts(s workflow.Step) int {
	if s.MaxAttempts <= 0 {
		return 5
	}
	return s.MaxAttempts
}

func truncate(s string) string {
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}
