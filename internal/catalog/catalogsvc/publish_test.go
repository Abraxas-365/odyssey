package catalogsvc_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogsvc"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogtest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// stores is a catalog.Fleet whose clusters announce the given stores.
type stores []catalog.Store

func ofType(r *eventtest.Recorder, typ string) []event.Event {
	var out []event.Event
	for _, e := range r.Events() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (s stores) SecretStores(context.Context, identity.OrganizationID) ([]catalog.Store, error) {
	return slices.Clone(s), nil
}

func withStores(names ...string) (*catalogsvc.Service, *eventtest.Recorder, *catalogtest.Repo) {
	svc, events, repo := setup()
	var announced stores
	for _, n := range names {
		announced = append(announced, catalog.Store{Name: n, Clusters: []catalog.StoreCluster{{ID: "c1", Name: "lima"}}})
	}
	svc.UseFleet(announced)
	return svc, events, repo
}

func register(recipe string, existing resourcespec.Spec) catalog.RegisterRecipe {
	return catalog.RegisterRecipe{Recipe: recipe, Kind: resourcekind.Postgres, PublishedBy: user, MayReadStores: true,
		RecipeDraft: catalog.RecipeDraft{Title: "Base de reportes", Description: "La RDS de reportes", Existing: existing.Existing}}
}

// Registering publishes v1 with an offering nobody can use yet, and the
// trail says who pointed a recipe at which store.
func TestRegisterRecipe(t *testing.T) {
	svc, events, _ := withStores("vault")
	ctx, org := context.Background(), identity.NewOrganizationID()
	e, err := svc.RegisterRecipe(ctx, org, register(" org-reportes ", resourcespec.ExamplePostgres("vault")))
	if err != nil {
		t.Fatal(err)
	}
	if e.Recipe != "org-reportes" || e.Version != 1 || e.Engine != resourcespec.EngineExisting || e.Offering.Active ||
		len(e.Offering.Tiers) != 0 || e.Offering.Version != 1 || e.Offering.UpdatedBy != user || !slices.Equal(e.Versions, []int{1}) {
		t.Fatalf("registered: %+v", e)
	}
	// nobody can create it until IT offers it
	if got, _ := svc.Available(ctx, org, project.TierProduction, []string{resourcespec.ConnectExisting, resourcespec.SecretStore("vault")}, user); slices.ContainsFunc(got,
		func(x catalog.Entry) bool { return x.Recipe == "org-reportes" }) {
		t.Fatal("an inactive offering is available")
	}
	pub := ofType(events, catalog.EventRecipePublished)
	if len(pub) != 1 {
		t.Fatalf("events: %d", len(pub))
	}
	var p catalog.RecipePublished
	if err := json.Unmarshal(pub[0].Payload, &p); err != nil || p.Recipe != "org-reportes" || p.Version != 1 || !slices.Equal(p.Stores, []string{"vault"}) {
		t.Fatalf("payload: %+v %v", p, err)
	}
	// IT then offers it like any other recipe
	if _, err := svc.UpdateOffering(ctx, org, "org-reportes", catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction}}}); err != nil {
		t.Fatal(err)
	}
	// an id already taken is a conflict, and no event is written
	if _, err := svc.RegisterRecipe(ctx, org, register("org-reportes", resourcespec.ExamplePostgres("vault"))); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("registered twice: %v", err)
	}
	if n := len(ofType(events, catalog.EventRecipePublished)); n != 1 {
		t.Fatalf("events after a refusal: %d", n)
	}
	// another organization may use the same id
	if _, err := svc.RegisterRecipe(ctx, identity.NewOrganizationID(), register("org-reportes", resourcespec.ExamplePostgres("vault"))); err != nil {
		t.Fatalf("same id elsewhere: %v", err)
	}
}

// P0-2: a store is readable by any ExternalSecret of its cluster, so a recipe
// reading one needs odyssey:secrets:external, and only a store a cluster of
// the organization announces; a connection with no secret needs neither.
func TestRegisterRecipeStores(t *testing.T) {
	svc, events, _ := withStores("vault")
	ctx, org := context.Background(), identity.NewOrganizationID()
	in := register("org-reportes", resourcespec.ExamplePostgres("vault"))
	in.MayReadStores = false
	if _, err := svc.RegisterRecipe(ctx, org, in); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("without secrets:external: %v", err)
	}
	if _, err := svc.RegisterRecipe(ctx, org, register("org-reportes", resourcespec.ExamplePostgres("vualt"))); code(err) != catalog.CodeStoreUnknown {
		t.Fatalf("unknown store: %v", err)
	}
	// without a fleet no store is known
	bare, _, _ := setup()
	if _, err := bare.RegisterRecipe(ctx, org, register("org-reportes", resourcespec.ExamplePostgres("vault"))); code(err) != catalog.CodeStoreUnknown {
		t.Fatalf("without a fleet: %v", err)
	}
	if got := events.Events(); len(got) != 0 {
		t.Fatalf("refusals wrote %d events", len(got))
	}
	// a bucket read with the app's own identity has no secret, so no store:
	// it needs neither the permission nor a fleet
	bucket := catalog.RegisterRecipe{Recipe: "org-bucket", Kind: resourcekind.ObjectStorage, PublishedBy: user,
		RecipeDraft: catalog.RecipeDraft{Title: "Bucket de reportes", Existing: &resourcespec.Existing{Values: []resourcespec.Value{
			{Variable: "S3_BUCKET", Value: "reportes"}, {Variable: "S3_REGION", Value: "us-east-1"}}}}}
	if _, err := bare.RegisterRecipe(ctx, org, bucket); err != nil {
		t.Fatalf("no store: %v", err)
	}
}

