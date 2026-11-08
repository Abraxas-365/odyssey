package resourcesvc_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/resourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// manyRepo lists a project's resources.
type manyRepo struct {
	resource.Repository
	rows []resource.Resource
}

func (f manyRepo) ListProject(context.Context, identity.OrganizationID, project.ID) ([]resource.Resource, error) {
	return f.rows, nil
}

// listing offers every factory recipe in every tier, and no organization
// recipe in production.
type listing struct{ cat }

func (listing) List(context.Context, identity.OrganizationID) ([]catalog.Entry, error) {
	var out []catalog.Entry
	for _, v := range catalog.Factory().Latest() {
		d := v.Factory
		d.Active, d.Tiers = true, []project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction}
		out = append(out, catalog.Entry{RecipeVersion: v, Offering: catalog.Offering{Recipe: v.Recipe, OfferingDraft: d}})
	}
	return out, nil
}

// A resource the company already has strands a tier change even when its
// kind holds no data: in the new tier the app would point somewhere else.
// A built-in one of such a kind can be made again.
func TestStrandedCountsTheCompanysResourcesAlways(t *testing.T) {
	p := project.Project{ID: project.NewID(), Organization: org, Status: project.StatusActive, Tier: project.TierInternal}
	row := func(name string, kind resourcekind.Kind, recipe string) resource.Resource {
		return resource.Resource{ID: resource.NewID(), Organization: org, Project: p.ID, Name: name, Kind: kind,
			Recipe: recipe, RecipeVersion: 1, Status: resource.StatusActive}
	}
	rows := []resource.Resource{
		row("cache", resourcekind.Redis, catalog.RedisBasic),     // built-in, no data, offered
		row("sesiones", resourcekind.Redis, "org-redis-empresa"), // the company's: not offered in production
	}
	svc := resourcesvc.New(manyRepo{rows: rows}, &cluster{}, usage{}, projects{p: p}, listing{}, txn.Passthrough{}, &eventtest.Recorder{}, &allow{})
	got, err := svc.Stranded(context.Background(), org, p.ID, project.TierProduction)
	if err != nil || len(got) != 1 || got[0].Name != "sesiones" {
		t.Fatalf("stranded %+v %v", got, err)
	}
}
