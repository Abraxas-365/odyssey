package catalogsvc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogsvc"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogtest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

var user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

// every capability the factory recipes need
var modern = []string{resourcespec.Builtin(resourcekind.Postgres), resourcespec.Builtin(resourcekind.Redis),
	resourcespec.Builtin(resourcekind.Files), resourcespec.SpecV1, resourcespec.PostgresInstances,
	resourcespec.PostgresVersion, resourcespec.PostgresResources}

// an agent from before the catalog: the kinds only
var old = []string{resourcespec.Builtin(resourcekind.Postgres), resourcespec.Builtin(resourcekind.Redis), resourcespec.Builtin(resourcekind.Files)}

func setup() (*catalogsvc.Service, *eventtest.Recorder, *catalogtest.Repo) {
	events, repo := &eventtest.Recorder{}, catalogtest.NewRepo()
	return catalogsvc.New(repo, txn.Passthrough{}, events), events, repo
}

func code(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestFirstReadSeedsTheFactoryOfferingsOnce(t *testing.T) {
	svc, events, repo := setup()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	entries, err := svc.List(context.Background(), org)
	if err != nil || len(entries) != 4 {
		t.Fatalf("list: %d, %v", len(entries), err)
	}
	if _, err := svc.List(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if rows, _ := repo.List(context.Background(), org); len(rows) != 4 {
		t.Fatalf("seeded %d offerings", len(rows))
	}
	if rows, _ := repo.List(context.Background(), other); len(rows) != 0 {
		t.Fatalf("seeded another organization: %d", len(rows))
	}
	if len(events.Events()) != 0 {
		t.Fatalf("seeding published %v", events.Types())
	}
}

// The basic recipes are what existed before the catalog: offered in every
// tier, the default, any size of today's range, without asking.
func TestBasicRecipesKeepTodaysBehaviour(t *testing.T) {
	svc, _, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	for _, k := range resourcekind.Kinds {
		for _, tier := range []project.Tier{project.TierSandbox, project.TierInternal} {
			r, err := svc.Resolve(ctx, org, catalog.Request{Kind: k, Tier: tier, Capabilities: old,
				Params: map[string]int{catalog.ParamStorage: resourcekind.MaxStorageGi}})
			if err != nil || r.Recipe != catalog.BasicRecipe(k) || r.Version != 1 || r.NeedsApproval() || !r.Spec.IsZero() ||
				r.Params[catalog.ParamStorage] != resourcekind.MaxStorageGi {
				t.Fatalf("%s %s: %+v %v", k, tier, r, err)
			}
		}
		r, err := svc.Resolve(ctx, org, catalog.Request{Kind: k, Tier: project.TierInternal, Capabilities: old})
		if err != nil || r.Params[catalog.ParamStorage] != resourcekind.DefaultStorageGi {
			t.Fatalf("%s default size: %+v %v", k, r, err)
		}
	}
}

func TestProductionTierHasTwoPostgresRecipesAndTheBasicIsTheDefault(t *testing.T) {
	svc, _, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	r, err := svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierProduction, Capabilities: modern})
	if err != nil || r.Recipe != catalog.PostgresBasic {
		t.Fatalf("default: %+v %v", r, err)
	}
	r, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierProduction, Capabilities: modern})
	if err != nil || r.Recipe != catalog.PostgresProduction || r.Params[catalog.ParamStorage] != 10 || r.NeedsApproval() ||
		r.Spec.Postgres == nil || r.Spec.Postgres.Instances != 2 {
		t.Fatalf("production: %+v %v", r, err)
	}
	// outside the pre-approved range: IT approves
	r, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierProduction, Capabilities: modern, Params: map[string]int{catalog.ParamStorage: 80}})
	if err != nil || !slices.Equal(r.OutOfRange, []string{catalog.ParamStorage}) {
		t.Fatalf("out of range: %+v %v", r, err)
	}
	// below the hard limit: refused, not even with approval
	_, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierProduction, Capabilities: modern, Params: map[string]int{catalog.ParamStorage: 2}})
	if code(err) != catalog.CodeOutOfLimits || !strings.Contains(err.Error(), "entre 5 y 100 GB.") {
		t.Fatalf("out of limits: %v", err)
	}
	// not offered in another tier
	_, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierSandbox, Capabilities: modern})
	if code(err) != catalog.CodeUnavailable {
		t.Fatalf("sandbox: %v", err)
	}
	// an agent from before the catalog cannot honour it
	_, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierProduction, Capabilities: old})
	if code(err) != catalog.CodeUnavailable {
		t.Fatalf("old agent: %v", err)
	}
	// a pinned version that is not the offered one
	_, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction, Version: 2,
		Tier: project.TierProduction, Capabilities: modern})
	if code(err) != catalog.CodeRecipeChanged {
		t.Fatalf("pinned: %v", err)
	}
	// unknown parameter, wrong kind
	if _, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierInternal, Capabilities: modern,
		Params: map[string]int{"replicas": 3}}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("unknown param: %v", err)
	}
	if _, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Redis, Recipe: catalog.PostgresBasic, Tier: project.TierInternal,
		Capabilities: modern}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("wrong kind: %v", err)
	}
}

