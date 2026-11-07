package releasepg_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasepg"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

func digestOf(c string) string {
	return "r.local/app@sha256:" + c + digest[len("@sha256:")+len(c):]
}

// multi is a version of three services (a worker last, one without a route).
func multi(org identity.OrganizationID, proj project.ID, c string) release.Release {
	img := digestOf(c)
	return release.Release{ID: release.NewID(), Organization: org, Project: proj, Image: img, Config: release.Config{Port: 8080, HealthPath: "/", Replicas: 1},
		Services: []release.Service{
			{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: img, Port: 8080, HealthPath: "/", Replicas: 2},
			{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api", StripPrefix: true, Image: img, Port: 3000, HealthPath: "/h", Replicas: 1},
			{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs", Image: img, Replicas: 3},
		}}
}

// ReleaseServices reads many versions' services in one statement, in their
// order; an unknown id is absent, and another organization's versions are
// never returned.
func TestReleaseServices(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	a, b := multi(org, project.NewID(), "a"), withServices(release.Release{ID: release.NewID(), Organization: org, Project: project.NewID(),
		Image: "r.local/app" + digest, Config: release.Config{Port: 8080, HealthPath: "/", Replicas: 1}})
	for _, r := range []release.Release{a, b} {
		if err := repo.CreateRelease(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ids := []release.ID{a.ID, b.ID, release.NewID()}
	got, err := repo.ReleaseServices(ctx, org, ids)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v %v", got, err)
	}
	if !slices.Equal(got[a.ID], a.Services) || !slices.Equal(got[b.ID], b.Services) {
		t.Fatalf("services %+v, want %+v and %+v", got, a.Services, b.Services)
	}
	if got, err := repo.ReleaseServices(ctx, org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids: %+v %v", got, err)
	}
	if got, err := repo.ReleaseServices(ctx, other, ids); err != nil || len(got) != 0 {
		t.Fatalf("another organization reads %+v %v", got, err)
	}
}

// ListServing lists the services of the version each environment serves, in
// order, in the same read; an environment serving nothing has none, and
// another organization reads nothing.
func TestListServingServices(t *testing.T) {
	db := pgtest.DB(t, migrate.Set{Owner: releasepg.Schema, Files: releasepg.Migrations()})
	repo := releasepg.New(db)
	tx := txn.Postgres{DB: db}
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	proj := project.NewID()
	r := multi(org, proj, "b")
	if err := repo.CreateRelease(ctx, r); err != nil {
		t.Fatal(err)
	}
	live, failing, building := project.NewEnvironmentID(), project.NewEnvironmentID(), project.NewEnvironmentID()
	start := func(env project.EnvironmentID, promote bool) {
		t.Helper()
		d := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: proj, Environment: env, Release: &r.ID, Image: r.Image,
			Status: release.StatusPending, Reason: release.ReasonDeploy, Step: release.StepApply, Revision: 1, Run: workflow.NewRunID()}
		err := tx.Do(ctx, func(ctx context.Context) error {
			if _, err := repo.LockState(ctx, release.EnvironmentState{Environment: env, Organization: org, Project: proj, App: "app"}); err != nil {
				return err
			}
			if err := repo.CreateDeployment(ctx, d); err != nil {
				return err
			}
			if err := repo.Begin(ctx, org, env, d.ID, 1); err != nil {
				return err
			}
			if !promote {
				return nil
			}
			if err := repo.Promote(ctx, org, env, d, "https://"+env.String()+".example"); err != nil {
				return err
			}
			return repo.Finish(ctx, org, d.ID, release.StatusReady, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	start(live, true)
	start(building, false) // nothing online yet
	_ = failing            // never deployed: absent
	list, err := repo.ListServing(ctx, org, []project.EnvironmentID{live, building, failing})
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	by := map[project.EnvironmentID]release.Serving{}
	for _, s := range list {
		by[s.Environment] = s
	}
	want := release.ServiceRefs(r.Services)
	if len(want) != 3 || want[2] != (release.ServiceRef{Name: "jobs", Kind: project.ServiceWorker, Route: ""}) {
		t.Fatalf("refs %+v", want)
	}
	if !slices.Equal(by[live].Services, want) {
		t.Fatalf("live serves %+v, want %+v", by[live].Services, want)
	}
	if s := by[building].Services; s == nil || len(s) != 0 {
		t.Fatalf("nothing online: %#v, want []", s)
	}
	if list, err := repo.ListServing(ctx, other, []project.EnvironmentID{live, building}); err != nil || len(list) != 0 {
		t.Fatalf("another organization reads %+v %v", list, err)
	}
}
