package bootstrap_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestWorkerRunsWorkflows is phase 0's exit criterion for workflows: a run
// started by one process (the API's Platform, which never works) is executed
// by another (the worker's, through bootstrap.Work), across a wait for a
// signal, with each step's output handed to the next.
func TestWorkerRunsWorkflows(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	type greeting struct{ Name string }
	register := func(r *workflow.Registry) {
		r.Define(workflow.Definition{Name: "test.greet", Steps: []workflow.Step{
			{Name: "compose", Run: func(_ context.Context, s *workflow.State) (any, error) {
				var in greeting
				if err := s.Input(&in); err != nil {
					return nil, err
				}
				return "Hola, " + in.Name, nil
			}},
			{Name: "approve", Run: func(context.Context, *workflow.State) (any, error) {
				return nil, workflow.WaitFor("approved", time.Minute)
			}},
			{Name: "send", Run: func(_ context.Context, s *workflow.State) (any, error) {
				var text string
				if err := s.Output("compose", &text); err != nil {
					return nil, err
				}
				return text + "!", nil
			}},
		}})
	}
	opts := bootstrap.Options{Register: register, Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond}}
	api, err := bootstrap.Assemble(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	worker, err := bootstrap.Assemble(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })

	org := identity.NewOrganizationID()
	var id workflow.RunID
	// Start joins the caller's transaction, as a service does with its write.
	err = txn.Postgres{DB: db}.Do(ctx, func(ctx context.Context) (err error) {
		id, err = api.Workflows.Start(ctx, workflow.Start{Workflow: "test.greet", Organization: org, Input: greeting{Name: "Ñandú"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- bootstrap.Work(wctx, worker, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()

	await := func(want workflow.Status) workflow.Run {
		t.Helper()
		var run workflow.Run
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if run, err = api.Workflows.Find(ctx, org, id); err != nil {
				t.Fatal(err)
			}
			if run.Status == want {
				return run
			}
		}
		t.Fatalf("run never reached %s: %+v", want, run)
		return run
	}
	if run := await(workflow.StatusWaiting); run.WaitingFor != "approved" || run.StepName != "approve" {
		t.Fatalf("waiting run %+v", run)
	}
	if err := api.Workflows.Signal(ctx, org, id, "approved", nil); err != nil {
		t.Fatal(err)
	}
	if run := await(workflow.StatusSucceeded); string(run.Outputs["send"]) != `"Hola, Ñandú!"` {
		t.Fatalf("outputs %s", run.Outputs)
	}
}
