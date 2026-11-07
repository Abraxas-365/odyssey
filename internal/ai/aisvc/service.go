// Package aisvc implements ai.Commands and ai.Queries and the AI workflows.
//
// Locks are taken in one order so changes never deadlock: the environment's
// AI lock (ai.Repository.LockEnvironment), then the environment's settings
// (through ai.Release). The gateway (ai.Provider) is only called from
// workflow steps, never inside a request, and never holding a lock.
package aisvc

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Options tune the service.
type Options struct {
	// BaseURL is the gateway as apps reach it (without /v1).
	BaseURL string
	// PollEvery is how often a workflow asks whether the app runs with the
	// new revision (default 2 s).
	PollEvery time.Duration
	// SettleTimeout bounds that wait (default 30 min).
	SettleTimeout time.Duration
	// Attempts is how many times a gateway step is tried before the run
	// gives up and records the failure (default 8).
	Attempts int
}

type Service struct {
	repo     ai.Repository
	provider ai.Provider // nil: the gateway is not connected
	release  ai.Release
	projects project.Queries
	engine   workflow.Engine
	tx       txn.Runner
	events   event.Publisher
	opts     Options
	// policy is IT's rules (the policyai adapter); nil refuses turning the AI on.
	policy ai.Policy
	// approvals holds what a rule wants approved (the approvalai adapter);
	// nil fails an approval verdict closed.
	approvals ai.Approvals
}

// New builds the service. A nil provider means the organization has no AI:
// everything but reading answers AI_UNAVAILABLE.
func New(repo ai.Repository, provider ai.Provider, rel ai.Release, projects project.Queries, engine workflow.Engine,
	tx txn.Runner, events event.Publisher, policy ai.Policy, opts Options) *Service {
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.PollEvery <= 0 {
		opts.PollEvery = 2 * time.Second
	}
	if opts.Attempts <= 0 {
		opts.Attempts = 8
	}
	if opts.SettleTimeout <= 0 {
		opts.SettleTimeout = 30 * time.Minute
	}
	return &Service{repo: repo, provider: provider, release: rel, projects: projects, engine: engine, tx: tx, events: events, policy: policy, opts: opts}
}

// authorize is the one seam where ai asks IT's rules before turning the AI
// on: nil, nil lets it go on; a Pending means a rule wants IT's approval, the
// request was stored and nothing may change (the caller answers 202); an
// error is POLICY_DENIED (403). The approved action re-runs as the requester
// with the approval's mark on ctx and redeems it here (gate.Enforce).
func (s *Service) authorize(ctx context.Context, p project.Project, e project.Environment, request gate.Request, hold gate.Hold) (*gate.Pending, error) {
	request.Tier, request.ProjectSlug, request.Environment = string(p.Tier), p.Slug, e.Slug
	hold.Project, hold.Environment = p.ID.String(), e.ID.String()
	return gate.Enforce(ctx, p.Organization, s.policy, s.approvals, request, hold)
}

// UseApprovals wires where approvals are held (the approvalai adapter).
func (s *Service) UseApprovals(a ai.Approvals) { s.approvals = a }

var (
	_ ai.Commands  = (*Service)(nil)
	_ ai.Queries   = (*Service)(nil)
	_ ai.Workflows = (*Service)(nil)
)

// place resolves the project and environment of a request.
func (s *Service) place(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Project, project.Environment, error) {
	p, err := s.projects.Find(ctx, org, id)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	e, err := s.projects.FindEnvironment(ctx, org, id, env)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, e, nil
}

func names(p project.Project, e project.Environment) ai.Names {
	return ai.Names{Project: p.Name, Environment: e.Slug}
}