func TestAvailableListsWhatTheTierAndClusterCanUseDefaultsFirst(t *testing.T) {
	svc, _, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	recipes := func(tier project.Tier, caps []string) []string {
		entries, err := svc.Available(ctx, org, tier, caps)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Recipe)
		}
		return out
	}
	if got := recipes(project.TierProduction, modern); !slices.Equal(got,
		[]string{catalog.PostgresBasic, catalog.PostgresProduction, catalog.RedisBasic, catalog.FilesBasic}) {
		t.Fatalf("production: %v", got)
	}
	if got := recipes(project.TierProduction, old); !slices.Equal(got, []string{catalog.PostgresBasic, catalog.RedisBasic, catalog.FilesBasic}) {
		t.Fatalf("old agent: %v", got)
	}
	if got := recipes(project.TierSandbox, modern); slices.Contains(got, catalog.PostgresProduction) {
		t.Fatalf("sandbox: %v", got)
	}
	// IT makes production postgres the default: it goes first
	_, err := svc.UpdateOffering(ctx, org, catalog.PostgresBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction},
			Defaults: []project.Tier{project.TierSandbox, project.TierInternal}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction},
			Defaults: []project.Tier{project.TierProduction}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := recipes(project.TierProduction, modern); got[0] != catalog.PostgresProduction {
		t.Fatalf("new default first: %v", got)
	}
	r, err := svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierProduction, Capabilities: modern})
	if err != nil || r.Recipe != catalog.PostgresProduction {
		t.Fatalf("resolves the new default: %+v %v", r, err)
	}
}

func TestUpdateOffering(t *testing.T) {
	svc, events, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	all := []project.Tier{project.TierProduction, project.TierSandbox, project.TierInternal, project.TierSandbox}

	// a second default in a tier is a conflict: the tie is refused on save
	_, err := svc.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: all, Defaults: []project.Tier{project.TierInternal}}})
	if code(err) != catalog.CodeDefaultTaken {
		t.Fatalf("tie: %v", err)
	}

	// limits and pre-approved range inside the schema; normalized tiers
	e, err := svc.UpdateOffering(ctx, org, catalog.PostgresBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: all, Defaults: all,
			Limits:      map[string]catalog.Range{catalog.ParamStorage: {Max: 20}},
			Preapproved: map[string]catalog.Range{catalog.ParamStorage: {Max: 5}}}})
	if err != nil || e.Offering.Revision != 2 || !slices.Equal(e.Offering.Tiers,
		[]project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction}) || e.Offering.UpdatedBy != user {
		t.Fatalf("update: %+v %v", e.Offering, err)
	}
	if got := events.Types(); !slices.Equal(got, []string{catalog.EventOfferingUpdated}) {
		t.Fatalf("events %v", got)
	}
	// not retroactive here, but new requests follow it
	_, err = svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierInternal, Capabilities: old,
		Params: map[string]int{catalog.ParamStorage: 30}})
	if code(err) != catalog.CodeOutOfLimits {
		t.Fatalf("new limit: %v", err)
	}
	r, err := svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierInternal, Capabilities: old,
		Params: map[string]int{catalog.ParamStorage: 10}})
	if err != nil || !r.NeedsApproval() {
		t.Fatalf("new range: %+v %v", r, err)
	}

	bad := []catalog.OfferingDraft{
		{Version: 9, Active: true, Tiers: all},
		{Version: 1, Active: true},
		{Version: 1, Active: true, Tiers: []project.Tier{"gold"}},
		{Version: 1, Active: true, Tiers: []project.Tier{project.TierSandbox}, Defaults: []project.Tier{project.TierInternal}},
		{Version: 1, Active: true, Tiers: all, Limits: map[string]catalog.Range{"replicas": {Max: 2}}},
		{Version: 1, Active: true, Tiers: all, Limits: map[string]catalog.Range{catalog.ParamStorage: {Max: 500}}},
		{Version: 1, Active: true, Tiers: all, Limits: map[string]catalog.Range{catalog.ParamStorage: {Min: 50, Max: 10}}},
		{Version: 1, Active: true, Tiers: all, Limits: map[string]catalog.Range{catalog.ParamStorage: {Max: 10}},
			Preapproved: map[string]catalog.Range{catalog.ParamStorage: {Min: 20}}},
	}
	for i, d := range bad {
		if _, err := svc.UpdateOffering(ctx, org, catalog.RedisBasic, catalog.UpdateOffering{OfferingDraft: d, UpdatedBy: user}); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("bad draft %d: %v", i, err)
		}
	}
	if _, err := svc.UpdateOffering(ctx, org, "mysql", catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1}}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("unknown recipe: %v", err)
	}

	// turning it off: nobody's default, and the kind is unavailable in the tier
	e, err = svc.UpdateOffering(ctx, org, catalog.RedisBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: false, Tiers: all, Defaults: all}})
	if err != nil || len(e.Offering.Defaults) != 0 {
		t.Fatalf("off: %+v %v", e.Offering, err)
	}
	if _, err := svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Redis, Tier: project.TierInternal, Capabilities: old}); code(err) != catalog.CodeUnavailable {
		t.Fatalf("off resolves: %v", err)
	}
}

