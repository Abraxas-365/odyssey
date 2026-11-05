// Package approvalmodule assembles the approval domain: repository, service,
// HTTP handler, the adapters that give each governed domain its own
// Approvals port, and what happens when a request is decided (a waiting
// workflow is signalled, a held action is run again). Only the composition
// root imports it.
package approvalmodule

import (
	"context"
	"io/fs"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalai"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalhttp"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalpg"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalrelease"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalresource"
	"github.com/Abraxas-365/odyssey/internal/approval/approvalsvc"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/jmoiron/sqlx"
)

// The module's subscriptions in the outbox.
const (
	// SignalRuns tells the workflow run waiting for a request what became of
	// it (a deployment's authorize step).
	SignalRuns = "approval.signal_runs"
	// ExecuteApproved runs an approved direct action again, as the requester.
	ExecuteApproved = "approval.execute_approved"
)

// ExpireEvery is how often the schedule expires the requests nobody decided.
const ExpireEvery = 5 * time.Minute

type Deps struct {
	DB     *sqlx.DB
	Events event.Publisher
	// Engine signals the runs waiting for a decision (workflow.Deferred).
	Engine workflow.Engine
	// The governed domains' commands: an approved direct action runs again
	// through them, as the requester.
	Release  release.Commands
	Resource resource.Commands
	AI       ai.Commands
}

type Module struct {
	Commands approval.Commands
	Queries  approval.Queries
	HTTP     *approvalhttp.Handler

	// What each governed domain holds and redeems through (one port per consumer).
	ReleaseApprovals  release.Approvals
	ResourceApprovals resource.Approvals
	AIApprovals       ai.Approvals

	engine    workflow.Engine
	executors []approval.Executor
}

func New(deps Deps) Module {
	svc := approvalsvc.New(approvalpg.New(deps.DB), txn.Postgres{DB: deps.DB}, deps.Events)
	return Module{
		Commands: svc, Queries: svc,
		HTTP:              approvalhttp.New(svc, svc),
		ReleaseApprovals:  approvalrelease.New(svc),
		ResourceApprovals: approvalresource.New(svc),
		AIApprovals:       approvalai.New(svc),
		engine:            deps.Engine,
		executors: []approval.Executor{
			approvalrelease.NewExecutor(deps.Release),
			approvalresource.NewExecutor(deps.Resource),
			approvalai.NewExecutor(deps.AI),
		},
	}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = approvalpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return approvalpg.Migrations() }

// Subscribe wires who the approval domain reacts to. Only the composition
// root calls it, before the bus is sealed.
func (m Module) Subscribe(bus *event.Bus) {
	bus.Add(event.Subscription{Name: SignalRuns, Handle: m.signalRun,
		Types: []string{approval.EventApproved, approval.EventRejected, approval.EventExpired, approval.EventCancelled}})
	bus.Add(event.Subscription{Name: ExecuteApproved, Handle: m.execute, Types: []string{approval.EventApproved}})
}

// Register adds the schedule that expires the requests nobody decided (a
// waiting deployment is cancelled through the signal that expiry publishes).
func (m Module) Register(r *workflow.Registry) {
	r.Every(workflow.Schedule{Name: "approval.expire", Every: ExpireEvery, Run: func(ctx context.Context) error {
		n, err := m.Commands.Expire(ctx)
		if n > 0 {
			slog.Info("expired approval requests nobody decided", "count", n)
		}
		return err
	}})
}

// signalRun is the handler of approval.{approved,rejected,expired,cancelled}:
// a request tied to a workflow run tells it the decision, which is the output
// of the step that waits.
func (m Module) signalRun(ctx context.Context, e event.Event) error {
	if e.Version != 1 {
		return nil
	}
	p, err := event.Decode[approval.Decided](e)
	if err != nil {
		return err
	}
	if p.Run == "" {
		return nil
	}
	run, err := workflow.ParseRunID(p.Run)
	if err != nil {
		return nil // not a run of ours: nothing waits for it
	}
	status := map[string]string{
		approval.EventApproved:  gate.DecisionApproved,
		approval.EventRejected:  gate.DecisionRejected,
		approval.EventExpired:   gate.DecisionExpired,
		approval.EventCancelled: gate.DecisionCancelled,
	}[e.Type]
	err = m.engine.Signal(ctx, e.Organization, run, gate.SignalApproval,
		gate.Decision{Request: p.Approval, Status: status, Note: p.Note})
	switch {
	case err == nil:
		return nil
	case errx.IsType(err, errx.TypeNotFound):
		return nil // the run is gone
	}
	var x *errx.Error
	if errx.As(err, &x) && x.Code == "WORKFLOW_FINISHED" {
		return nil // already ended (a newer deployment replaced it)
	}
	return err
}

// execute is the handler of approval.approved for the direct actions (those
// with no run): it runs the action again as the requester, with the actor
// they had when they asked and the approval's mark, so the governed service
// redeems the approval instead of holding the action again. A failure is
// recorded on the request and published, not retried: the approval is spent
// or the action no longer makes sense, and the requester can ask again.
func (m Module) execute(ctx context.Context, e event.Event) error {
	if e.Version != 1 {
		return nil
	}
	p, err := event.Decode[approval.Decided](e)
	if err != nil {
		return err
	}
	if p.Run != "" {
		return nil // a deployment: its workflow carries on by itself
	}
	id, err := approval.ParseID(p.Approval)
	if err != nil {
		return err
	}
	r, err := m.Queries.Get(ctx, e.Organization, id)
	if err != nil {
		return err
	}
	if r.ConsumedAt != nil || r.ExecutionError != "" || r.Status != approval.StatusApproved {
		return nil // delivered twice: it already ran or already failed
	}
	var ex approval.Executor
	for _, x := range m.executors {
		if x.Handles(r) {
			ex = x
			break
		}
	}
	if ex == nil {
		return m.Commands.Fail(ctx, e.Organization, id, "Esta aprobación no corresponde a ninguna acción conocida.")
	}
	as := actor.Actor{User: r.RequestedBy, System: r.RequestedSystem, Permissions: r.RequesterPermissions}
	run := gate.WithApproval(actor.With(ctx, as), r.ID.String())
	if err := txn.Attempt(run, func(ctx context.Context) error { return ex.Execute(ctx, r) }); err != nil {
		slog.Warn("approved action could not run", "approval", r.ID, "action", r.Action, "error", err)
		return m.Commands.Fail(ctx, e.Organization, id, words(err))
	}
	// The action ran. Normally it redeemed the approval on the way; when it
	// did not (a rule changed meanwhile and now allows it, or the action was a
	// no-op) the approval is used up here, so an approved request never stays
	// unconsumed. Consume is idempotent: already used is nothing to do.
	return m.Commands.Consume(ctx, e.Organization, id)
}

// words says why the action failed in the requester's language: the service's
// own message when it has one, a plain fallback otherwise (internals stay in
// the log).
func words(err error) string {
	var x *errx.Error
	if errx.As(err, &x) && x.Type != errx.TypeInternal && x.Message != "" {
		return x.Message
	}
	return "No se pudo hacer lo aprobado. Pídelo de nuevo."
}