func (s *Service) Get(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (ai.View, error) {
	if _, _, err := s.place(ctx, org, id, env); err != nil {
		return ai.View{}, err
	}
	keys, err := s.repo.List(ctx, org, env)
	if err != nil {
		return ai.View{}, err
	}
	return s.view(ctx, org, id, env, keys)
}

// view says whether the AI can be turned on here, and how it stands.
func (s *Service) view(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, keys []ai.Key) (ai.View, error) {
	if s.provider == nil {
		return ai.NewView(keys, s.opts.BaseURL, false, ai.MessageOf(ai.ErrUnavailable())), nil
	}
	t, err := s.release.Target(ctx, org, id, env)
	if err != nil {
		return ai.View{}, err
	}
	var v ai.View
	switch refusal := s.refusal(t); {
	case refusal != nil:
		v = ai.NewView(keys, s.opts.BaseURL, false, ai.MessageOf(refusal))
	default:
		v = ai.NewView(keys, s.opts.BaseURL, true, "")
	}
	v.Services = append([]string{}, t.Services...)
	v.ScopedSettings = t.ScopedSettings
	return v, nil
}

// refusal is why the environment's cluster cannot run the AI (nil: it can).
func (s *Service) refusal(t ai.Target) error {
	switch {
	case !t.Capable:
		return ai.ErrClusterUnavailable()
	case t.Key == nil:
		return ai.ErrNoSecrets()
	}
	return nil
}

func (s *Service) Enable(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input ai.Enable) (ai.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return ai.View{}, err
	}
	if p.Archived() {
		return ai.View{}, project.ArchivedError()
	}
	if s.provider == nil {
		return ai.View{}, ai.ErrUnavailable()
	}
	// IT's rules judge only a CHANGE, and before the transaction (a refusal,
	// or in 3b the approval request it stores, must not live in a transaction
	// that rolls back on that same refusal). The AI already on (or on its way)
	// is a no-op and is not judged, unless the services asked are others:
	// that change is judged like turning it on. The keys are read here and
	// re-read under the lock: if the AI turned on in between, the
	// transaction finds it on and changes nothing, so no second judgment is
	// needed.
	current, err := s.repo.List(ctx, org, e.ID)
	if err != nil {
		return ai.View{}, err
	}
	has := statusesOf(current)
	switch {
	case has[ai.StatusCreating] && input.Services != nil:
		return ai.View{}, ai.ErrBusy() // the key on its way carries the services it was asked with
	case has[ai.StatusActive] || has[ai.StatusCreating]:
		if input.Services == nil {
			return s.view(ctx, org, id, e.ID, current)
		}
		t, err := s.release.Target(ctx, org, id, e.ID)
		if err != nil {
			return ai.View{}, err
		}
		if sameServices(t.Services, *input.Services) {
			return s.view(ctx, org, id, e.ID, current)
		}
		return s.scope(ctx, p, e, *input.Services, input.RequestedBy)
	}
	reached, err := s.release.Reach(ctx, org, id, e.ID, input.Services)
	if err != nil {
		return ai.View{}, err
	}
	pending, err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionEnableAI, Services: reached}, gate.Hold{
		Summary: "Activar la IA de «" + p.Name + "» (" + e.Slug + ")" + servicesLabel(input.Services),
		Payload: ai.EnableApproval{Op: ai.OpEnable, Services: input.Services}})
	if err != nil {
		return ai.View{}, err
	}
	if pending != nil { // held for IT: the AI stays off
		return ai.View{Outcome: gate.Holding(*pending)}, nil
	}
	var keys []ai.Key
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		if keys, err = s.repo.List(ctx, org, e.ID); err != nil {
			return err
		}
		switch has := statusesOf(keys); {
		case has[ai.StatusActive] || has[ai.StatusCreating]:
			return nil // on, or on its way: asking again changes nothing
		}
		// Keys still being revoked (or whose revocation failed) do not block
		// turning the AI on again: the new key is another row with another name,
		// and every revocation works on the ids it was started with, never on the
		// environment's name prefix, so none can reach it (docs/ai.md).
		t, err := s.release.Target(ctx, org, id, e.ID)
		if err != nil {
			return err
		}
		if refusal := s.refusal(t); refusal != nil {
			return refusal
		}
		// a variable the AI sets that the app defines, checked again under
		// the lock (release refuses it again when it applies)
		if _, err := s.release.Reach(ctx, org, id, e.ID, input.Services); err != nil {
			return err
		}
		return s.begin(ctx, p, e, input.RequestedBy, ai.WorkflowEnable, input.Services, &keys)
	})
	if err != nil {
		return ai.View{}, err
	}
	return s.view(ctx, org, id, e.ID, keys)
}

// scope moves the AI that is on to other services (same key), judged like
// turning it on for them.
func (s *Service) scope(ctx context.Context, p project.Project, e project.Environment, services []string, by identity.UserID) (ai.View, error) {
	reached, err := s.release.Reach(ctx, p.Organization, p.ID, e.ID, &services)
	if err != nil {
		return ai.View{}, err
	}
	pending, err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionEnableAI, Services: reached}, gate.Hold{
		Summary: "Dar la IA de «" + p.Name + "» (" + e.Slug + ")" + servicesLabel(&services),
		Payload: ai.EnableApproval{Op: ai.OpEnable, Services: &services}})
	if err != nil {
		return ai.View{}, err
	}
	if pending != nil {
		return ai.View{Outcome: gate.Holding(*pending)}, nil
	}
	var keys []ai.Key
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, p.Organization, e.ID); err != nil {
			return err
		}
		if keys, err = s.repo.List(ctx, p.Organization, e.ID); err != nil {
			return err
		}
		switch has := statusesOf(keys); {
		case has[ai.StatusCreating]:
			return ai.ErrBusy()
		case !has[ai.StatusActive]:
			return ai.ErrNotEnabled()
		}
		_, err := s.release.Scope(ctx, p.Organization, p.ID, e.ID, services, by)
		return err
	})
	if err != nil {
		return ai.View{}, err
	}
	return s.view(ctx, p.Organization, p.ID, e.ID, keys)
}

