package resourcesvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/resourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

var (
	org  = identity.NewOrganizationID()
	user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
)

// projects answers place: one active production app with one environment.
type projects struct {
	project.Queries
	p project.Project
	e project.Environment
}

func (f projects) Find(context.Context, identity.OrganizationID, project.ID) (project.Project, error) {
	return f.p, nil
}

func (f projects) FindEnvironment(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (project.Environment, error) {
	return f.e, nil
}

// repo holds one resource; bumpOnLock simulates a writer that changed it
// between judging and the transaction.
type repo struct {
	resource.Repository
	r          resource.Resource
	bumpOnLock bool
	changed    int
}

func (f *repo) Find(context.Context, identity.OrganizationID, project.EnvironmentID, string) (resource.Resource, error) {
	return f.r, nil
}

func (f *repo) FindForUpdate(context.Context, identity.OrganizationID, project.EnvironmentID, string) (resource.Resource, error) {
	if f.bumpOnLock {
		f.r.Generation++
	}
	return f.r, nil
}

func (f *repo) Change(_ context.Context, _ identity.OrganizationID, _ project.EnvironmentID, _ string, params map[string]int, gen int64) error {
	f.changed++
	f.r.Params, f.r.Generation = params, gen
	return nil
}

type cluster struct {
	resource.Cluster
	applied int
	last    resource.Desired
}

func (c *cluster) Apply(_ context.Context, d resource.Desired) error {
	c.applied++
	c.last = d
	return nil
}

func (c *cluster) Observed(context.Context, identity.OrganizationID, project.EnvironmentID, string) (resource.Observed, error) {
	return resource.Observed{}, nil
}

type usage struct{ resource.Usage }

func (usage) Connections(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) ([]resource.Connection, error) {
	return nil, nil
}

// cat resolves a change by taking the values asked over the current ones
// (the real rules are catalogsvc's, tested there). It knows the factory and
// one organization's own recipe, org-reportes@1, as Queries.Version does.
type cat struct{ resource.Catalog }

var reportes = catalog.OrgRecipe{Organization: org, Recipe: "org-reportes", Version: 1, Kind: resourcekind.Postgres,
	Title: "Reportes", Engine: resourcespec.EngineExisting, Spec: resourcespec.ExamplePostgres("vault"), CreatedBy: user}

func (cat) Version(_ context.Context, o identity.OrganizationID, recipe string, version int) (catalog.RecipeVersion, bool, error) {
	if o == org {
		if v, ok := catalog.Factory().With([]catalog.OrgRecipe{reportes}).Version(recipe, version); ok {
			return v, true, nil
		}
	}
	v, ok := catalog.Factory().Version(recipe, version)
	return v, ok, nil
}

func (cat) ResolveChange(_ context.Context, _ identity.OrganizationID, ch catalog.Change) (catalog.Resolved, error) {
	out := map[string]int{}
	for k, v := range ch.Current {
		out[k] = v
	}
	for k, v := range ch.Params {
		out[k] = v
	}
	return catalog.Resolved{Kind: resourcekind.Postgres, Recipe: ch.Recipe, Version: ch.Version, Params: out}, nil
}

type allow struct{ checked int }

func (a *allow) Check(context.Context, identity.OrganizationID, gate.Request) (gate.Verdict, error) {
	a.checked++
	return gate.Verdict{Effect: gate.Allow}, nil
}

type fixture struct {
	svc     *resourcesvc.Service
	repo    *repo
	cluster *cluster
	policy  *allow
	events  *eventtest.Recorder
	p       project.Project
	e       project.Environment
}

func setup() fixture {
	p := project.Project{ID: project.NewID(), Organization: org, Name: "Ventas", Slug: "ventas", Status: project.StatusActive, Tier: project.TierProduction}
	e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Organization: org, Slug: "production"}
	r := resource.Resource{ID: resource.NewID(), Organization: org, Project: p.ID, Environment: e.ID, Name: "db", Kind: resourcekind.Postgres,
		StorageGi: 5, Recipe: catalog.PostgresBasic, RecipeVersion: 1, Params: map[string]int{catalog.ParamStorage: 5},
		Generation: 3, Status: resource.StatusActive, CreatedBy: user}
	f := fixture{repo: &repo{r: r}, cluster: &cluster{}, policy: &allow{}, events: &eventtest.Recorder{}, p: p, e: e}
	f.svc = resourcesvc.New(f.repo, f.cluster, usage{}, projects{p: p, e: e}, cat{}, txn.Passthrough{}, f.events, f.policy)
	return f
}

func (f fixture) change(params map[string]int) (resource.View, error) {
	return f.svc.Change(context.Background(), org, f.p.ID, f.e.ID, "db", resource.Change{Params: params, RequestedBy: user})
}

func TestChangeGrowsAtANewGeneration(t *testing.T) {
	f := setup()
	if _, err := f.change(map[string]int{catalog.ParamStorage: 8}); err != nil {
		t.Fatal(err)
	}
	if f.repo.changed != 1 || f.cluster.applied != 1 || f.repo.r.Generation != 4 || f.repo.r.Params[catalog.ParamStorage] != 8 {
		t.Fatalf("changed %d, applied %d, now %+v", f.repo.changed, f.cluster.applied, f.repo.r)
	}
	if got := f.events.Types(); len(got) != 1 || got[0] != resource.EventResized {
		t.Fatalf("events: %v", got)
	}
	if f.cluster.last.Recipe != "postgres-basico@1" {
		t.Fatalf("applied %+v", f.cluster.last)
	}
}

// A resource pinned to its organization's own recipe is applied with that
// version, read through the catalog for its organization (not the
// factory's alone, which does not know it).
func TestApplyReadsTheOrganizationsOwnRecipe(t *testing.T) {
	f := setup()
	f.repo.r.Recipe, f.repo.r.RecipeVersion = reportes.Recipe, reportes.Version
	if _, err := f.change(map[string]int{catalog.ParamStorage: 8}); err != nil {
		t.Fatal(err)
	}
	if f.cluster.applied != 1 || f.cluster.last.Recipe != "org-reportes@1" {
		t.Fatalf("applied %d: %+v", f.cluster.applied, f.cluster.last)
	}
}

// What was judged must still be what is there: a resource that changed
// between judging and locking is refused (409 RESOURCE_CHANGED), nothing
// stored, applied or published.
func TestChangeRefusesWhatChangedMeanwhile(t *testing.T) {
	f := setup()
	f.repo.bumpOnLock = true
	_, err := f.change(map[string]int{catalog.ParamStorage: 8})
	var e *errx.Error
	if !errors.As(err, &e) || e.Code != resource.CodeChanged || !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("want 409 %s, got %v", resource.CodeChanged, err)
	}
	if f.repo.changed != 0 || f.cluster.applied != 0 || len(f.events.Events()) != 0 {
		t.Fatalf("changed %d, applied %d, events %v", f.repo.changed, f.cluster.applied, f.events.Types())
	}
}

// The same values change nothing and ask no one; a parameter the resource
// lacks is not a change asked for.
func TestChangeWithTheSameValuesIsANoOp(t *testing.T) {
	f := setup()
	f.repo.r.Params = map[string]int{catalog.ParamStorage: 5}
	if _, err := f.change(map[string]int{catalog.ParamStorage: 5}); err != nil {
		t.Fatal(err)
	}
	if f.policy.checked != 0 || f.repo.changed != 0 || f.cluster.applied != 0 || len(f.events.Events()) != 0 {
		t.Fatalf("checked %d, changed %d, applied %d, events %v", f.policy.checked, f.repo.changed, f.cluster.applied, f.events.Types())
	}
}
