// Package workflow is durable execution for long operations (deploy, create
// a database, promote): a workflow is a named list of steps whose progress
// survives restarts. The engine (adapters/workflowpg, River on Postgres)
// persists the run after every step, retries failed steps with backoff and
// parks runs that wait for a signal (an approval, a callback) or a deadline.
//
// Domains define workflows next to their aggregate (<domain>/workflows.go or
// in the service) and the composition root registers them. A service starts
// one inside its transaction, so the run exists if and only if the write
// that asked for it committed:
//
//	err := s.tx.Do(ctx, func(ctx context.Context) error {
//		if err := s.repo.Create(ctx, org, id, input); err != nil {
//			return err
//		}
//		_, err := s.workflows.Start(ctx, workflow.Start{Workflow: "release.deploy", Organization: org,
//			Actor: input.CreatedBy, Subject: event.Subject("release", id), Input: deployInput{Release: id},
//			IdempotencyKey: id.String()})
//		return err
//	})
//
// Steps run at least once: a crash after a step's effect and before its
// result is saved runs it again. Make every step idempotent; State.Key() is a
// stable idempotency key for calls to other systems.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

type runTag struct{}

func (runTag) IDName() string { return "run_id" }

// RunID identifies one execution of a workflow.
type RunID = identity.ID[runTag]

func NewRunID() RunID                      { return identity.NewID[runTag]() }
func ParseRunID(raw string) (RunID, error) { return identity.ParseID[runTag](raw) }

// Status is a run's lifecycle: running ⇄ waiting → succeeded | failed | cancelled.
type Status string

const (
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Finished reports whether the run can no longer change.
func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Run is the persisted state of one execution.
type Run struct {
	ID           RunID                      `json:"id" db:"id"`
	Organization identity.OrganizationID    `json:"organization_id" db:"organization_id"`
	Workflow     string                     `json:"workflow" db:"workflow"`
	Subject      string                     `json:"subject" db:"subject"`
	Status       Status                     `json:"status" db:"status"`
	Step         int                        `json:"step" db:"step"` // index of the current step
	StepName     string                     `json:"step_name" db:"step_name"`
	Attempt      int                        `json:"attempt" db:"attempt"` // failed attempts of the current step
	Input        json.RawMessage            `json:"input" db:"-"`
	Outputs      map[string]json.RawMessage `json:"outputs" db:"-"`
	Error        string                     `json:"error,omitempty" db:"error"`
	WaitingFor   string                     `json:"waiting_for,omitempty" db:"waiting_for"`
	WaitUntil    *time.Time                 `json:"wait_until,omitempty" db:"wait_until"`
	StartedBy    identity.UserID            `json:"started_by" db:"started_by"`
	Correlation  string                     `json:"correlation_id" db:"correlation_id"`
	CreatedAt    time.Time                  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time                  `json:"updated_at" db:"updated_at"`
	FinishedAt   *time.Time                 `json:"finished_at,omitempty" db:"finished_at"`
}

// Start asks for a new run.
type Start struct {
	Workflow     string
	Organization identity.OrganizationID
	// Actor is who asked for it (zero for the system).
	Actor identity.UserID
	// Subject is the entity the run works on ("release/<id>"), for listings.
	Subject string
	// Input is marshalled to JSON and handed to every step.
	Input any
	// IdempotencyKey, when set, makes Start return the existing run of the
	// same workflow and key in the organization instead of starting another.
	IdempotencyKey string
}

func (s Start) Validate() error {
	switch {
	case !namePattern.MatchString(s.Workflow):
		return errx.Internal(fmt.Sprintf("workflow: invalid name %q", s.Workflow))
	case s.Organization.IsZero():
		return errx.Internal("workflow: start without organization")
	case len(s.IdempotencyKey) > 200:
		return errx.Internal("workflow: idempotency key too long")
	}
	return nil
}

// Engine starts and steers runs. Start and Signal join the transaction in
// ctx (Start requires one): the run and its first job commit with the write.
type Engine interface {
	Start(ctx context.Context, start Start) (RunID, error)
	// Signal delivers a named signal to a run. A signal sent before the run
	// waits for it is kept and consumed when the run gets there.
	Signal(ctx context.Context, organization identity.OrganizationID, run RunID, signal string, payload any) error
	// Cancel stops a run that has not finished (its current step may still
	// complete, but nothing after it runs).
	Cancel(ctx context.Context, organization identity.OrganizationID, run RunID, reason string) error
	Find(ctx context.Context, organization identity.OrganizationID, run RunID) (Run, error)
}

// Step is one unit of work. Its output (JSON) is saved and readable by the
// following steps through State.Output.
type Step struct {
	Name string
	Run  func(ctx context.Context, s *State) (any, error)
	// MaxAttempts before the run fails (default 5). Attempts are spaced by
	// exponential backoff.
	MaxAttempts int
	// Timeout bounds the step's total time across attempts and polls (zero:
	// unbounded); past it the run fails.
	Timeout time.Duration
}

// Definition is a workflow: its steps in order and what to do when it fails.
type Definition struct {
	Name  string
	Steps []Step
	// OnFailure runs in the transaction that marks the run failed (after
	// retries are exhausted, Fail, a timeout or an unknown step). Use it to
	// record the domain fact ("deployment failed") and compensate (start a
	// rollback). An error rolls the marking back and the engine retries.
	OnFailure func(ctx context.Context, s *State, cause error) error
}

// State is what a step sees of its run.
type State struct {
	Run     Run
	Attempt int // 1 on the first attempt of this step
}

// Input decodes the run's input.
func (s *State) Input(into any) error { return decode(s.Run.Input, into, "input") }

// Output decodes the saved output of an earlier step (or the payload of the
// signal it waited for). It fails when that step saved nothing.
func (s *State) Output(step string, into any) error {
	raw, ok := s.Run.Outputs[step]
	if !ok {
		return errx.Internal("workflow: no output from step " + step)
	}
	return decode(raw, into, "output of "+step)
}

// Key is a stable idempotency key for the current step ("<run>/<step>"):
// pass it to other systems so a re-run step does not act twice.
func (s *State) Key() string { return s.Run.ID.String() + "/" + s.Run.StepName }

func decode(raw json.RawMessage, into any, what string) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return errx.Wrap(err, "workflow: decode "+what, errx.TypeInternal)
	}
	return nil
}

