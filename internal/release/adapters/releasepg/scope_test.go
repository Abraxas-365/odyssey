package releasepg_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// TestSettingsKeepTheirServices (MS-4): a revision stores each service's
// own variables and the services every secret, resource and the AI reach;
// a revision from before MS-4 reads as the whole app.
func TestSettingsKeepTheirServices(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	env := project.NewEnvironmentID()

	s := release.DefaultSettings(org, env)
	s.Env = map[string]string{"LOG": "info"}
	s.ServiceEnv = map[string]map[string]string{"api": {"LOG": "debug"}, "web": {"PUBLIC_URL": "https://x"}}
	s.Secrets = []release.SecretRef{{Name: "TOKEN", Source: release.SecretManaged, Version: 1, KeyID: "k", Services: release.Scope{"api"}}}
	s.Resources = []release.ResourceUse{{Name: "db", Kind: resourcekind.Postgres, Services: release.Scope{"api", "jobs"}}}
	s.AI = release.AISettings{Enabled: true, KeyVersion: 1, KeyID: "k", BaseURL: "http://gw", Services: release.Scope{"web"}}
	if err := tx.Do(ctx, func(ctx context.Context) error { _, err := repo.AppendSettings(ctx, s); return err }); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindSettings(ctx, org, env)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServiceEnv["api"]["LOG"] != "debug" || got.ServiceEnv["web"]["PUBLIC_URL"] != "https://x" || got.Env["LOG"] != "info" ||
		!slices.Equal(got.Secrets[0].Services, release.Scope{"api"}) ||
		!slices.Equal(got.Resources[0].Services, release.Scope{"api", "jobs"}) ||
		!slices.Equal(got.AI.Services, release.Scope{"web"}) || !got.Scoped() {
		t.Fatalf("read back %+v", got)
	}

	// A row from before MS-4 (the column's default) reads as the whole app.
	old := project.NewEnvironmentID()
	if _, err := db.ExecContext(ctx, `INSERT INTO release.environment_settings (environment_id, organization_id, revision, access_mode)
		VALUES ($1, $2, 1, 'team')`, old, org); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.FindSettings(ctx, org, old); err != nil || got.ServiceEnv == nil || len(got.ServiceEnv) != 0 || got.Scoped() {
		t.Fatalf("pre-MS-4 row %+v %v", got, err)
	}
	// tenant isolation: another organization sees the defaults
	if got, err := repo.FindSettings(ctx, other, env); err != nil || got.Revision != 0 || len(got.ServiceEnv) != 0 {
		t.Fatalf("cross-tenant %+v %v", got, err)
	}
}

// TestSecretRequestsKeepTheirServices (MS-4): a request stores who will
// read the value; asking again replaces it; ScopeSecretRequest changes only
// that, of the organization's own request.
func TestSecretRequestsKeepTheirServices(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID(uuid.NewString())
	env := project.NewEnvironmentID()

	put := func(name string, services release.Scope) {
		t.Helper()
		if err := repo.PutSecretRequest(ctx, release.SecretRequest{Environment: env, Organization: org, Name: name, Note: "n",
			RequestedBy: user, RequestedVia: "console", Services: services}); err != nil {
			t.Fatal(err)
		}
	}
	put("TOKEN", release.Scope{"web", "api"})
	put("WHOLE", nil)
	list := func(org identity.OrganizationID) []release.SecretRequest {
		t.Helper()
		got, err := repo.ListSecretRequests(ctx, org, env)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := list(org)
	if len(got) != 2 || !slices.Equal(got[0].Services, release.Scope{"api", "web"}) || got[1].Services != nil {
		t.Fatalf("listed %+v", got)
	}
	// asking again replaces the services (the caller resolved them)
	put("TOKEN", release.Scope{"api"})
	if got := list(org); !slices.Equal(got[0].Services, release.Scope{"api"}) {
		t.Fatalf("renewed %+v", got[0])
	}

	scope := func(org identity.OrganizationID, name string, services release.Scope) error {
		return repo.ScopeSecretRequest(ctx, release.SecretRequest{Environment: env, Organization: org, Name: name, Services: services})
	}
	if err := scope(org, "TOKEN", nil); err != nil {
		t.Fatal(err)
	}
	if got := list(org); got[0].Services != nil || got[0].Note != "n" || got[0].RequestedBy != user {
		t.Fatalf("scoped to the whole app %+v", got[0])
	}
	if err := scope(org, "MISSING", release.Scope{"api"}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("missing request: %v", err)
	}
	// tenant isolation: another organization can neither scope nor read them
	if err := scope(other, "WHOLE", release.Scope{"api"}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant scope: %v", err)
	}
	if got := list(org); got[1].Services != nil {
		t.Fatalf("another organization scoped a request: %+v", got[1])
	}
	if got := list(other); len(got) != 0 {
		t.Fatalf("cross-tenant list %+v", got)
	}
}
