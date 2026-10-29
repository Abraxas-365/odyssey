package releasepg_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRepository(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	proj, env := project.NewID(), project.NewEnvironmentID()

	r := release.Release{ID: release.NewID(), Organization: org, Project: proj, Image: "r.local/app" + digest,
		Config: release.Config{Port: 8080, HealthPath: "/health", Replicas: 2, Env: map[string]string{"TZ": "America/Lima"}}}
	if err := repo.CreateRelease(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.FindRelease(ctx, org, r.ID); err != nil || got.Config.Env["TZ"] != "America/Lima" || got.Config.Port != 8080 {
		t.Fatalf("find release %+v %v", got, err)
	}
	if _, err := repo.FindRelease(ctx, other, r.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant release: %v", err)
	}

	// state: zero before first use, created and locked by LockState
	if s, err := repo.FindState(ctx, org, env); err != nil || s.Current != nil || s.Revision != 0 {
		t.Fatalf("fresh state %+v %v", s, err)
	}
	begin := func(revision int64) release.Deployment {
		t.Helper()
		d := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: proj, Environment: env,
			Release: &r.ID, Image: r.Image, Status: release.StatusPending, Reason: release.ReasonDeploy,
			Step: release.StepApply, Revision: revision, Run: workflow.NewRunID()}
		err := tx.Do(ctx, func(ctx context.Context) error {
			if _, err := repo.LockState(ctx, release.EnvironmentState{Environment: env, Organization: org, Project: proj, App: "app"}); err != nil {
				return err
			}
			if err := repo.CreateDeployment(ctx, d); err != nil {
				return err
			}
			return repo.Begin(ctx, org, env, d.ID, revision)
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	first := begin(1)
	if err := repo.SetProgress(ctx, org, first.ID, release.StatusVerifying, release.StepWaitReady); err != nil {
		t.Fatal(err)
	}

	// one unfinished deployment per environment
	err := tx.Do(ctx, func(ctx context.Context) error {
		return repo.CreateDeployment(ctx, release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: proj,
			Environment: env, Release: &r.ID, Image: r.Image, Status: release.StatusPending, Reason: release.ReasonDeploy,
			Step: release.StepApply, Revision: 2, Run: workflow.NewRunID()})
	})
	if !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("second active deployment: %v", err)
	}
	// a release-less deployment must be building from a source
	err = tx.Do(ctx, func(ctx context.Context) error {
		return repo.CreateDeployment(ctx, release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: proj,
			Environment: project.NewEnvironmentID(), Status: release.StatusPending, Reason: release.ReasonDeploy,
			Step: release.StepApply, Revision: 1, Run: workflow.NewRunID()})
	})
	if err == nil {
		t.Fatal("a deployment without release nor build was stored")
	}

	// promote moves the current release and clears the active deployment
	err = tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.Promote(ctx, org, env, first, "https://app.example"); err != nil {
			return err
		}
		return repo.Finish(ctx, org, first.ID, release.StatusReady, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.FindState(ctx, org, env)
	if err != nil || s.Current == nil || *s.Current != r.ID || s.Active != nil || s.URL != "https://app.example" || s.Revision != 1 {
		t.Fatalf("promoted state %+v %v", s, err)
	}
	// a finished deployment never changes
	if err := repo.Finish(ctx, org, first.ID, release.StatusFailed, &release.Failure{Code: "X"}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("refinish: %v", err)
	}
	if err := repo.SetProgress(ctx, org, first.ID, release.StatusApplying, release.StepApply); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("progress after finish: %v", err)
	}

	second := begin(2)
	err = tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.Finish(ctx, org, second.ID, release.StatusFailed, &release.Failure{Code: release.CodeUnhealthy, Message: "No responde.", Detail: "GET /health 503"}); err != nil {
			return err
		}
		return repo.Settle(ctx, org, env, second.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindDeployment(ctx, org, second.ID)
	if err != nil || got.Status != release.StatusFailed || got.Error == nil || *got.Error != (release.Failure{Code: release.CodeUnhealthy, Message: "No responde.", Detail: "GET /health 503"}) || got.FinishedAt == nil {
		t.Fatalf("failed deployment %+v %v", got, err)
	}
	// a stale revision cannot begin
	err = tx.Do(ctx, func(ctx context.Context) error { return repo.Begin(ctx, org, env, second.ID, 1) })
	if !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("stale begin: %v", err)
	}

	page, err := repo.ListDeployments(ctx, org, env, release.DeploymentFilter{}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 2 || page.Items[0].ID != second.ID {
		t.Fatalf("list %+v %v (newest first)", page, err)
	}
	page, err = repo.ListDeployments(ctx, org, env, release.DeploymentFilter{Status: []release.Status{release.StatusReady}}, query.Pagination{Limit: 10})
	if err != nil || page.Page.Total != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("filtered %+v %v", page, err)
	}
	if page, err := repo.ListDeployments(ctx, other, env, release.DeploymentFilter{}, query.Pagination{Limit: 10}); err != nil || page.Page.Total != 0 {
		t.Fatalf("cross-tenant list %+v %v", page, err)
	}
	if _, err := repo.FindDeployment(ctx, other, first.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant deployment: %v", err)
	}
	// another tenant cannot lock (or create) state for this environment
	err = tx.Do(ctx, func(ctx context.Context) error {
		_, err := repo.LockState(ctx, release.EnvironmentState{Environment: env, Organization: other, Project: proj, App: "x"})
		return err
	})
	if !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant lock: %v", err)
	}

	// from source: building with no release until SetBuilt records it
	b, src := build.NewID(), build.NewSourceID()
	cfg := release.Config{Port: 3000, HealthPath: "/", Replicas: 1, Env: map[string]string{"A": "1"}}
	third := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: proj, Environment: env,
		Build: &b, Source: &release.DeploymentSource{ID: src, Name: "visitas.tar.gz", Size: 1234}, Config: &cfg,
		Status: release.StatusBuilding, Reason: release.ReasonDeploy, Step: release.StepBuild, Revision: 3, Run: workflow.NewRunID()}
	if err := tx.Do(ctx, func(ctx context.Context) error { return repo.CreateDeployment(ctx, third) }); err != nil {
		t.Fatal(err)
	}
	got, err = repo.FindDeployment(ctx, org, third.ID)
	if err != nil || got.Release != nil || got.Image != "" || got.Build == nil || *got.Build != b || got.Source == nil ||
		*got.Source != *third.Source || got.Config == nil || got.Config.Env["A"] != "1" || got.Status != release.StatusBuilding {
		t.Fatalf("building deployment %+v %v", got, err)
	}
	// a building deployment cannot move on without its release
	if err := repo.SetProgress(ctx, org, third.ID, release.StatusApplying, release.StepApply); err == nil {
		t.Fatal("a release-less deployment moved to apply")
	}
	built := release.Release{ID: release.NewID(), Organization: org, Project: proj, Image: "r.local/built" + digest, Config: cfg}
	err = tx.Do(ctx, func(ctx context.Context) error {
		if err := repo.CreateRelease(ctx, built); err != nil {
			return err
		}
		return repo.SetBuilt(ctx, org, third.ID, built)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = repo.FindDeployment(ctx, org, third.ID)
	if err != nil || got.Release == nil || *got.Release != built.ID || got.Image != built.Image ||
		got.Status != release.StatusPending || got.Step != release.StepApply {
		t.Fatalf("built deployment %+v %v", got, err)
	}
	if err := repo.SetBuilt(ctx, org, third.ID, built); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("set built twice: %v", err)
	}
	if err := repo.SetBuilt(ctx, org, first.ID, built); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("set built on an image deployment: %v", err)
	}
}