// What is registered is validated as any published version: the id, the
// kind, a connection, and only a resource the company already has.
func TestRegisterRecipeValidates(t *testing.T) {
	svc, _, _ := withStores("vault")
	ctx, org := context.Background(), identity.NewOrganizationID()
	for name, in := range map[string]catalog.RegisterRecipe{
		"factory-like id": register("postgres-reportes", resourcespec.ExamplePostgres("vault")),
		"no connection":   register("org-reportes", resourcespec.Spec{}),
		"no title": func() catalog.RegisterRecipe {
			in := register("org-reportes", resourcespec.ExamplePostgres("vault"))
			in.Title = " "
			return in
		}(),
		"files": func() catalog.RegisterRecipe {
			in := register("org-archivos", resourcespec.ExamplePostgres("vault"))
			in.Kind = resourcekind.Files
			return in
		}(),
		"no publisher": func() catalog.RegisterRecipe {
			in := register("org-reportes", resourcespec.ExamplePostgres("vault"))
			in.PublishedBy = identity.UserID{}
			return in
		}(),
	} {
		if _, err := svc.RegisterRecipe(ctx, org, in); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if entries, _ := svc.List(ctx, org); len(entries) != len(catalog.Factory().Latest()) {
		t.Fatalf("a refused recipe was stored: %d entries", len(entries))
	}
}

// A new version keeps the kind and engine, is not offered until IT moves
// the offering to it, and leaves the offered version as it is.
func TestPublishVersion(t *testing.T) {
	svc, events, _ := withStores("vault", "aws")
	ctx, org := context.Background(), identity.NewOrganizationID()
	if _, err := svc.RegisterRecipe(ctx, org, register("org-reportes", resourcespec.ExamplePostgres("vault"))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateOffering(ctx, org, "org-reportes", catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction}}}); err != nil {
		t.Fatal(err)
	}
	next := catalog.PublishVersion{PublishedBy: user, MayReadStores: true,
		RecipeDraft: catalog.RecipeDraft{Title: "Base de reportes (AWS)", Existing: resourcespec.ExamplePostgres("aws").Existing}}
	e, err := svc.PublishVersion(ctx, org, "org-reportes", next)
	if err != nil {
		t.Fatal(err)
	}
	// the entry is what is offered: still v1, with v2 to choose
	if e.Version != 1 || e.Kind != resourcekind.Postgres || e.Offering.Version != 1 || !e.Offering.Active || !slices.Equal(e.Versions, []int{1, 2}) {
		t.Fatalf("after v2: %+v", e)
	}
	v2, ok, _ := svc.Version(ctx, org, "org-reportes", 2)
	if !ok || v2.Title != "Base de reportes (AWS)" || v2.Engine != resourcespec.EngineExisting || !slices.Contains(v2.Requires, resourcespec.SecretStore("aws")) {
		t.Fatalf("v2: %+v %v", v2, ok)
	}
	pub := ofType(events, catalog.EventRecipePublished)
	if len(pub) != 2 || pub[1].Summary != "Publicó una versión nueva de «Base de reportes (AWS)» en el catálogo" {
		t.Fatalf("events: %+v", pub)
	}
	// the stores are checked again
	next.MayReadStores = false
	if _, err := svc.PublishVersion(ctx, org, "org-reportes", next); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("v3 without secrets:external: %v", err)
	}
	// a factory recipe gets versions only with Odicea, an unknown one is 404,
	// and another organization's is unknown
	next.MayReadStores = true
	if _, err := svc.PublishVersion(ctx, org, catalog.PostgresBasic, next); !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("factory: %v", err)
	}
	if _, err := svc.PublishVersion(ctx, org, "org-nada", next); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := svc.PublishVersion(ctx, identity.NewOrganizationID(), "org-reportes", next); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization's: %v", err)
	}
}

// Stores are what the fleet reports (none without one).
func TestStores(t *testing.T) {
	ctx, org := context.Background(), identity.NewOrganizationID()
	bare, _, _ := setup()
	if got, err := bare.Stores(ctx, org); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("without a fleet: %#v %v", got, err)
	}
	svc, _, _ := withStores("vault")
	if got, err := svc.Stores(ctx, org); err != nil || len(got) != 1 || got[0].Name != "vault" || got[0].Clusters[0].Name != "lima" {
		t.Fatalf("stores: %+v %v", got, err)
	}
}