// Control is returned by a step (as its error) to steer the run instead of
// completing or failing the step.
type Control struct {
	Kind    ControlKind
	Signal  string        // WaitFor
	Delay   time.Duration // Poll, After, WaitFor (timeout)
	Output  any           // After
	Failure error         // Fail
}

type ControlKind int

const (
	// ControlWait parks the run until Signal arrives (or Delay passes: the run fails).
	ControlWait ControlKind = iota + 1
	// ControlPoll runs the same step again after Delay, without counting an attempt.
	ControlPoll
	// ControlAfter completes the step with Output and starts the next one after Delay.
	ControlAfter
	// ControlFail fails the run now, without retries.
	ControlFail
)

func (c *Control) Error() string {
	switch c.Kind {
	case ControlWait:
		return "workflow: wait for " + c.Signal
	case ControlPoll:
		return "workflow: poll"
	case ControlAfter:
		return "workflow: continue after delay"
	default:
		if c.Failure != nil {
			return c.Failure.Error()
		}
		return "workflow: failed"
	}
}

// Unwrap exposes a Fail's error, so OnFailure can inspect the cause with
// errors.Is / errors.As.
func (c *Control) Unwrap() error { return c.Failure }

// WaitFor parks the run until signal arrives; its payload becomes the
// step's output. With timeout > 0 the run fails if it does not arrive in time.
func WaitFor(signal string, timeout time.Duration) error {
	return &Control{Kind: ControlWait, Signal: signal, Delay: timeout}
}

// Poll runs the step again after d (e.g. "until the deployment is Ready");
// bound the total wait with Step.Timeout.
func Poll(d time.Duration) error { return &Control{Kind: ControlPoll, Delay: d} }

// After completes the step with output and starts the next one after d.
func After(d time.Duration, output any) error {
	return &Control{Kind: ControlAfter, Delay: d, Output: output}
}

