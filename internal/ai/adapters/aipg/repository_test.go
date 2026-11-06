package aipg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aipg"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func errType(err error) errx.Type {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Type
	}
	return ""
}

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: aipg.Schema, Files: aipg.Migrations()})
	repo := aipg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	proj, env, env2 := project.NewID(), project.NewEnvironmentID(), project.NewEnvironmentID()
	mk := func(org identity.OrganizationID, env project.EnvironmentID, status ai.Status) ai.Key {
		k := ai.Key{ID: ai.NewKeyID(), Organization: org, Project: proj, Environment: env, Status: status, CreatedBy: user}
		k.Name = ai.KeyName(env, k.ID)
		return k
	}

	// Create / Find / List
	first := mk(org, env, ai.StatusCreating)
	if err := repo.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, first); errType(err) != errx.TypeConflict {
		t.Fatalf("duplicate id: %v", err)
	}
	got, err := repo.Find(ctx, org, env, first.ID)
	if err != nil || got.Name != first.Name || got.Status != ai.StatusCreating || got.ProviderKeyID != nil || got.CreatedAt.IsZero() {
		t.Fatalf("find %+v %v", got, err)
	}
	if bad := mk(org, env, "bogus"); errType(repo.Create(ctx, bad)) != errx.TypeValidation {
		t.Fatalf("bad status accepted")
	}
	if _, err := repo.Find(ctx, other, env, first.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("find across tenants: %v", err)
	}
	if _, err := repo.Find(ctx, org, env2, first.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("find across environments: %v", err)
	}

	// Activate: the creating key takes over; a retry changes nothing
	changed, replaced, err := repo.Activate(ctx, org, env, first.ID, "sa-1")
	if err != nil || !changed || replaced {
		t.Fatalf("activate: %v %v %v", changed, replaced, err)
	}
	if changed, _, err = repo.Activate(ctx, org, env, first.ID, "sa-1"); err != nil || changed {
		t.Fatalf("activate twice: %v %v", changed, err)
	}
	if got, _ := repo.Find(ctx, org, env, first.ID); got.Status != ai.StatusActive || got.ProviderKeyID == nil || *got.ProviderKeyID != "sa-1" {
		t.Fatalf("active %+v", got)
	}
	if _, _, err := repo.Activate(ctx, other, env, first.ID, "x"); errType(err) != errx.TypeNotFound {
		t.Fatalf("activate across tenants: %v", err)
	}

	// Rotation: the new key retires the old one; one active key per environment
	second := mk(org, env, ai.StatusCreating)
	if err := repo.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	changed, replaced, err = repo.Activate(ctx, org, env, second.ID, "sa-2")
	if err != nil || !changed || !replaced {
		t.Fatalf("rotate: %v %v %v", changed, replaced, err)
	}
	keys, err := repo.List(ctx, org, env)
	if err != nil || len(keys) != 2 || keys[0].ID != first.ID || keys[0].Status != ai.StatusRevoking || keys[1].Status != ai.StatusActive {
		t.Fatalf("list after rotation %+v %v", keys, err)
	}
	if _, _, err := repo.Activate(ctx, org, env, first.ID, "sa-1"); errType(err) != errx.TypeNotFound {
		t.Fatalf("a revoking key cannot be activated again: %v", err)
	}
	// the table refuses a second active key even around the repository
	_, err = db.ExecContext(ctx, `INSERT INTO ai.keys (id, organization_id, project_id, environment_id, name, status, created_by)
		VALUES ($1, $2, $3, $4, 'dup-active', 'active', $5)`, ai.NewKeyID(), org, proj, env, user)
	if err == nil {
		t.Fatal("two active keys in one environment")
	}

	// tenants and environments do not see each other
	if err := repo.Create(ctx, mk(other, env, ai.StatusCreating)); err != nil {
		t.Fatal(err)
	}
	foreign, _ := repo.List(ctx, other, env)
	mine, _ := repo.List(ctx, org, env)
	empty, _ := repo.List(ctx, org, env2)
	if len(foreign) != 1 || len(mine) != 2 || len(empty) != 0 {
		t.Fatalf("list isolation %d %d %d", len(foreign), len(mine), len(empty))
	}

	// DeleteCreating only removes a creating key, of the right tenant
	third := mk(org, env, ai.StatusCreating)
	if err := repo.Create(ctx, third); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.DeleteCreating(ctx, other, env, third.ID); err != nil || ok {
		t.Fatalf("delete creating across tenants: %v %v", ok, err)
	}
	if ok, err := repo.DeleteCreating(ctx, org, env, second.ID); err != nil || ok {
		t.Fatalf("an active key is not deleted as creating: %v %v", ok, err)
	}
	if ok, err := repo.DeleteCreating(ctx, org, env, third.ID); err != nil || !ok {
		t.Fatalf("delete creating: %v %v", ok, err)
	}
	if ok, _ := repo.DeleteCreating(ctx, org, env, third.ID); ok {
		t.Fatal("deleted twice")
	}

	// MarkRevoking takes every key of the environment, only of the environment
	marked, err := repo.MarkRevoking(ctx, org, env)
	if err != nil || len(marked) != 2 {
		t.Fatalf("mark revoking %+v %v", marked, err)
	}
	for _, k := range marked {
		if k.Status != ai.StatusRevoking {
			t.Errorf("not revoking: %+v", k)
		}
	}
	if foreign, _ = repo.List(ctx, other, env); len(foreign) != 1 || foreign[0].Status != ai.StatusCreating {
		t.Fatalf("another tenant's key was touched: %+v", foreign)
	}

	// Delete
	if ok, err := repo.Delete(ctx, other, env, first.ID); err != nil || ok {
		t.Fatalf("delete across tenants: %v %v", ok, err)
	}
	for _, k := range marked {
		if ok, err := repo.Delete(ctx, org, env, k.ID); err != nil || !ok {
			t.Fatalf("delete %v %v", ok, err)
		}
	}
	if ok, _ := repo.Delete(ctx, org, env, first.ID); ok {
		t.Fatal("deleted twice")
	}

	// LockEnvironment takes the lock inside a transaction without failing
	if err := repo.LockEnvironment(ctx, org, env); err != nil {
		t.Fatal(err)
	}
}