func TestResolveChangeFollowsMutability(t *testing.T) {
	svc, _, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	ch := func(cur, want int) (catalog.Resolved, error) {
		return svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1, Tier: project.TierProduction,
			Current: map[string]int{catalog.ParamStorage: cur}, Params: map[string]int{catalog.ParamStorage: want}})
	}
	if r, err := ch(10, 20); err != nil || r.NeedsApproval() || r.Params[catalog.ParamStorage] != 20 {
		t.Fatalf("grow: %+v %v", r, err)
	}
	if _, err := ch(20, 10); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("shrink: %v", err)
	}
	if r, err := ch(10, 60); err != nil || !r.NeedsApproval() {
		t.Fatalf("grow out of range: %+v %v", r, err)
	}
	// a value IT already approved is not asked again
	if r, err := ch(60, 60); err != nil || r.NeedsApproval() {
		t.Fatalf("same: %+v %v", r, err)
	}
	if _, err := ch(10, 101); code(err) != catalog.CodeOutOfLimits {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 7}); code(err) != catalog.CodeRecipeChanged {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1,
		Current: map[string]int{catalog.ParamStorage: 10}, Params: map[string]int{"cpu": 2}}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("unknown parameter: %v", err)
	}
}

// Only what changes is judged: a value the resource already has stays even
// when IT narrowed the offering since, and a parameter the resource lacks
// (a version that added it) takes its default without being a change.
func TestResolveChangeJudgesOnlyWhatChanges(t *testing.T) {
	svc, _, _ := setup()
	ctx, org := context.Background(), identity.NewOrganizationID()
	if _, err := svc.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction},
			Limits: map[string]catalog.Range{catalog.ParamStorage: {Min: 5, Max: 30}}}}); err != nil {
		t.Fatal(err)
	}
	// 60 GiB was fine when it was created; the limit is now 30
	r, err := svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1,
		Current: map[string]int{catalog.ParamStorage: 60}, Params: map[string]int{catalog.ParamStorage: 60}})
	if err != nil || r.NeedsApproval() || r.Params[catalog.ParamStorage] != 60 {
		t.Fatalf("unchanged value outside today's limits: %+v %v", r, err)
	}
	// growing past today's limit is refused
	if _, err := svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1,
		Current: map[string]int{catalog.ParamStorage: 60}, Params: map[string]int{catalog.ParamStorage: 70}}); code(err) != catalog.CodeOutOfLimits {
		t.Fatalf("grow past the limit: %v", err)
	}
	// a resource without the parameter: it takes the default, nothing to approve
	r, err = svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1, Current: map[string]int{}})
	if err != nil || r.NeedsApproval() || r.Params[catalog.ParamStorage] != 10 {
		t.Fatalf("missing parameter: %+v %v", r, err)
	}
}
