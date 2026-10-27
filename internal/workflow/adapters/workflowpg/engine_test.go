package workflowpg_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"github.com/jmoiron/sqlx"
)

type input struct {
	Name string `json:"name"`
}

type harness struct {
	db  *sqlx.DB
	eng *workflowpg.Engine
	org identity.OrganizationID
}

// start runs the engine for the test with registry already filled.
func start(t *testing.T, reg *workflow.Registry) harness {
	t.Helper()
	db := pgtest.DB(t, migrate.Set{Owner: workflowpg.Schema, Files: workflowpg.Migrations()})
	ctx := context.Background()
	if err := workflowpg.MigrateRiver(ctx, db); err != nil {
		t.Fatal(err)
	}
	eng, err := workflowpg.New(ctx, db, reg, workflowpg.Options{
		PollInterval: 20 * time.Millisecond,
		Backoff:      func(int) time.Duration { return 0 },
		Logger:       slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- eng.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("engine stop: %v", err)
		}
		eng.Close()
	})
	return harness{db: db, eng: eng, org: identity.NewOrganizationID()}
}

func (h harness) start(t *testing.T, s workflow.Start) workflow.RunID {
	t.Helper()
	s.Organization = h.org
	var id workflow.RunID
	err := txn.Postgres{DB: h.db}.Do(context.Background(), func(ctx context.Context) error {
		var err error
		id, err = h.eng.Start(ctx, s)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// await polls until the run reaches want (or any finished state).
func (h harness) await(t *testing.T, id workflow.RunID, want workflow.Status) workflow.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second) // delayed steps wait for River's 5 s scheduler
	for {
		r, err := h.eng.Find(context.Background(), h.org, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == want || (r.Status.Finished() && want.Finished()) {
			if r.Status != want {
				t.Fatalf("run ended %s (want %s): %s", r.Status, want, r.Error)
			}
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run still %s at step %s (want %s): %s", r.Status, r.StepName, want, r.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStepsRunInOrderPassOutputsAndRetry(t *testing.T) {
	reg := workflow.NewRegistry()
	var flaky atomic.Int32
	reg.Define(workflow.Definition{Name: "demo.greet", Steps: []workflow.Step{
		{Name: "build", Run: func(_ context.Context, s *workflow.State) (any, error) {
			var in input
			if err := s.Input(&in); err != nil {
				return nil, err
			}
			return "hola " + in.Name, nil
		}},
		{Name: "flaky", Run: func(_ context.Context, s *workflow.State) (any, error) {
			if flaky.Add(1) < 3 {
				return nil, errors.New("temporary")
			}
			return s.Attempt, nil
		}},
		{Name: "finish", Run: func(_ context.Context, s *workflow.State) (any, error) {
			var greeting string
			if err := s.Output("build", &greeting); err != nil {
				return nil, err
			}
			return greeting + " (" + s.Key() + ")", nil
		}},
	}})
	h := start(t, reg)
	id := h.start(t, workflow.Start{Workflow: "demo.greet", Subject: "demo/1", Input: input{Name: "Ana"}})
	r := h.await(t, id, workflow.StatusSucceeded)
	if string(r.Outputs["flaky"]) != "3" || string(r.Outputs["finish"]) != `"hola Ana (`+id.String()+`/finish)"` {
		t.Fatalf("outputs %s / %s", r.Outputs["flaky"], r.Outputs["finish"])
	}
	if r.FinishedAt == nil || r.Error != "" || r.Subject != "demo/1" {
		t.Fatalf("run %+v", r)
	}
}

func TestFailureRunsOnFailureOnce(t *testing.T) {
	reg := workflow.NewRegistry()
	var compensated atomic.Int32
	var mu sync.Mutex
	var cause error
	reg.Define(workflow.Definition{Name: "demo.fails",
		Steps: []workflow.Step{{Name: "boom", MaxAttempts: 2, Run: func(context.Context, *workflow.State) (any, error) {
			return nil, errors.New("cluster unreachable")
		}}},
		OnFailure: func(_ context.Context, _ *workflow.State, err error) error {
			compensated.Add(1)
			mu.Lock()
			cause = err
			mu.Unlock()
			return nil
		},
	})
	reg.Define(workflow.Definition{Name: "demo.permanent",
		Steps: []workflow.Step{{Name: "bad_input", Run: func(context.Context, *workflow.State) (any, error) {
			return nil, workflow.Fail(errors.New("image not found"))
		}}},
	})
	h := start(t, reg)
	r := h.await(t, h.start(t, workflow.Start{Workflow: "demo.fails"}), workflow.StatusFailed)
	if r.Error != "cluster unreachable" || compensated.Load() != 1 {
		t.Fatalf("error %q, compensations %d, cause %v", r.Error, compensated.Load(), cause)
	}
	r = h.await(t, h.start(t, workflow.Start{Workflow: "demo.permanent"}), workflow.StatusFailed)
	if r.Error != "image not found" || r.Attempt != 0 {
		t.Fatalf("permanent failure retried: %+v", r)
	}
}

// A failing OnFailure is retried on its own: the step that failed never runs
// again, and what OnFailure wrote before failing is rolled back.
func TestOnFailureErrorRetriesOnlyTheHook(t *testing.T) {
	reg := workflow.NewRegistry()
	var steps, hooks atomic.Int32
	reg.Define(workflow.Definition{Name: "demo.flaky_hook",
		Steps: []workflow.Step{{Name: "boom", MaxAttempts: 2, Run: func(context.Context, *workflow.State) (any, error) {
			steps.Add(1)
			return nil, errors.New("cluster unreachable")
		}}},
		OnFailure: func(ctx context.Context, s *workflow.State, err error) error {
			if _, xerr := txn.Q(ctx, nil).ExecContext(ctx, `CREATE TABLE IF NOT EXISTS public.compensations (n int)`); xerr != nil {
				return xerr
			}
			if _, xerr := txn.Q(ctx, nil).ExecContext(ctx, `INSERT INTO public.compensations VALUES (1)`); xerr != nil {
				return xerr
			}
			if hooks.Add(1) < 3 {
				return errors.New("notification service down")
			}
			if err.Error() != "cluster unreachable" {
				return workflow.Fail(errors.New("wrong cause " + err.Error()))
			}
			return nil
		},
	})
	h := start(t, reg)
	r := h.await(t, h.start(t, workflow.Start{Workflow: "demo.flaky_hook"}), workflow.StatusFailed)
	if steps.Load() != 2 || hooks.Load() != 3 || r.Error != "cluster unreachable" {
		t.Fatalf("steps %d hooks %d error %q", steps.Load(), hooks.Load(), r.Error)
	}
	var n int
	if err := h.db.Get(&n, `SELECT count(*) FROM public.compensations`); err != nil || n != 1 {
		t.Fatalf("compensations %d (%v): failed hooks must roll back their writes", n, err)
	}
}

func TestSignalsWaitsAndTimeouts(t *testing.T) {
	reg := workflow.NewRegistry()
	reg.Define(workflow.Definition{Name: "demo.approval", Steps: []workflow.Step{
		{Name: "await_approval", Run: func(context.Context, *workflow.State) (any, error) {
			return nil, workflow.WaitFor("approved", time.Minute)
		}},
		{Name: "apply", Run: func(_ context.Context, s *workflow.State) (any, error) {
			var by string
			if err := s.Output("await_approval", &by); err != nil {
				return nil, err
			}
			return "aprobado por " + by, nil
		}},
	}})
	await := func(context.Context, *workflow.State) (any, error) {
		return nil, workflow.WaitFor("approved", time.Minute)
	}
	reg.Define(workflow.Definition{Name: "demo.twice", Steps: []workflow.Step{
		{Name: "first", Run: await}, {Name: "second", Run: await},
	}})
	reg.Define(workflow.Definition{Name: "demo.impatient", Steps: []workflow.Step{
		{Name: "await", Run: func(context.Context, *workflow.State) (any, error) {
			return nil, workflow.WaitFor("never", 100*time.Millisecond)
		}},
	}})
	var polls atomic.Int32
	reg.Define(workflow.Definition{Name: "demo.poll", Steps: []workflow.Step{
		{Name: "until_ready", Run: func(context.Context, *workflow.State) (any, error) {
			switch polls.Add(1) {
			case 1:
				return nil, workflow.Poll(50 * time.Millisecond) // through River's scheduler
			case 2, 3:
				return nil, workflow.Poll(0)
			}
			return "ready", nil
		}},
	}})
	h := start(t, reg)
	ctx := context.Background()

	// late signal: the run parks, then the signal wakes it
	late := h.start(t, workflow.Start{Workflow: "demo.approval"})
	h.await(t, late, workflow.StatusWaiting)
	if err := h.eng.Signal(ctx, h.org, late, "approved", "maria"); err != nil {
		t.Fatal(err)
	}
	if r := h.await(t, late, workflow.StatusSucceeded); string(r.Outputs["apply"]) != `"aprobado por maria"` {
		t.Fatalf("outputs %s", r.Outputs["apply"])
	}

	// early signal: sent in the same transaction as the start, consumed later
	var early workflow.RunID
	err := txn.Postgres{DB: h.db}.Do(ctx, func(ctx context.Context) error {
		var err error
		if early, err = h.eng.Start(ctx, workflow.Start{Workflow: "demo.approval", Organization: h.org}); err != nil {
			return err
		}
		return h.eng.Signal(ctx, h.org, early, "approved", "jorge")
	})
	if err != nil {
		t.Fatal(err)
	}
	h.await(t, early, workflow.StatusSucceeded)

	// two signals while waiting: one advances the run, the other stays
	// pending for the next wait of the same name
	twice := h.start(t, workflow.Start{Workflow: "demo.twice"})
	h.await(t, twice, workflow.StatusWaiting)
	for _, who := range []string{"ana", "luis"} {
		if err := h.eng.Signal(ctx, h.org, twice, "approved", who); err != nil {
			t.Fatal(err)
		}
	}
	if r := h.await(t, twice, workflow.StatusSucceeded); string(r.Outputs["first"]) != `"ana"` || string(r.Outputs["second"]) != `"luis"` {
		t.Fatalf("outputs %s", r.Outputs)
	}

	// other organizations cannot see, signal or cancel a run
	other := identity.NewOrganizationID()
	if err := h.eng.Signal(ctx, other, early, "approved", "x"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("signal from another organization: %v", err)
	}
	if err := h.eng.Cancel(ctx, other, early, "x"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cancel from another organization: %v", err)
	}
	if _, err := h.eng.Find(ctx, other, early); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("find from another organization: %v", err)
	}
	if err := h.eng.Signal(ctx, h.org, early, "approved", "x"); err == nil {
		t.Fatal("signal to a finished run")
	}

	if r := h.await(t, h.start(t, workflow.Start{Workflow: "demo.impatient"}), workflow.StatusFailed); r.Error == "" {
		t.Fatal("timeout without error")
	}
	h.await(t, h.start(t, workflow.Start{Workflow: "demo.poll"}), workflow.StatusSucceeded)
	if polls.Load() != 4 {
		t.Fatalf("polls %d", polls.Load())
	}
}

func TestStartIsTransactionalIdempotentAndCancellable(t *testing.T) {
	reg := workflow.NewRegistry()
	var ran atomic.Int32
	reg.Define(workflow.Definition{Name: "demo.once", Steps: []workflow.Step{
		{Name: "count", Run: func(context.Context, *workflow.State) (any, error) {
			ran.Add(1)
			return nil, nil
		}},
	}})
	reg.Define(workflow.Definition{Name: "demo.blocked", Steps: []workflow.Step{
		{Name: "await", Run: func(context.Context, *workflow.State) (any, error) {
			return nil, workflow.WaitFor("go", 0)
		}},
	}})
	h := start(t, reg)
	ctx := context.Background()

	if _, err := h.eng.Start(ctx, workflow.Start{Workflow: "demo.once", Organization: h.org}); err == nil {
		t.Fatal("start outside a transaction must fail")
	}
	// a rolled-back write leaves no run and runs nothing
	boom := errors.New("write failed")
	err := txn.Postgres{DB: h.db}.Do(ctx, func(ctx context.Context) error {
		if _, err := h.eng.Start(ctx, workflow.Start{Workflow: "demo.once", Organization: h.org}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	a := h.start(t, workflow.Start{Workflow: "demo.once", IdempotencyKey: "release-1"})
	b := h.start(t, workflow.Start{Workflow: "demo.once", IdempotencyKey: "release-1"})
	if a != b {
		t.Fatal("same idempotency key started two runs")
	}
	h.await(t, a, workflow.StatusSucceeded)
	time.Sleep(100 * time.Millisecond)
	if ran.Load() != 1 {
		t.Fatalf("ran %d times", ran.Load())
	}
	var runs int
	_ = h.db.Get(&runs, `SELECT count(*) FROM workflow.runs`)
	if runs != 1 {
		t.Fatalf("%d runs stored (the rolled-back one must not exist)", runs)
	}

	blocked := h.start(t, workflow.Start{Workflow: "demo.blocked"})
	h.await(t, blocked, workflow.StatusWaiting)
	if err := h.eng.Cancel(ctx, h.org, blocked, "ya no se necesita"); err != nil {
		t.Fatal(err)
	}
	if r := h.await(t, blocked, workflow.StatusCancelled); r.Error != "ya no se necesita" {
		t.Fatalf("cancelled %+v", r)
	}
}

func TestRegistryRejectsMistakes(t *testing.T) {
	step := workflow.Step{Name: "a", Run: func(context.Context, *workflow.State) (any, error) { return nil, nil }}
	cases := map[string]func(r *workflow.Registry){
		"bad name": func(r *workflow.Registry) { r.Define(workflow.Definition{Name: "Bad", Steps: []workflow.Step{step}}) },
		"no steps": func(r *workflow.Registry) { r.Define(workflow.Definition{Name: "x.y"}) },
		"dup step": func(r *workflow.Registry) {
			r.Define(workflow.Definition{Name: "x.y", Steps: []workflow.Step{step, step}})
		},
		"nil step": func(r *workflow.Registry) {
			r.Define(workflow.Definition{Name: "x.y", Steps: []workflow.Step{{Name: "a"}}})
		},
		"dup define": func(r *workflow.Registry) {
			d := workflow.Definition{Name: "x.y", Steps: []workflow.Step{step}}
			r.Define(d)
			r.Define(d)
		},
		"sealed": func(r *workflow.Registry) {
			r.Seal()
			r.Define(workflow.Definition{Name: "x.y", Steps: []workflow.Step{step}})
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			fn(workflow.NewRegistry())
		})
	}
}
