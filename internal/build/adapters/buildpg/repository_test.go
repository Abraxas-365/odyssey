package buildpg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildpg"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

const image = "r.local/o/p@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: buildpg.Schema, Files: buildpg.Migrations()})
	repo := buildpg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	proj, env := project.NewID(), project.NewEnvironmentID()

	source := func() build.Source {
		t.Helper()
		s := build.Source{ID: build.NewSourceID(), Organization: org, Project: proj, Name: "mi-app", Size: 10,
			SHA256: strings.Repeat("ab", 32), Files: 2, UnpackedSize: 20}
		s.Key = "organizations/" + org.String() + "/sources/" + s.ID.String()
		if err := repo.CreateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := source()
	if got, err := repo.FindSource(ctx, org, s.ID); err != nil || got.Key != s.Key || got.SHA256 != s.SHA256 || got.Expired() {
		t.Fatalf("find source %+v %v", got, err)
	}
	if _, err := repo.FindSource(ctx, other, s.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant source: %v", err)
	}

	newBuild := func(src build.Source) build.Build {
		t.Helper()
		b := build.Build{ID: build.NewID(), Organization: org, Project: proj, Environment: env, Source: src.ID,
			Status: build.StatusQueued, Run: workflow.NewRunID()}
		if err := repo.CreateBuild(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := newBuild(s)
	if err := repo.CreateBuild(ctx, b); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("duplicate build: %v", err)
	}
	if _, err := repo.FindBuild(ctx, other, b.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant build: %v", err)
	}
	if err := repo.MarkRunning(ctx, other, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindBuild(ctx, org, b.ID); got.Status != build.StatusQueued {
		t.Fatalf("another tenant moved the build: %s", got.Status)
	}
	if err := repo.MarkRunning(ctx, org, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindBuild(ctx, org, b.ID); got.Status != build.StatusRunning || got.StartedAt == nil {
		t.Fatalf("running: %+v", got)
	}
	// Logs are recorded only once finished; a succeeded build needs a digest.
	if err := repo.SetLogs(ctx, org, b.ID, "k", nil); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("logs of an unfinished build: %v", err)
	}
	if err := repo.Finish(ctx, org, b.ID, build.StatusSucceeded, "r.local/o/p:latest", nil); err == nil {
		t.Fatal("succeeded without a digest")
	}
	if err := repo.Finish(ctx, org, b.ID, build.StatusSucceeded, image, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, org, b.ID, build.StatusCancelled, "", nil); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("finish twice: %v", err)
	}
	index := build.LogIndex{{Seq: 1001, Offset: 52000}, {Seq: 2001, Offset: 104000}}
	if err := repo.SetLogs(ctx, org, b.ID, "organizations/x/builds/y.log", index); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindBuild(ctx, org, b.ID); got.Image != image || got.LogsKey == "" || got.FinishedAt == nil ||
		len(got.LogsIndex) != 2 || got.LogsIndex[1] != index[1] {
		t.Fatalf("succeeded: %+v", got)
	}

	failed := newBuild(s)
	f := build.Failure{Code: build.CodeUnsupported, Message: build.Message(build.CodeUnsupported), Detail: "no start command"}
	if err := repo.Finish(ctx, org, failed.ID, build.StatusFailed, "", &f); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.FindBuild(ctx, org, failed.ID); got.Error == nil || *got.Error != f || got.LogsIndex == nil || len(got.LogsIndex) != 0 {
		t.Fatalf("failure: %+v, index %#v", got.Error, got.LogsIndex)
	}

	// Sweep: old sources with only finished builds, and orphans; never one
	// an unfinished build needs.
	busy := source()
	newBuild(busy)
	orphan := source()
	fresh := source()
	_, err := db.ExecContext(ctx, `UPDATE build.sources SET created_at = now() - interval '8 days' WHERE id = ANY($1::uuid[])`,
		"{"+s.ID.String()+","+busy.ID.String()+"}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `UPDATE build.sources SET created_at = now() - interval '2 hours' WHERE id = $1`, orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var got []build.Source
	err = tx.Do(ctx, func(ctx context.Context) error {
		var err error
		got, err = repo.LockExpirable(ctx, now.Add(-7*24*time.Hour), now.Add(-time.Hour), 10)
		if err != nil {
			return err
		}
		for _, src := range got {
			if err := repo.Expire(ctx, src.Organization, src.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := idsOf(got); len(ids) != 2 || !ids[s.ID] || !ids[orphan.ID] || ids[busy.ID] || ids[fresh.ID] {
		t.Fatalf("expirable: %v", ids)
	}
	if err := repo.Expire(ctx, org, s.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("expire twice: %v", err)
	}
	if got, _ := repo.FindSource(ctx, org, s.ID); !got.Expired() {
		t.Fatal("not expired")
	}
	undeleted, err := repo.ListUndeleted(ctx, 10)
	if err != nil || len(undeleted) != 2 {
		t.Fatalf("undeleted %v %v", undeleted, err)
	}
	if err := repo.ForgetSourceBytes(ctx, other, s.ID); err != nil {
		t.Fatal(err)
	}
	for _, src := range undeleted {
		if err := repo.ForgetSourceBytes(ctx, src.Organization, src.ID); err != nil {
			t.Fatal(err)
		}
	}
	if undeleted, _ := repo.ListUndeleted(ctx, 10); len(undeleted) != 0 {
		t.Fatalf("still undeleted: %v", undeleted)
	}
}

func TestQuotaAndDiscard(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: buildpg.Schema, Files: buildpg.Migrations()})
	repo := buildpg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	proj, env := project.NewID(), project.NewEnvironmentID()
	source := func(o identity.OrganizationID, size int64) build.Source {
		t.Helper()
		s := build.Source{ID: build.NewSourceID(), Organization: o, Project: proj, Name: "mi-app", Size: size,
			SHA256: strings.Repeat("ab", 32), Files: 1, UnpackedSize: size}
		s.Key = "org/" + o.String() + "/sources/" + s.ID.String()
		if err := repo.CreateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	buildOf := func(s build.Source, finished bool) {
		t.Helper()
		b := build.Build{ID: build.NewID(), Organization: s.Organization, Project: proj, Environment: env, Source: s.ID,
			Status: build.StatusQueued, Run: workflow.NewRunID()}
		if err := repo.CreateBuild(ctx, b); err != nil {
			t.Fatal(err)
		}
		if finished {
			if err := repo.Finish(ctx, s.Organization, b.ID, build.StatusCancelled, "", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	built := source(org, 100) // evictable
	buildOf(built, true)
	building := source(org, 10) // an unfinished build needs it
	buildOf(building, false)
	unused := source(org, 1) // never built: not evictable (a deployment may be about to build it)
	source(other, 1000)      // another tenant's

	u, err := repo.Usage(ctx, org)
	if err != nil || u != (build.Usage{Stored: 111, Evictable: 100}) {
		t.Fatalf("usage %+v %v", u, err)
	}
	if err := repo.LockQuota(ctx, org); err == nil {
		t.Fatal("LockQuota outside a transaction")
	}
	err = tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.LockQuota(ctx, org); err != nil {
			return err
		}
		// Another upload of the same organization waits; another's does not.
		blocked, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if err := tx.Do(blocked, func(ctx context.Context) error { return repo.LockQuota(ctx, org) }); err == nil {
			t.Error("two uploads of an organization held the quota at once")
		}
		if err := tx.Do(context.Background(), func(ctx context.Context) error { return repo.LockQuota(ctx, other) }); err != nil {
			t.Errorf("another organization's quota: %v", err)
		}
		got, err := repo.LockEvictable(ctx, org, 10)
		if err != nil {
			return err
		}
		if ids := idsOf(got); len(ids) != 1 || !ids[built.ID] {
			t.Errorf("evictable: %v", ids)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// ExpireUnused: only a source no build used, once, in its organization.
	if _, err := repo.ExpireUnused(ctx, org, unused.ID); err == nil {
		t.Fatal("ExpireUnused outside a transaction")
	}
	expire := func(o identity.OrganizationID, id build.SourceID) (build.Source, error) {
		var s build.Source
		err := tx.Do(ctx, func(ctx context.Context) error {
			var err error
			s, err = repo.ExpireUnused(ctx, o, id)
			return err
		})
		return s, err
	}
	if _, err := expire(other, unused.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant discard: %v", err)
	}
	for _, used := range []build.Source{built, building} {
		if _, err := expire(org, used.ID); !errx.IsType(err, errx.TypeConflict) {
			t.Fatalf("discarded a built source: %v", err)
		}
	}
	if s, err := expire(org, unused.ID); err != nil || !s.Expired() || s.Key != unused.Key {
		t.Fatalf("discard %+v %v", s, err)
	}
	if _, err := expire(org, unused.ID); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("discard twice: %v", err)
	}
	if u, _ := repo.Usage(ctx, org); u != (build.Usage{Stored: 110, Evictable: 100}) {
		t.Fatalf("usage after discard %+v", u)
	}
}

// TestDerivedSources: a source cut from an upload is found by its parent and
// folder (in its organization, while it lasts), only one lives per folder,
// and its builds count as the parent's for the quota and the discard.
func TestDerivedSources(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: buildpg.Schema, Files: buildpg.Migrations()})
	repo := buildpg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	proj, env := project.NewID(), project.NewEnvironmentID()
	create := func(parent *build.Source, root string, size int64) (build.Source, error) {
		s := build.Source{ID: build.NewSourceID(), Organization: org, Project: proj, Name: "mono", Size: size,
			SHA256: strings.Repeat("ab", 32), Files: 1, UnpackedSize: size, Root: root}
		if parent != nil {
			s.Parent = &parent.ID
		}
		s.Key = "org/" + org.String() + "/sources/" + s.ID.String()
		return s, repo.CreateSource(ctx, s)
	}
	mono, err := create(nil, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	api, err := create(&mono, "apps/api", 10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindDerived(ctx, org, mono.ID, "apps/api")
	if err != nil || got.ID != api.ID || got.Parent == nil || *got.Parent != mono.ID || got.Root != "apps/api" {
		t.Fatalf("find derived %+v %v", got, err)
	}
	if _, err := repo.FindDerived(ctx, other, mono.ID, "apps/api"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant's: %v", err)
	}
	if _, err := repo.FindDerived(ctx, org, mono.ID, "apps/web"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another folder: %v", err)
	}
	// One live source per folder; a parent and a folder go together.
	if _, err := create(&mono, "apps/api", 10); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("second cut of a folder: %v", err)
	}
	if _, err := create(nil, "apps/api", 10); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("a folder without a parent: %v", err)
	}

	// A build of the folder makes the parent built: not discardable,
	// evictable with it once the build ends.
	b := build.Build{ID: build.NewID(), Organization: org, Project: proj, Environment: env, Source: api.ID,
		Status: build.StatusQueued, Run: workflow.NewRunID()}
	if err := repo.CreateBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	if u, _ := repo.Usage(ctx, org); u != (build.Usage{Stored: 110, Evictable: 0}) {
		t.Fatalf("usage while building %+v", u)
	}
	if err := repo.Finish(ctx, org, b.ID, build.StatusSucceeded, "img@sha256:"+strings.Repeat("0", 64), nil); err != nil {
		t.Fatal(err)
	}
	if u, _ := repo.Usage(ctx, org); u != (build.Usage{Stored: 110, Evictable: 110}) {
		t.Fatalf("usage once built %+v", u)
	}
	err = tx.Do(ctx, func(ctx context.Context) error {
		_, err := repo.ExpireUnused(ctx, org, mono.ID)
		return err
	})
	if !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("discarded a parent whose folder was built: %v", err)
	}
	// Expired, the folder can be cut again.
	if err := repo.Expire(ctx, org, api.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindDerived(ctx, org, mono.ID, "apps/api"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("expired derived: %v", err)
	}
	if _, err := create(&mono, "apps/api", 10); err != nil {
		t.Fatalf("cut again after expiry: %v", err)
	}
}

func idsOf(sources []build.Source) map[build.SourceID]bool {
	out := map[build.SourceID]bool{}
	for _, s := range sources {
		out[s.ID] = true
	}
	return out
}