// sameServices says a and b name the same services (as sets; empty is the
// whole app).
func sameServices(a, b []string) bool {
	norm := func(s []string) []string { return slices.Compact(slices.Sorted(slices.Values(s))) }
	return slices.Equal(norm(a), norm(b))
}

// servicesLabel words the services asked for in a summary.
func servicesLabel(services *[]string) string {
	if services == nil || len(*services) == 0 {
		return ""
	}
	return " solo para " + strings.Join(*services, ", ")
}

func (s *Service) Rotate(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input ai.Rotate) (ai.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return ai.View{}, err
	}
	if p.Archived() {
		return ai.View{}, project.ArchivedError()
	}
	if s.provider == nil {
		return ai.View{}, ai.ErrUnavailable()
	}
	var keys []ai.Key
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		if keys, err = s.repo.List(ctx, org, e.ID); err != nil {
			return err
		}
		has := statusesOf(keys)
		switch {
		case has[ai.StatusCreating] || has[ai.StatusRevoking]:
			return ai.ErrBusy()
		case !has[ai.StatusActive]:
			return ai.ErrNotEnabled()
		}
		t, err := s.release.Target(ctx, org, id, e.ID)
		if err != nil {
			return err
		}
		if refusal := s.refusal(t); refusal != nil {
			return refusal
		}
		return s.begin(ctx, p, e, input.RequestedBy, ai.WorkflowRotate, nil, &keys)
	})
	if err != nil {
		return ai.View{}, err
	}
	return s.view(ctx, org, id, e.ID, keys)
}

// begin records the key being made and starts the workflow (the caller holds
// the environment's lock and the transaction).
func (s *Service) begin(ctx context.Context, p project.Project, e project.Environment, by identity.UserID, workflowName string, services *[]string, keys *[]ai.Key) error {
	k := ai.Key{ID: ai.NewKeyID(), Organization: p.Organization, Project: p.ID, Environment: e.ID, Status: ai.StatusCreating, CreatedBy: by}
	k.Name = ai.KeyName(e.ID, k.ID)
	if err := s.repo.Create(ctx, k); err != nil {
		return err
	}
	if _, err := s.engine.Start(ctx, workflow.Start{Workflow: workflowName, Organization: p.Organization, Actor: by,
		Subject: event.Subject(ai.SubjectKind, k.ID), Input: ai.Input{Key: k.ID, Project: p.ID, Environment: e.ID, Services: services},
		IdempotencyKey: k.ID.String()}); err != nil {
		return err
	}
	stored, err := s.repo.Find(ctx, p.Organization, e.ID, k.ID)
	if err != nil {
		return err
	}
	*keys = append(*keys, stored)
	return nil
}

func (s *Service) Disable(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input ai.Disable) (ai.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return ai.View{}, err
	}
	if p.Archived() {
		return ai.View{}, project.ArchivedError()
	}
	var keys []ai.Key
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		if keys, err = s.repo.List(ctx, org, e.ID); err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil // off already
		}
		// The app stops using the AI first, in the same transaction that marks
		// the keys: a key is never revoked while a deployment may still name it.
		applied, err := s.release.Disable(ctx, org, id, e.ID, input.RequestedBy)
		if err != nil {
			return err
		}
		if keys, err = s.repo.MarkRevoking(ctx, org, e.ID); err != nil {
			return err
		}
		if s.provider == nil {
			// The app is already without the AI; with no gateway connected there
			// is nobody to revoke the keys: they wait, marked, for IT (and for
			// the sweep, once a gateway is connected).
			if keys, err = s.repo.FailRevocation(ctx, org, e.ID, idsOf(keys), ai.ReasonNoGateway); err != nil {
				return err
			}
			if err := s.events.Publish(ctx, ai.NewRevokeFailed(org, input.RequestedBy, id.String(), e.ID.String(), ai.ReasonNoGateway, names(p, e))); err != nil {
				return err
			}
			return s.events.Publish(ctx, ai.NewDisabled(org, input.RequestedBy, id.String(), e.ID.String(), names(p, e)))
		}
		// Asking again while a revocation failed starts the workflow again: the
		// way out of it ("Reintentar"; revoking twice is harmless).
		if err := s.startRevocation(ctx, org, input.RequestedBy, id, e.ID, applied.Revision, idsOf(keys)); err != nil {
			return err
		}
		return s.events.Publish(ctx, ai.NewDisabled(org, input.RequestedBy, id.String(), e.ID.String(), names(p, e)))
	})
	if err != nil {
		return ai.View{}, err
	}
	return s.view(ctx, org, id, e.ID, keys)
}

