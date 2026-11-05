package aisvc

import (
	"context"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

// Workflows are ai.enable, ai.rotate and ai.disable (see package ai). The
// key's clear text exists only inside create_key, between the gateway's answer
// and the sealing; what the run saves is the sealed bytes and the gateway's id.
// Every step is safe to run twice:
//
//   - create_key revokes any key of the same deterministic name first, so a
//     crash between the gateway making the key and the run saving it leaves no
//     orphan (the retry removes it and makes another);
//   - apply is one transaction (cluster delivery, settings, activation), and
//     asking for what is already in force changes nothing;
//   - revoking a revoked key is a success.
func (s *Service) Workflows() []workflow.Definition {
	return []workflow.Definition{
		{Name: ai.WorkflowEnable, OnFailure: s.makeFailed, Steps: []workflow.Step{
			{Name: ai.StepCreateKey, Run: s.createKey, MaxAttempts: s.opts.Attempts},
			{Name: ai.StepApply, Run: s.apply, MaxAttempts: s.opts.Attempts},
		}},
		{Name: ai.WorkflowRotate, OnFailure: s.makeFailed, Steps: []workflow.Step{
			{Name: ai.StepCreateKey, Run: s.createKey, MaxAttempts: s.opts.Attempts},
			{Name: ai.StepApply, Run: s.apply, MaxAttempts: s.opts.Attempts},
			{Name: ai.StepSettle, Run: s.settle, Timeout: s.opts.SettleTimeout, MaxAttempts: 30},
			{Name: ai.StepRevokeOld, Run: s.revokeOld, MaxAttempts: s.opts.Attempts},
		}},
		{Name: ai.WorkflowDisable, OnFailure: s.revokeFailed, Steps: []workflow.Step{
			{Name: ai.StepSettle, Run: s.settle, Timeout: s.opts.SettleTimeout, MaxAttempts: 30},
			{Name: ai.StepRevoke, Run: s.revokeAll, MaxAttempts: s.opts.Attempts},
		}},
	}
}

// made is what create_key saves: nothing secret.
type made struct {
	// ProviderKeyID is how the gateway names the key (for revoking it).
	ProviderKeyID string `json:"provider_key_id"`
	// Version and ClusterKeyID say what the sealed bytes are for.
	Version      int64  `json:"version"`
	ClusterKeyID string `json:"cluster_key_id"`
	Sealed       []byte `json:"sealed"`
}

// applied is what apply saves for settle.
type applied struct {
	Revision int64 `json:"revision"`
}

func (s *Service) input(st *workflow.State) (ai.Input, error) {
	var in ai.Input
	if err := st.Input(&in); err != nil {
		return ai.Input{}, workflow.Fail(err)
	}
	if s.provider == nil {
		return ai.Input{}, workflow.Fail(ai.ErrUnavailable())
	}
	return in, nil
}

// fail turns a refusal retrying cannot fix into a failed run; anything else
// (the gateway or the database unreachable) is retried.
func fail(err error) error {
	if permanent(err) {
		return workflow.Fail(err)
	}
	return err
}

func (s *Service) createKey(ctx context.Context, st *workflow.State) (any, error) {
	in, err := s.input(st)
	if err != nil {
		return nil, err
	}
	org := st.Run.Organization
	k, err := s.repo.Find(ctx, org, in.Environment, in.Key)
	if err != nil {
		return nil, workflow.Fail(err) // the row is gone: a disable took over
	}
	if k.Status != ai.StatusCreating {
		return nil, workflow.Fail(errx.Conflict("la llave ya no se está creando"))
	}
	t, err := s.release.Target(ctx, org, in.Project, in.Environment)
	if err != nil {
		return nil, fail(err)
	}
	if refusal := s.refusal(t); refusal != nil {
		return nil, workflow.Fail(refusal)
	}
	if err := s.revokeNamed(ctx, k.Name); err != nil { // a leftover of an earlier attempt
		return nil, err
	}
	pk, err := s.provider.CreateKey(ctx, k.Name)
	if err != nil {
		return nil, err
	}
	sealed, err := secretbox.Seal(t.Key.Public, secretbox.Target{Cluster: t.Key.Cluster, Environment: in.Environment.String(),
		App: t.Key.App, Name: ai.SecretName, Version: t.NextVersion}, []byte(pk.Secret))
	if err != nil {
		// The clear key dies here; take the key back so nothing is left alive.
		_ = s.provider.RevokeKey(ctx, pk.ID)
		return nil, workflow.Fail(errx.Internal("the AI key could not be sealed to the cluster"))
	}
	return made{ProviderKeyID: pk.ID, Version: t.NextVersion, ClusterKeyID: t.Key.KeyID, Sealed: sealed}, nil
}

// revokeNamed revokes every live key of the gateway with exactly this name.
func (s *Service) revokeNamed(ctx context.Context, name string) error {
	return s.revokeByPrefix(ctx, name, nil)
}

// revokeByPrefix revokes the live keys whose name starts with prefix, except
// those whose gateway id is in keep.
func (s *Service) revokeByPrefix(ctx context.Context, prefix string, keep map[string]bool) error {
	infos, err := s.provider.ListKeys(ctx, prefix)
	if err != nil {
		return err
	}
	for _, i := range infos {
		if keep[i.ID] {
			continue
		}
		if err := s.provider.RevokeKey(ctx, i.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) apply(ctx context.Context, st *workflow.State) (any, error) {
	in, err := s.input(st)
	if err != nil {
		return nil, err
	}
	var m made
	if err := st.Output(ai.StepCreateKey, &m); err != nil {
		return nil, workflow.Fail(err)
	}
	org := st.Run.Organization
	p, e, err := s.place(ctx, org, in.Project, in.Environment)
	if err != nil {
		return nil, fail(err)
	}
	by := st.Run.StartedBy
	var out applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		k, err := s.repo.Find(ctx, org, e.ID, in.Key)
		if err != nil {
			return err
		}
		// Archived apps keep their AI but take no new keys; the key is taken back
		// below. The project is read again here, under the lock: archiving may
		// have happened since the step started.
		p, err = s.projects.Find(ctx, org, in.Project)
		if err != nil {
			return err
		}
		if p.Archived() {
			return project.ArchivedError()
		}
		a, err := s.release.Enable(ctx, org, in.Project, e.ID, ai.Delivery{Version: m.Version, KeyID: m.ClusterKeyID,
			BaseURL: s.opts.BaseURL, Sealed: m.Sealed, By: by})
		if err != nil {
			return err
		}
		changed, replaced, err := s.repo.Activate(ctx, org, e.ID, in.Key, m.ProviderKeyID)
		if err != nil {
			return err
		}
		out = applied{Revision: a.Revision}
		if !changed {
			return nil
		}
		if replaced {
			return s.events.Publish(ctx, ai.NewRotated(org, by, k, m.Version, names(p, e)))
		}
		return s.events.Publish(ctx, ai.NewEnabled(org, by, k, m.Version, names(p, e)))
	})
	if err != nil {
		return nil, fail(err)
	}
	return out, nil
}

// settle waits until the app runs with the revision (or nothing is online).
func (s *Service) settle(ctx context.Context, st *workflow.State) (any, error) {
	in, err := s.input(st)
	if err != nil {
		return nil, err
	}
	revision := in.Revision
	if in.Gone {
		return nil, nil // no app is left that could run with the keys
	}
	if revision == 0 {
		var a applied
		if err := st.Output(ai.StepApply, &a); err != nil {
			return nil, workflow.Fail(err)
		}
		revision = a.Revision
	}
	ok, err := s.release.Settled(ctx, st.Run.Organization, in.Project, in.Environment, revision)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, workflow.Poll(s.opts.PollEvery)
	}
	return nil, nil
}

