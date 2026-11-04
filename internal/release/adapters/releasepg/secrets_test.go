package releasepg_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// TestSettingsCarryVariablesAndSecrets: a revision stores the whole
// configuration (variables and secret references) and reads it back as it
// was; a revision written before 2a reads as empty collections.
func TestSettingsCarryVariablesAndSecrets(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID(uuid.NewString())
	env := project.NewEnvironmentID()
	loaded := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	s := release.DefaultSettings(org, env)
	s.CreatedBy = user
	s.Env = map[string]string{"TZ": "America/Lima", "LOG_LEVEL": "debug"}
	s.Secrets = []release.SecretRef{
		{Name: "STRIPE_API_KEY", Source: release.SecretManaged, Version: 2, KeyID: "k3y", LoadedBy: user, LoadedAt: &loaded},
		{Name: "DB_PASSWORD", Source: release.SecretExternal, Store: "vault-prod", Key: "apps/db", Property: "password"},
	}
	var out release.Settings
	if err := tx.Do(ctx, func(ctx context.Context) error {
		var err error
		out, err = repo.AppendSettings(ctx, s)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check := func(what string, got release.Settings) {
		t.Helper()
		if got.Revision != 1 || !maps.Equal(got.Env, s.Env) || len(got.Secrets) != 2 ||
			got.Secrets[0].Name != "DB_PASSWORD" || got.Secrets[0].Store != "vault-prod" || got.Secrets[0].Property != "password" ||
			got.Secrets[1].Name != "STRIPE_API_KEY" || got.Secrets[1].Version != 2 || got.Secrets[1].KeyID != "k3y" ||
			got.Secrets[1].LoadedBy != user || got.Secrets[1].LoadedAt == nil || !got.Secrets[1].LoadedAt.Equal(loaded) {
			t.Fatalf("%s: %+v", what, got)
		}
	}
	check("appended", out)
	latest, err := repo.FindSettings(ctx, org, env)
	if err != nil {
		t.Fatal(err)
	}
	check("latest", latest)
	rev, err := repo.FindSettingsRevision(ctx, org, env, 1)
	if err != nil {
		t.Fatal(err)
	}
	check("revision 1", rev)

	// a row from before 2a (the columns' defaults) reads as empty, not nil
	old := project.NewEnvironmentID()
	if _, err := db.ExecContext(ctx, `INSERT INTO release.environment_settings (environment_id, organization_id, revision, access_mode)
		VALUES ($1, $2, 1, 'team')`, old, org); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.FindSettings(ctx, org, old); err != nil || got.Env == nil || got.Secrets == nil || len(got.Env)+len(got.Secrets) != 0 {
		t.Fatalf("pre-2a row %+v %v", got, err)
	}
	// the table refuses a malformed document
	if _, err := db.ExecContext(ctx, `INSERT INTO release.environment_settings (environment_id, organization_id, revision, access_mode, env)
		VALUES ($1, $2, 2, 'team', '[]')`, old, org); err == nil {
		t.Fatal("env as an array was stored")
	}
	// tenant isolation: another organization sees the defaults
	if got, err := repo.FindSettings(ctx, other, env); err != nil || got.Revision != 0 || len(got.Env)+len(got.Secrets) != 0 {
		t.Fatalf("cross-tenant %+v %v", got, err)
	}
}

// TestSecretVersions: the high-water mark of each managed secret is kept in
// its own table, raised with every revision: it survives removing the secret
// (and would survive purging the history), only goes up, and ignores external
// secrets and other organizations.
func TestSecretVersions(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	env, otherEnv := project.NewEnvironmentID(), project.NewEnvironmentID()

	if got, err := repo.ListSecretVersions(ctx, org, env); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty %v %v", got, err)
	}
	appendRev := func(org identity.OrganizationID, at project.EnvironmentID, secrets ...release.SecretRef) {
		t.Helper()
		s := release.DefaultSettings(org, at)
		s.Secrets = secrets
		if err := tx.Do(ctx, func(ctx context.Context) error { _, err := repo.AppendSettings(ctx, s); return err }); err != nil {
			t.Fatal(err)
		}
	}
	appendRev(org, env, release.SecretRef{Name: "A", Source: release.SecretManaged, Version: 3, KeyID: "k"},
		release.SecretRef{Name: "B", Source: release.SecretManaged}, // requested, never loaded
		release.SecretRef{Name: "DB", Source: release.SecretExternal, Store: "vault", Key: "x"})
	appendRev(org, env)                                                                                      // all removed
	appendRev(org, env, release.SecretRef{Name: "A", Source: release.SecretManaged, Version: 1, KeyID: "k"}) // re-added lower
	appendRev(other, otherEnv, release.SecretRef{Name: "A", Source: release.SecretManaged, Version: 9, KeyID: "k"})

	got, err := repo.ListSecretVersions(ctx, org, env)
	if err != nil || len(got) != 1 || got["A"] != 3 {
		t.Fatalf("versions %v %v", got, err)
	}
	// it only goes up
	appendRev(org, env, release.SecretRef{Name: "A", Source: release.SecretManaged, Version: 4, KeyID: "k"},
		release.SecretRef{Name: "C", Source: release.SecretManaged, Version: 2, KeyID: "k"})
	if got, err := repo.ListSecretVersions(ctx, org, env); err != nil || len(got) != 2 || got["A"] != 4 || got["C"] != 2 {
		t.Fatalf("raised %v %v", got, err)
	}
	// it does not depend on the history: purge it and the mark stays
	if _, err := db.ExecContext(ctx, `DELETE FROM release.environment_settings WHERE environment_id = $1`, env); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ListSecretVersions(ctx, org, env); err != nil || got["A"] != 4 || got["C"] != 2 {
		t.Fatalf("after purging the history %v %v", got, err)
	}

	if got, err := repo.ListSecretVersions(ctx, other, otherEnv); err != nil || len(got) != 1 || got["A"] != 9 {
		t.Fatalf("other organization %v %v", got, err)
	}
	// tenant isolation: neither reads the other's environment
	if got, err := repo.ListSecretVersions(ctx, org, otherEnv); err != nil || len(got) != 0 {
		t.Fatalf("cross-tenant %v %v", got, err)
	}
	if got, err := repo.ListSecretVersions(ctx, other, env); err != nil || len(got) != 0 {
		t.Fatalf("cross-tenant %v %v", got, err)
	}
	// another organization writing under the same environment id (a bug or an
	// attack: ids are random) can neither raise nor read the first one's marks
	appendRev(other, env, release.SecretRef{Name: "A", Source: release.SecretManaged, Version: 99, KeyID: "k"})
	if got, err := repo.ListSecretVersions(ctx, org, env); err != nil || len(got) != 2 || got["A"] != 4 || got["C"] != 2 {
		t.Fatalf("raised by another organization %v %v", got, err)
	}
	if got, err := repo.ListSecretVersions(ctx, other, env); err != nil || len(got) != 0 {
		t.Fatalf("read by another organization %v %v", got, err)
	}
}

func TestSecretRequests(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user, user2 := identity.MustParseUserID(uuid.NewString()), identity.MustParseUserID(uuid.NewString())
	env := project.NewEnvironmentID()

	if got, err := repo.ListSecretRequests(ctx, org, env); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty %+v %v", got, err)
	}
	put := func(org identity.OrganizationID, name, note string, by identity.UserID) error {
		return repo.PutSecretRequest(ctx, release.SecretRequest{Environment: env, Organization: org, Name: name, Note: note,
			RequestedBy: by, RequestedVia: "assistant:claude-code"})
	}
	if err := put(org, "STRIPE_API_KEY", "pagos", user); err != nil {
		t.Fatal(err)
	}
	if err := put(org, "A_TOKEN", "", user); err != nil {
		t.Fatal(err)
	}
	// asking again renews the request (the newer note and requester)
	if err := put(org, "STRIPE_API_KEY", "pagos con tarjeta", user2); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListSecretRequests(ctx, org, env)
	if err != nil || len(got) != 2 || got[0].Name != "A_TOKEN" || got[1].Name != "STRIPE_API_KEY" ||
		got[1].Note != "pagos con tarjeta" || got[1].RequestedBy != user2 || got[1].RequestedVia != "assistant:claude-code" ||
		got[1].Environment != env || got[1].Organization != org || got[1].CreatedAt.IsZero() {
		t.Fatalf("listed %+v %v", got, err)
	}
	// a bad name is refused by the table
	if err := put(org, "1BAD", "", user); err == nil {
		t.Fatal("a bad name was stored")
	}

	// tenant isolation: another organization neither sees, renews nor deletes them
	if got, err := repo.ListSecretRequests(ctx, other, env); err != nil || len(got) != 0 {
		t.Fatalf("cross-tenant list %+v %v", got, err)
	}
	_ = put(other, "STRIPE_API_KEY", "hijack", user)
	if got, _ := repo.ListSecretRequests(ctx, org, env); got[1].Note != "pagos con tarjeta" || got[1].Organization != org {
		t.Fatalf("another organization renewed a request: %+v", got[1])
	}
	if ok, err := repo.DeleteSecretRequest(ctx, other, env, "STRIPE_API_KEY"); err != nil || ok {
		t.Fatalf("cross-tenant delete %v %v", ok, err)
	}

	if ok, err := repo.DeleteSecretRequest(ctx, org, env, "STRIPE_API_KEY"); err != nil || !ok {
		t.Fatalf("delete %v %v", ok, err)
	}
	if ok, err := repo.DeleteSecretRequest(ctx, org, env, "STRIPE_API_KEY"); err != nil || ok {
		t.Fatalf("second delete %v %v", ok, err)
	}
	if got, _ := repo.ListSecretRequests(ctx, org, env); len(got) != 1 || got[0].Name != "A_TOKEN" {
		t.Fatalf("after delete %+v", got)
	}
}