// startRevocation starts ai.disable for the environment's revoking keys (the
// caller holds the environment's lock and the transaction). revision is the
// settings revision the app must run before a key is revoked.
func (s *Service) startRevocation(ctx context.Context, org identity.OrganizationID, by identity.UserID, id project.ID, env project.EnvironmentID, revision int64, keys []ai.KeyID) error {
	return s.startRevocationOf(ctx, org, by, id, env, revision, keys, false)
}

// startRevocationOf is startRevocation; gone says the environment no longer
// exists (Input.Gone: nothing to wait for).
func (s *Service) startRevocationOf(ctx context.Context, org identity.OrganizationID, by identity.UserID, id project.ID, env project.EnvironmentID, revision int64, keys []ai.KeyID, gone bool) error {
	run := ai.NewKeyID()
	_, err := s.engine.Start(ctx, workflow.Start{Workflow: ai.WorkflowDisable, Organization: org, Actor: by,
		Subject: event.Subject(ai.EnvironmentSubjectKind, env), Input: ai.Input{Key: run, Project: id, Environment: env, Revision: revision, Keys: keys, Gone: gone},
		IdempotencyKey: run.String()})
	return err
}

// sweepBatch bounds the environments one sweep retries.
const sweepBatch = 100

// Sweep retries the revocations that failed once the gateway is connected: it
// sets each environment's revoke_failed keys revoking again and starts the
// disable workflow for them (which waits for the app to run without them, as
// the first attempt did). It returns how many environments it retried.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	if s.provider == nil {
		return 0, nil
	}
	failed, err := s.repo.ListRevokeFailed(ctx, sweepBatch)
	if err != nil {
		return 0, err
	}
	retried := 0
	for _, k := range failed {
		again, err := s.retryRevocation(ctx, k)
		if err != nil {
			slog.Warn("could not retry the revocation of an AI key; the next sweep will", "environment", k.Environment, "error", err)
			continue
		}
		if again {
			retried++
		}
	}
	return retried, nil
}

// retryRevocation retries one environment within its organization. The app is
// not needed to revoke: the keys are the ones the rows name. When the project
// or environment is gone there is no app to wait for or to update, so the run
// only revokes them at the gateway (Input.Gone) and drops the rows. Any other
// error reading the environment leaves everything as it is for the next sweep.
// The status change is published (the system's event, no actor).
func (s *Service) retryRevocation(ctx context.Context, k ai.Key) (bool, error) {
	again := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, k.Organization, k.Environment); err != nil {
			return err
		}
		var (
			revision int64
			n        ai.Names
		)
		gone := false
		p, e, err := s.place(ctx, k.Organization, k.Project, k.Environment)
		switch {
		case errx.IsType(err, errx.TypeNotFound):
			gone = true
		case err != nil:
			return err
		default:
			n = names(p, e)
			t, err := s.release.Target(ctx, k.Organization, k.Project, k.Environment)
			switch {
			case errx.IsType(err, errx.TypeNotFound):
				gone, n = true, ai.Names{}
			case err != nil:
				return err
			default:
				revision = t.Revision
			}
		}
		keys, err := s.repo.RetryRevocation(ctx, k.Organization, k.Environment)
		if err != nil || len(keys) == 0 {
			return err
		}
		again = true
		if err := s.startRevocationOf(ctx, k.Organization, k.CreatedBy, k.Project, k.Environment, revision, idsOf(keys), gone); err != nil {
			return err
		}
		return s.events.Publish(ctx, ai.NewRevocationRetried(k.Organization, k.Project.String(), k.Environment.String(), attemptsOf(keys), n))
	})
	return again, err
}

// attemptsOf is how many revocations had failed for the keys that were taken
// up again (the most any of them had).
func attemptsOf(keys []ai.Key) int {
	most := 0
	for _, k := range keys {
		most = max(most, k.RevokeAttempts)
	}
	return most
}

// firstFailure says whether any of the keys just failed for the first time
// (since a person last asked): only that is published, so a sweep that keeps
// failing does not repeat the event every time.
func firstFailure(keys []ai.Key) bool {
	for _, k := range keys {
		if k.RevokeAttempts == 1 {
			return true
		}
	}
	return false
}

func statusesOf(keys []ai.Key) map[ai.Status]bool {
	out := map[ai.Status]bool{}
	for _, k := range keys {
		out[k.Status] = true
	}
	return out
}

// permanent says whether retrying cannot change the error's outcome.
func permanent(err error) bool {
	var e *errx.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Type {
	case errx.TypeBusiness, errx.TypeConflict, errx.TypeValidation, errx.TypeNotFound:
		return true
	}
	return false
}

func idsOf(keys []ai.Key) []ai.KeyID {
	out := make([]ai.KeyID, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ID)
	}
	return out
}