// Fail fails the run without retrying the step (the error is permanent).
func Fail(err error) error { return &Control{Kind: ControlFail, Failure: err} }

// AsControl extracts a Control from a step's error.
func AsControl(err error) (*Control, bool) {
	var c *Control
	ok := errors.As(err, &c)
	return c, ok
}

// Schedule is a recurring maintenance task (purge old events, reconcile).
// Exactly one replica runs each tick.
type Schedule struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Registry holds the workflows and schedules the engine can run. The
// composition root fills it at startup.
type Registry struct {
	mu        sync.RWMutex
	defs      map[string]Definition
	schedules map[string]Schedule
	sealed    bool
}

func NewRegistry() *Registry {
	return &Registry{defs: map[string]Definition{}, schedules: map[string]Schedule{}}
}

// Define registers d. It panics on a programming error: after Seal, an
// invalid or duplicated name, no steps, a duplicated step or a nil step.
func (r *Registry) Define(d Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mustOpen(d.Name)
	if _, dup := r.defs[d.Name]; dup {
		panic("workflow: duplicate workflow " + d.Name)
	}
	if len(d.Steps) == 0 {
		panic("workflow: " + d.Name + " has no steps")
	}
	seen := map[string]bool{}
	for _, s := range d.Steps {
		if !namePattern.MatchString(s.Name) || seen[s.Name] || s.Run == nil {
			panic(fmt.Sprintf("workflow: %s: invalid, duplicated or empty step %q", d.Name, s.Name))
		}
		seen[s.Name] = true
	}
	r.defs[d.Name] = d
}

// Every registers a recurring task.
func (r *Registry) Every(s Schedule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mustOpen(s.Name)
	if _, dup := r.schedules[s.Name]; dup || s.Every <= 0 || s.Run == nil {
		panic("workflow: duplicate or invalid schedule " + s.Name)
	}
	r.schedules[s.Name] = s
}

func (r *Registry) mustOpen(name string) {
	if r.sealed {
		panic("workflow: " + name + " registered after Seal")
	}
	if !namePattern.MatchString(name) {
		panic(fmt.Sprintf("workflow: invalid name %q", name))
	}
}

// Seal forbids further registrations.
func (r *Registry) Seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

// Definition returns the registered workflow named name.
func (r *Registry) Definition(name string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[name]
	return d, ok
}

// Schedules lists the recurring tasks.
func (r *Registry) Schedules() []Schedule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Schedule, 0, len(r.schedules))
	for _, s := range r.schedules {
		out = append(out, s)
	}
	return out
}

// Deferred is an Engine filled in after construction. A module that both
// defines workflows and starts them is built before the engine (which needs
// its definitions); the composition root hands it a Deferred and Sets the
// engine once built. Using it before Set is a wiring bug.
type Deferred struct {
	mu sync.RWMutex
	e  Engine
}

var _ Engine = (*Deferred)(nil)

// Set installs the engine; it may be called once.
func (d *Deferred) Set(e Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.e != nil {
		panic("workflow: Deferred engine set twice")
	}
	d.e = e
}

func (d *Deferred) engine() (Engine, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.e == nil {
		return nil, errx.Internal("workflow: engine used before it was set")
	}
	return d.e, nil
}

func (d *Deferred) Start(ctx context.Context, s Start) (RunID, error) {
	e, err := d.engine()
	if err != nil {
		return RunID{}, err
	}
	return e.Start(ctx, s)
}

func (d *Deferred) Signal(ctx context.Context, org identity.OrganizationID, run RunID, signal string, payload any) error {
	e, err := d.engine()
	if err != nil {
		return err
	}
	return e.Signal(ctx, org, run, signal, payload)
}

func (d *Deferred) Cancel(ctx context.Context, org identity.OrganizationID, run RunID, reason string) error {
	e, err := d.engine()
	if err != nil {
		return err
	}
	return e.Cancel(ctx, org, run, reason)
}

func (d *Deferred) Find(ctx context.Context, org identity.OrganizationID, run RunID) (Run, error) {
	e, err := d.engine()
	if err != nil {
		return Run{}, err
	}
	return e.Find(ctx, org, run)
}