// A revocation that gave up is recorded, found again by the sweep (one key per
// environment, across tenants), and retried; only what was asked for moves.
func TestRevokeFailedLifecycle(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: aipg.Schema, Files: aipg.Migrations()})
	repo := aipg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	proj, env, env2 := project.NewID(), project.NewEnvironmentID(), project.NewEnvironmentID()
	mk := func(org identity.OrganizationID, env project.EnvironmentID, status ai.Status) ai.Key {
		k := ai.Key{ID: ai.NewKeyID(), Organization: org, Project: proj, Environment: env, Status: status, CreatedBy: user}
		k.Name = ai.KeyName(env, k.ID)
		if err := repo.Create(ctx, k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	a, b := mk(org, env, ai.StatusRevoking), mk(org, env, ai.StatusRevoking)
	active := mk(org, env, ai.StatusActive)
	foreign := mk(other, env2, ai.StatusRevoking)

	// only the named keys that are still revoking give up
	failed, err := repo.FailRevocation(ctx, org, env, []ai.KeyID{a.ID, active.ID}, "gateway down")
	if err != nil || len(failed) != 1 || failed[0].ID != a.ID || failed[0].Status != ai.StatusRevokeFailed || failed[0].RevokeError != "gateway down" {
		t.Fatalf("fail revocation %+v %v", failed, err)
	}
	// the first failure counts one attempt and waits 5 minutes
	if failed[0].RevokeAttempts != 1 || failed[0].NextRetryAt == nil || !within(time.Until(*failed[0].NextRetryAt), 5*time.Minute) {
		t.Errorf("first failure: attempts %d, next %v", failed[0].RevokeAttempts, failed[0].NextRetryAt)
	}
	if got, _ := repo.Find(ctx, org, env, b.ID); got.Status != ai.StatusRevoking || got.RevokeAttempts != 0 || got.NextRetryAt != nil {
		t.Errorf("b was not named: %+v", got)
	}
	if got, _ := repo.Find(ctx, org, env, active.ID); got.Status != ai.StatusActive {
		t.Errorf("the active key was touched: %+v", got)
	}
	if none, err := repo.FailRevocation(ctx, org, env, nil, "x"); err != nil || len(none) != 0 {
		t.Errorf("no ids must change nothing: %+v %v", none, err)
	}
	// a creating key can be given up too (an attempt nobody can clean up)
	c := mk(org, env2, ai.StatusCreating)
	if k, ok, err := repo.FailKey(ctx, org, env2, c.ID, "gateway down"); err != nil || !ok || k.RevokeAttempts != 1 || k.Status != ai.StatusRevokeFailed {
		t.Fatalf("fail key %+v %v %v", k, ok, err)
	}
	if _, ok, _ := repo.FailKey(ctx, org, env, active.ID, "x"); ok {
		t.Error("an active key was failed")
	}
	if _, ok, _ := repo.FailKey(ctx, other, env2, c.ID, "x"); ok {
		t.Error("another tenant's key was failed")
	}

	// the sweep sees none of them while they wait
	if waiting, err := repo.ListRevokeFailed(ctx, 10); err != nil || len(waiting) != 0 {
		t.Fatalf("keys still waiting were listed: %+v %v", waiting, err)
	}
	if again, err := repo.RetryRevocation(ctx, org, env); err != nil || len(again) != 0 {
		t.Fatalf("a key still waiting was retried: %+v %v", again, err)
	}
	// ...and sees one key per environment, of every tenant, once they are due
	if _, err := repo.FailRevocation(ctx, other, env2, []ai.KeyID{foreign.ID}, "gateway down"); err != nil {
		t.Fatal(err)
	}
	due := func(id ai.KeyID, ago time.Duration) {
		t.Helper()
		if _, err := db.Exec(`UPDATE ai.keys SET next_retry_at = now() - $2 * interval '1 second' WHERE id = $1`, id, ago.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	due(a.ID, time.Minute)
	due(c.ID, time.Hour)
	due(foreign.ID, 2*time.Hour)
	listed, err := repo.ListRevokeFailed(ctx, 10)
	if err != nil || len(listed) != 2 {
		t.Fatalf("sweep list %+v %v", listed, err)
	}
	// the longest due first, whatever its creation order
	if listed[0].ID != foreign.ID {
		t.Errorf("order %v", []ai.KeyID{listed[0].ID, listed[1].ID})
	}
	if limited, _ := repo.ListRevokeFailed(ctx, 1); len(limited) != 1 {
		t.Errorf("limit ignored: %d", len(limited))
	}

	// retrying clears the reason and only touches the environment's due failed keys
	again, err := repo.RetryRevocation(ctx, org, env)
	if err != nil || len(again) != 1 || again[0].ID != a.ID || again[0].Status != ai.StatusRevoking || again[0].RevokeError != "" {
		t.Fatalf("retry %+v %v", again, err)
	}
	// it keeps the attempts: failing again waits twice as long (10 minutes)
	if again[0].RevokeAttempts != 1 {
		t.Errorf("attempts reset by the sweep: %d", again[0].RevokeAttempts)
	}
	second, err := repo.FailRevocation(ctx, org, env, []ai.KeyID{a.ID}, "still down")
	if err != nil || len(second) != 1 || second[0].RevokeAttempts != 2 || !within(time.Until(*second[0].NextRetryAt), 10*time.Minute) {
		t.Fatalf("second failure %+v %v", second, err)
	}
	if got, _ := repo.Find(ctx, other, env2, foreign.ID); got.Status != ai.StatusRevokeFailed {
		t.Errorf("another environment's failed key was retried: %+v", got)
	}
	// a person turning the AI off again starts over, due or not
	marked, err := repo.MarkRevoking(ctx, org, env)
	if err != nil || len(marked) != 3 {
		t.Fatalf("mark revoking %+v %v", marked, err)
	}
	for _, k := range marked {
		if k.Status != ai.StatusRevoking || k.RevokeError != "" || k.RevokeAttempts != 0 || k.NextRetryAt != nil {
			t.Errorf("not reset: %+v", k)
		}
	}
	// another tenant's key (same environment id, as a test can make it) is not
	// taken by someone else's MarkRevoking
	own, err := repo.MarkRevoking(ctx, org, env2)
	if err != nil || len(own) != 1 || own[0].ID != c.ID {
		t.Fatalf("mark revoking %+v %v", own, err)
	}
	if got, _ := repo.Find(ctx, other, env2, foreign.ID); got.Status != ai.StatusRevokeFailed || got.RevokeAttempts != 1 {
		t.Errorf("another tenant's key was marked: %+v", got)
	}
	foreignMarked, err := repo.MarkRevoking(ctx, other, env2)
	if err != nil || len(foreignMarked) != 1 || foreignMarked[0].Status != ai.StatusRevoking || foreignMarked[0].RevokeError != "" {
		t.Fatalf("mark revoking %+v %v", foreignMarked, err)
	}
}

// within says whether d is the wait asked for, give or take the seconds the
// test took and the database's clock running a little ahead of the test's
// (Postgres in the Docker VM drifts by tens of milliseconds).
func within(d, want time.Duration) bool { return d > want-30*time.Second && d <= want+5*time.Second }

// The wait doubles from 5 minutes and stops at 6 hours.
func TestRevokeBackoffGrowsAndIsCapped(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: aipg.Schema, Files: aipg.Migrations()})
	repo := aipg.New(db)
	ctx := context.Background()
	org, env := identity.NewOrganizationID(), project.NewEnvironmentID()
	k := ai.Key{ID: ai.NewKeyID(), Organization: org, Project: project.NewID(), Environment: env, Status: ai.StatusRevoking,
		CreatedBy: identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")}
	k.Name = ai.KeyName(env, k.ID)
	if err := repo.Create(ctx, k); err != nil {
		t.Fatal(err)
	}
	for i, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute,
		160 * time.Minute, 320 * time.Minute, 6 * time.Hour, 6 * time.Hour} {
		failed, err := repo.FailRevocation(ctx, org, env, []ai.KeyID{k.ID}, "down")
		if err != nil || len(failed) != 1 || failed[0].RevokeAttempts != i+1 || !within(time.Until(*failed[0].NextRetryAt), want) {
			t.Fatalf("failure %d: want %v: %+v %v", i+1, want, failed, err)
		}
		if _, err := db.Exec(`UPDATE ai.keys SET status = 'revoking' WHERE id = $1`, k.ID); err != nil {
			t.Fatal(err)
		}
	}
	// a very long streak neither overflows nor passes the cap
	if _, err := db.Exec(`UPDATE ai.keys SET revoke_attempts = 100000 WHERE id = $1`, k.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.FailRevocation(ctx, org, env, []ai.KeyID{k.ID}, "down")
	if err != nil || len(failed) != 1 || !within(time.Until(*failed[0].NextRetryAt), 6*time.Hour) {
		t.Fatalf("long streak: %+v %v", failed, err)
	}
}

// One environment that keeps failing must not starve the others: the keys
// that never waited, and the ones due the longest, come before the rest, and
// those still waiting are not listed at all.
func TestListRevokeFailedDoesNotStarve(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: aipg.Schema, Files: aipg.Migrations()})
	repo := aipg.New(db)
	ctx := context.Background()
	org := identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	var ids []ai.KeyID
	for i := 0; i < 5; i++ {
		env := project.NewEnvironmentID()
		k := ai.Key{ID: ai.NewKeyID(), Organization: org, Project: project.NewID(), Environment: env, Status: ai.StatusRevoking, CreatedBy: user}
		k.Name = ai.KeyName(env, k.ID)
		if err := repo.Create(ctx, k); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.FailRevocation(ctx, org, env, []ai.KeyID{k.ID}, "down"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, k.ID)
	}
	// the oldest three keep failing (their next try is in the future); the
	// newest two are due (one never waited: NULL)
	if _, err := db.Exec(`UPDATE ai.keys SET next_retry_at = now() - interval '1 hour' WHERE id = $1`, ids[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ai.keys SET next_retry_at = NULL WHERE id = $1`, ids[4]); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListRevokeFailed(ctx, 2)
	if err != nil || len(got) != 2 || got[0].ID != ids[4] || got[1].ID != ids[3] {
		t.Fatalf("want the never-waited key, then the due one: %+v %v", got, err)
	}
	// a limit of 1 would have starved ids[3] and ids[4] under the old order
	// (created_at): the three that wait are not listed at all
	for _, k := range got {
		if k.ID == ids[0] || k.ID == ids[1] || k.ID == ids[2] {
			t.Errorf("a key still waiting was listed: %v", k.ID)
		}
	}
}