// revokeOld revokes the keys a rotation replaced and drops their rows. The
// active key is never touched: it is found by its id, not by its name.
func (s *Service) revokeOld(ctx context.Context, st *workflow.State) (any, error) {
	in, err := s.input(st)
	if err != nil {
		return nil, err
	}
	return nil, s.revokeOldRows(ctx, st.Run.Organization, in)
}

func (s *Service) revokeOldRows(ctx context.Context, org identity.OrganizationID, in ai.Input) error {
	keys, err := s.repo.List(ctx, org, in.Environment)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Status != ai.StatusRevoking {
			continue
		}
		if err := s.revokeRow(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// revokeAll takes back the keys the disable marked revoking, each by its
// gateway id (or by its name, for one whose id was never saved), and drops
// their rows. It never reaches for the environment's name prefix: a key made
// after the disable (the app turned the AI on again) is another row and stays.
func (s *Service) revokeAll(ctx context.Context, st *workflow.State) (any, error) {
	in, err := s.input(st)
	if err != nil {
		return nil, err
	}
	keys, err := s.repo.List(ctx, st.Run.Organization, in.Environment)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.Status != ai.StatusRevoking || !slices.Contains(in.Keys, k.ID) {
			continue // not one this disable marked (or already handled)
		}
		if err := s.revokeRow(ctx, k); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// revokeRow revokes a stored key at the gateway (by id, and by name for one
// whose id was never saved) and drops its row.
func (s *Service) revokeRow(ctx context.Context, k ai.Key) error {
	if k.ProviderKeyID != nil {
		if err := s.provider.RevokeKey(ctx, *k.ProviderKeyID); err != nil {
			return err
		}
	} else if err := s.revokeNamed(ctx, k.Name); err != nil {
		return err
	}
	_, err := s.repo.Delete(ctx, k.Organization, k.Environment, k.ID)
	return err
}

// revokeFailed runs when ai.disable gives up (the gateway unreachable for
// good, not connected, or the app never settled): the keys it was revoking are
// recorded as failed, so the console shows it with a retry and the sweep tries
// again. Nothing is left "revoking" forever.
func (s *Service) revokeFailed(ctx context.Context, st *workflow.State, cause error) error {
	var in ai.Input
	if err := st.Input(&in); err != nil {
		return nil
	}
	org := st.Run.Organization
	if err := s.repo.LockEnvironment(ctx, org, in.Environment); err != nil {
		return err
	}
	reason := ai.FailureReason(cause)
	if s.provider == nil {
		reason = ai.ReasonNoGateway
	}
	keys, err := s.repo.FailRevocation(ctx, org, in.Environment, in.Keys, reason)
	if err != nil || len(keys) == 0 {
		return err
	}
	if !firstFailure(keys) {
		return nil // a retry of the sweep that failed again: already reported
	}
	return s.publishRevokeFailed(ctx, org, st.Run.StartedBy, in, reason)
}

// publishRevokeFailed publishes ai.revoke_failed. An environment that no longer
// exists is still reported (with no names): its keys are the platform's to
// revoke and IT should know when that cannot be done.
func (s *Service) publishRevokeFailed(ctx context.Context, org identity.OrganizationID, by identity.UserID, in ai.Input, reason string) error {
	var n ai.Names
	p, e, err := s.place(ctx, org, in.Project, in.Environment)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
	case err != nil:
		return err
	default:
		n = names(p, e)
	}
	return s.events.Publish(ctx, ai.NewRevokeFailed(org, by, in.Project.String(), in.Environment.String(), reason, n))
}

// makeFailed runs when enable or rotate gives up. A key that never took over
// is taken back from the gateway and, while it is still creating, its row is
// dropped, so a failed attempt leaves nothing (a row a disable already marked
// revoking is the disable's to drop). A key that took over stays: the app
// uses it. A rotation that gave up waiting also revokes the old key: the new
// one is already delivered and in the settings. When the gateway cannot be
// reached (or is not connected) the key it could not take back is recorded as
// revoke_failed, never left behind silently: the sweep retries it.
func (s *Service) makeFailed(ctx context.Context, st *workflow.State, cause error) error {
	var in ai.Input
	if err := st.Input(&in); err != nil {
		return nil
	}
	org := st.Run.Organization
	if err := s.repo.LockEnvironment(ctx, org, in.Environment); err != nil {
		return err
	}
	k, err := s.repo.Find(ctx, org, in.Environment, in.Key)
	notFound := errx.IsType(err, errx.TypeNotFound)
	if err != nil && !notFound {
		return err
	}
	if s.provider == nil {
		if notFound {
			return nil
		}
		return s.giveUp(ctx, st, in, ai.ReasonNoGateway)
	}
	if notFound { // a disable dropped the row; the key may be at the gateway still
		return s.revokeNamed(ctx, ai.KeyName(in.Environment, in.Key))
	}
	var gateway error
	switch k.Status {
	case ai.StatusActive:
		gateway = s.revokeOldRows(ctx, org, in)
	case ai.StatusCreating, ai.StatusRevoking:
		if gateway = s.revokeNamed(ctx, k.Name); gateway == nil && k.Status == ai.StatusCreating {
			_, gateway = s.repo.DeleteCreating(ctx, org, in.Environment, in.Key)
		}
	}
	if gateway == nil {
		return nil
	}
	if !errx.IsType(gateway, errx.TypeExternal) {
		return gateway // the database, not the gateway: retry
	}
	return s.giveUp(ctx, st, in, ai.FailureReason(gateway))
}

// giveUp records that the gateway could not be told to revoke: the run's own
// key (when it is creating or revoking) and the keys a rotation retired.
func (s *Service) giveUp(ctx context.Context, st *workflow.State, in ai.Input, reason string) error {
	org := st.Run.Organization
	failedKey, _, err := s.repo.FailKey(ctx, org, in.Environment, in.Key, reason)
	if err != nil {
		return err
	}
	keys, err := s.repo.List(ctx, org, in.Environment)
	if err != nil {
		return err
	}
	var retired []ai.KeyID
	for _, k := range keys {
		if k.Status == ai.StatusRevoking {
			retired = append(retired, k.ID)
		}
	}
	failedKeys, err := s.repo.FailRevocation(ctx, org, in.Environment, retired, reason)
	if err != nil {
		return err
	}
	if !firstFailure(append(failedKeys, failedKey)) {
		return nil
	}
	return s.publishRevokeFailed(ctx, org, st.Run.StartedBy, in, reason)
}
