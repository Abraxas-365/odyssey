package policypg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policypg"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

func errType(err error) errx.Type {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Type
	}
	return ""
}

func setup(t *testing.T) (*policypg.Repository, context.Context) {
	repo, _, ctx := setupDB(t)
	return repo, ctx
}

func setupDB(t *testing.T) (*policypg.Repository, txn.Runner, context.Context) {
	db := pgtest.DB(t, migrate.Set{Owner: policypg.Schema, Files: policypg.Migrations()})
	return policypg.New(db), txn.Postgres{DB: db}, context.Background()
}

func rule(org identity.OrganizationID, name string) policy.Rule {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return policy.Rule{ID: policy.NewRuleID(), Organization: org, Name: name, Description: "d", Expression: "true",
		Effect: policy.EffectDeny, Approvers: "odyssey:approvals:decide", Enabled: true, Version: 1,
		CreatedBy: user, UpdatedBy: user, CreatedAt: now, UpdatedAt: now}
}

func TestCreateFindListAndTenantIsolation(t *testing.T) {
	repo, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := rule(org, "uno")
	if err := repo.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Find(ctx, org, r.ID)
	if err != nil || got.Name != "uno" || got.Effect != policy.EffectDeny || got.Builtin != nil || got.CreatedBy != user || got.Version != 1 {
		t.Fatalf("find %+v %v", got, err)
	}
	if _, err := repo.Find(ctx, other, r.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant find: %v", err)
	}
	if rules, _ := repo.List(ctx, other); len(rules) != 0 {
		t.Fatalf("cross-tenant list: %d", len(rules))
	}
	if n, _ := repo.Count(ctx, org); n != 1 {
		t.Fatalf("count %d", n)
	}
	if n, _ := repo.Count(ctx, other); n != 0 {
		t.Fatalf("other count %d", n)
	}
	bad := rule(org, "x")
	bad.Effect = "maybe" // the CHECK is the last line of defense
	if err := repo.Create(ctx, bad); err == nil {
		t.Fatal("effect outside the set was stored")
	}
}

func TestListPutsBuiltinsFirstThenOldestFirst(t *testing.T) {
	repo, ctx := setup(t)
	org := identity.NewOrganizationID()
	first, second := rule(org, "primera"), rule(org, "segunda")
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	for _, r := range []policy.Rule{second, first} {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.SeedBuiltins(ctx, policy.Builtins(org, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	rules, err := repo.List(ctx, org)
	if err != nil || len(rules) != builtins+2 {
		t.Fatalf("%d rules, %v", len(rules), err)
	}
	if rules[0].Name != "Publicar en producción requiere aprobación de TI" || rules[4].Name != "Producción exige revisión de vulnerabilidades" ||
		rules[builtins].Name != "primera" || rules[builtins+1].Name != "segunda" {
		t.Fatalf("order: %q %q %q %q", rules[0].Name, rules[4].Name, rules[builtins].Name, rules[builtins+1].Name)
	}
}

// builtins is how many rules every organization is born with.
var builtins = len(policy.Builtins(identity.NewOrganizationID(), time.Now()))

func TestSeedBuiltinsIsIdempotentAndPerOrganization(t *testing.T) {
	repo, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	if n, err := repo.SeedBuiltins(ctx, policy.Builtins(org, time.Now().UTC())); err != nil || n != builtins {
		t.Fatalf("first seed: %d %v", n, err)
	}
	if n, err := repo.SeedBuiltins(ctx, policy.Builtins(org, time.Now().UTC())); err != nil || n != 0 {
		t.Fatalf("second seed: %d %v", n, err)
	}
	if n, err := repo.SeedBuiltins(ctx, policy.Builtins(other, time.Now().UTC())); err != nil || n != builtins {
		t.Fatalf("other org seed: %d %v", n, err)
	}
	rules, _ := repo.List(ctx, org)
	if len(rules) != builtins {
		t.Fatalf("%d rules", len(rules))
	}
	// the builtins born off are stored disabled; the others enabled
	for _, r := range rules {
		off := *r.Builtin == policy.BuiltinRequiresScan || *r.Builtin == policy.BuiltinProductionServices
		if off == r.Enabled {
			t.Errorf("%s enabled=%v", *r.Builtin, r.Enabled)
		}
	}
	// a new builtin key reaches an organization that has the old ones
	extra := policy.Builtins(org, time.Now().UTC())[0]
	k := "future_rule"
	extra.Builtin = &k
	if n, _ := repo.SeedBuiltins(ctx, []policy.Rule{extra}); n != 1 {
		t.Fatalf("new key: %d", n)
	}
}

func TestUpdateMovesTheVersionAndDeleteSparesBuiltins(t *testing.T) {
	repo, ctx := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := rule(org, "uno")
	if err := repo.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Name, r.Enabled, r.Effect, r.UpdatedAt = "dos", false, policy.EffectApproval, time.Now().UTC()
	if err := repo.Update(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Find(ctx, org, r.ID)
	if got.Name != "dos" || got.Enabled || got.Effect != policy.EffectApproval || got.Version != 2 {
		t.Fatalf("after update %+v", got)
	}
	// another tenant can neither change nor delete it
	r.Organization = other
	if err := repo.Update(ctx, r); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant update: %v", err)
	}
	if err := repo.Delete(ctx, other, r.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("cross-tenant delete: %v", err)
	}
	if err := repo.Delete(ctx, org, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Find(ctx, org, r.ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("deleted: %v", err)
	}

	// the statement itself spares built-ins
	if _, err := repo.SeedBuiltins(ctx, policy.Builtins(org, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	b, _ := repo.List(ctx, org)
	if err := repo.Delete(ctx, org, b[0].ID); errType(err) != errx.TypeNotFound {
		t.Fatalf("deleted a built-in: %v", err)
	}
	if n, _ := repo.Count(ctx, org); n != builtins {
		t.Fatalf("count %d", n)
	}
}

func TestLocksNeedATransactionAndWorkInOne(t *testing.T) {
	repo, tx, ctx := setupDB(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	r := rule(org, "uno")
	if err := repo.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindForUpdate(ctx, org, r.ID); errType(err) != errx.TypeInternal {
		t.Fatalf("FindForUpdate outside a tx: %v", err)
	}
	if err := repo.LockRules(ctx, org); errType(err) != errx.TypeInternal {
		t.Fatalf("LockRules outside a tx: %v", err)
	}
	err := tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.LockRules(ctx, org); err != nil {
			return err
		}
		got, err := repo.FindForUpdate(ctx, org, r.ID)
		if err != nil || got.ID != r.ID {
			t.Errorf("FindForUpdate %+v %v", got, err)
		}
		if _, err := repo.FindForUpdate(ctx, other, r.ID); errType(err) != errx.TypeNotFound {
			t.Errorf("cross-tenant FindForUpdate: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
