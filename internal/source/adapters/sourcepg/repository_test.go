package sourcepg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type env struct {
	t    *testing.T
	ctx  context.Context
	db   *sqlx.DB
	repo *sourcepg.Repository
	user identity.UserID
}

func setup(t *testing.T) *env {
	db := pgtest.DB(t, migrate.Set{Owner: sourcepg.Schema, Files: sourcepg.Migrations()})
	return &env{t: t, ctx: context.Background(), db: db, repo: sourcepg.New(db), user: identity.MustParseUserID(uuid.NewString())}
}

var externals int64 = 1000

func (e *env) install(org identity.OrganizationID) source.Installation {
	e.t.Helper()
	externals++
	i, err := e.repo.UpsertInstallation(e.ctx, source.Installation{ID: source.NewInstallationID(), Organization: org,
		ExternalID: externals, Account: "acme", AccountType: "Organization", LinkedBy: e.user})
	if err != nil {
		e.t.Fatal(err)
	}
	return i
}

func (e *env) connect(i source.Installation, p project.ID, repo int64, branch string) source.Connection {
	e.t.Helper()
	c, err := e.repo.UpsertConnection(e.ctx, source.Connection{ID: source.NewConnectionID(), Organization: i.Organization,
		Project: p, Installation: i.ID, RepositoryID: repo, Repository: "acme/app", Branch: branch, Root: "apps/web",
		Config: release.Config{Port: 3000, HealthPath: "/", Replicas: 1, Env: map[string]string{"A": "b"}}, ConnectedBy: e.user})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func (e *env) push(c source.Connection, commit string, trigger source.Trigger) source.Push {
	e.t.Helper()
	p := source.Push{ID: source.NewPushID(), Organization: c.Organization, Project: c.Project, Connection: c.ID,
		Git:     build.GitRef{Provider: build.ProviderGitHub, Repository: c.Repository, Branch: c.Branch, Commit: commit, Title: "fix", Author: "ana"},
		Trigger: trigger, Status: source.StatusQueued}
	if err := e.repo.CreatePush(e.ctx, p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("%s: want NotFound, got %v", what, err)
	}
}

func TestInstallations(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()

	i := e.install(org)
	// a refresh keeps the id and updates what GitHub says
	again, err := repo.UpsertInstallation(ctx, source.Installation{ID: source.NewInstallationID(), Organization: org,
		ExternalID: i.ExternalID, Account: "acme-renamed", AccountType: "Organization", Suspended: true})
	if err != nil || again.ID != i.ID || again.Account != "acme-renamed" || !again.Suspended || again.LinkedBy != e.user {
		t.Fatalf("refresh %+v %v", again, err)
	}
	// another organization cannot take it
	_, err = repo.UpsertInstallation(ctx, source.Installation{ID: source.NewInstallationID(), Organization: other,
		ExternalID: i.ExternalID, Account: "acme", AccountType: "Organization"})
	var xe *errx.Error
	if !errx.As(err, &xe) || xe.Type != errx.TypeConflict || xe.Code != source.CodeTaken {
		t.Fatalf("taken: %v", err)
	}

	if got, err := repo.FindInstallation(ctx, org, i.ID); err != nil || got.ExternalID != i.ExternalID {
		t.Fatalf("find %+v %v", got, err)
	}
	_, err = repo.FindInstallation(ctx, other, i.ID)
	notFound(t, "cross-tenant find", err)
	if got, err := repo.FindInstallationByExternal(ctx, i.ExternalID); err != nil || got.ID != i.ID {
		t.Fatalf("by external %+v %v", got, err)
	}
	if list, err := repo.ListInstallations(ctx, org); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if list, err := repo.ListInstallations(ctx, other); err != nil || len(list) != 0 {
		t.Fatalf("list other %v %v", list, err)
	}

	notFound(t, "cross-tenant delete", repo.DeleteInstallation(ctx, other, i.ID))
	if err := repo.DeleteInstallation(ctx, org, i.ID); err != nil {
		t.Fatal(err)
	}
	_, err = repo.FindInstallationByExternal(ctx, i.ExternalID)
	notFound(t, "deleted", err)
}

func TestConnections(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	i := e.install(org)
	p := project.NewID()

	c := e.connect(i, p, 42, "main")
	if c.Config.Port != 3000 || c.Config.Env["A"] != "b" || c.Root != "apps/web" || c.LastCommit != "" {
		t.Fatalf("connect %+v", c)
	}
	_, err := repo.FindConnection(ctx, other, p)
	notFound(t, "cross-tenant find", err)

	// a push moves the last commit; reconnecting the same branch keeps it
	e.push(c, sha('a'), source.TriggerWebhook)
	if err := repo.ObserveHead(ctx, c.ID, `W/"1"`, time.Now()); err != nil {
		t.Fatal(err)
	}
	same := e.connect(i, p, 42, "main")
	if same.ID != c.ID || same.LastCommit != sha('a') || same.ETag != `W/"1"` {
		t.Fatalf("reconnect same %+v", same)
	}
	// another branch forgets them
	moved := e.connect(i, p, 42, "dev")
	if moved.ID != c.ID || moved.LastCommit != "" || moved.ETag != "" || moved.Branch != "dev" {
		t.Fatalf("reconnect other branch %+v", moved)
	}

	// lock needs a transaction
	if _, err := repo.LockConnection(ctx, org, p); err == nil {
		t.Fatal("lock outside a transaction")
	}
	err = txn.Postgres{DB: e.db}.Do(ctx, func(ctx context.Context) error {
		got, err := repo.LockConnection(ctx, org, p)
		if err == nil && got.ID != c.ID {
			t.Errorf("locked %+v", got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if list, err := repo.ListConnectionsByRepository(ctx, i.ID, 42); err != nil || len(list) != 1 {
		t.Fatalf("by repository %v %v", list, err)
	}

	// the poll: never checked first, then by age; suspended skipped
	p2 := project.NewID()
	c2 := e.connect(i, p2, 43, "main")
	recon, err := repo.ListConnectionsToReconcile(ctx, time.Now(), 10)
	if err != nil || len(recon) != 2 {
		t.Fatalf("reconcile %v %v", recon, err)
	}
	if err := repo.ObserveHead(ctx, c2.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if recon, _ = repo.ListConnectionsToReconcile(ctx, time.Now().Add(-time.Minute), 10); len(recon) != 1 || recon[0].ID != c.ID {
		t.Fatalf("reconcile after check %v", recon)
	}
	if _, err := repo.UpsertInstallation(ctx, source.Installation{ID: source.NewInstallationID(), Organization: org,
		ExternalID: i.ExternalID, Account: "acme", AccountType: "Organization", Suspended: true}); err != nil {
		t.Fatal(err)
	}
	if recon, _ = repo.ListConnectionsToReconcile(ctx, time.Now().Add(time.Hour), 10); len(recon) != 0 {
		t.Fatalf("suspended reconciled %v", recon)
	}

	// an installation's connections: some repositories, then all
	gone, err := repo.DeleteConnectionsOfInstallation(ctx, org, i.ID, []int64{43})
	if err != nil || len(gone) != 1 || gone[0].ID != c2.ID {
		t.Fatalf("delete of repository %v %v", gone, err)
	}
	if gone, err = repo.DeleteConnectionsOfInstallation(ctx, other, i.ID, nil); err != nil || len(gone) != 0 {
		t.Fatalf("cross-tenant delete %v %v", gone, err)
	}
	// an installation with connections cannot be deleted
	if err := repo.DeleteInstallation(ctx, org, i.ID); err == nil {
		t.Fatal("deleted an installation with connections")
	}
	notFound(t, "cross-tenant delete", repo.DeleteConnection(ctx, other, p))
	if err := repo.DeleteConnection(ctx, org, p); err != nil {
		t.Fatal(err)
	}
	_, err = repo.FindConnection(ctx, org, p)
	notFound(t, "deleted", err)
}

func TestPushes(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	i := e.install(org)
	p := project.NewID()
	c := e.connect(i, p, 42, "main")

	a := e.push(c, sha('a'), source.TriggerWebhook)
	b := e.push(c, sha('b'), source.TriggerReconcile)
	m := e.push(c, sha('b'), source.TriggerManual) // "Publicar ahora" queues the same commit again
	if got, _ := repo.FindConnection(ctx, org, p); got.LastCommit != sha('b') {
		t.Fatalf("last commit %q", got.LastCommit)
	}

	got, err := repo.FindPush(ctx, org, a.ID)
	if err != nil || got.Git.Commit != sha('a') || got.Git.Author != "ana" || got.Status != source.StatusQueued || got.Error != nil || got.Deployment != nil {
		t.Fatalf("find %+v %v", got, err)
	}
	_, err = repo.FindPush(ctx, other, a.ID)
	notFound(t, "cross-tenant find", err)
	if _, err := repo.LockPush(ctx, org, a.ID); err == nil {
		t.Fatal("lock outside a transaction")
	}

	// the newest skips what was queued before it
	skipped, err := repo.SkipPushes(ctx, org, c.ID, b.ID)
	if err != nil || len(skipped) != 1 || skipped[0].ID != a.ID || skipped[0].Status != source.StatusSkipped {
		t.Fatalf("skip %v %v", skipped, err)
	}
	if skipped, _ = repo.SkipPushes(ctx, other, c.ID, m.ID); len(skipped) != 0 {
		t.Fatalf("cross-tenant skip %v", skipped)
	}

	// deployed and failed
	d := release.NewDeploymentID()
	b.Status, b.Deployment = source.StatusDeployed, &d
	if err := repo.UpdatePush(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.FindPushByDeployment(ctx, org, d); err != nil || got.ID != b.ID {
		t.Fatalf("by deployment %+v %v", got, err)
	}
	_, err = repo.FindPushByDeployment(ctx, other, d)
	notFound(t, "cross-tenant by deployment", err)
	f := source.FailureOf(source.CodeRootMissing, "apps/web")
	m.Status, m.Error = source.StatusFailed, &f
	if err := repo.UpdatePush(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindPush(ctx, org, m.ID); got.Error == nil || got.Error.Code != source.CodeRootMissing || got.Error.Message == "" {
		t.Fatalf("failed %+v", got)
	}
	// the schema keeps a push's state consistent
	bad := a
	bad.Status = source.StatusDeployed
	if err := repo.UpdatePush(ctx, bad); err == nil {
		t.Fatal("deployed without a deployment")
	}
	notFound(t, "cross-tenant update", repo.UpdatePush(ctx, source.Push{ID: a.ID, Organization: other, Status: source.StatusSkipped}))

	// newest first, filtered
	page, err := repo.ListPushes(ctx, org, p, source.PushFilter{}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 3 || len(page.Items) != 3 || page.Items[0].ID != m.ID || page.Items[2].ID != a.ID {
		t.Fatalf("list %+v %v", page, err)
	}
	page, err = repo.ListPushes(ctx, org, p, source.PushFilter{Status: []source.PushStatus{source.StatusSkipped}}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 1 || page.Items[0].ID != a.ID {
		t.Fatalf("filtered %+v %v", page, err)
	}
	if page, _ = repo.ListPushes(ctx, other, p, source.PushFilter{}, query.Pagination{Limit: 10}); page.Page.Total != 0 {
		t.Fatalf("cross-tenant list %+v", page)
	}

	// history outlives the connection
	if err := repo.DeleteConnection(ctx, org, p); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindPush(ctx, org, a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveries(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	if first, err := repo.SeeDelivery(ctx, "d-1"); err != nil || !first {
		t.Fatalf("first %v %v", first, err)
	}
	if first, err := repo.SeeDelivery(ctx, "d-1"); err != nil || first {
		t.Fatalf("again %v %v", first, err)
	}
	if n, err := repo.ForgetDeliveries(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("forget %d %v", n, err)
	}
	if first, _ := repo.SeeDelivery(ctx, "d-1"); !first {
		t.Fatal("forgotten delivery still seen")
	}
}
