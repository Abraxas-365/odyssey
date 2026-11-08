package catalog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// Every factory version is sound: a known kind, a valid spec whose needs
// are what it declares, parameters with a default inside their bounds, and
// a factory offering that validates and keeps that default.
func TestFactoryVersionsAreSound(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range catalog.Factory() {
		if seen[v.Ref()] {
			t.Errorf("%s twice", v.Ref())
		}
		seen[v.Ref()] = true
		if strings.HasPrefix(v.Recipe, catalog.OrgPrefix) {
			t.Errorf("%s: %q is the prefix of the organizations' own recipes", v.Ref(), catalog.OrgPrefix)
		}
		if catalog.CheckRecipeID(v.Recipe) == nil {
			t.Errorf("%s would be a valid organization recipe id", v.Ref())
		}
		if _, ok := resourcekind.Lookup(v.Kind); !ok {
			t.Errorf("%s: unknown kind %s", v.Ref(), v.Kind)
		}
		if err := v.Spec.Validate(v.Kind); err != nil {
			t.Errorf("%s: %v", v.Ref(), err)
		}
		if got, want := v.Requires, v.Spec.Requires(v.Kind); !equal(got, want) {
			t.Errorf("%s requires %v, its spec needs %v", v.Ref(), got, want)
		}
		if _, ok := v.Param(catalog.ParamStorage); !ok {
			// Today a resource's size is a column (resource.resources.storage_gi,
			// with resources_params_check) and resourcepg.Change needs it. A
			// recipe without one relaxes those first (a nullable column), then
			// this check.
			t.Errorf("%s has no %s (the resource's size, which resourcepg still requires)", v.Ref(), catalog.ParamStorage)
		}
		for _, p := range v.Params {
			if p.Default < p.Min || p.Default > p.Max {
				t.Errorf("%s.%s: default %d outside [%d, %d]", v.Ref(), p.Name, p.Default, p.Min, p.Max)
			}
		}
		if v.Version == 1 {
			if err := v.Factory.Validate(v); err != nil {
				t.Errorf("%s factory offering: %v", v.Ref(), err)
			}
			o := catalog.Offering{OfferingDraft: v.Factory}
			if _, _, err := catalog.Check(v, o, nil); err != nil {
				t.Errorf("%s: its defaults are outside its factory limits: %v", v.Ref(), err)
			}
		}
	}
	for _, k := range resourcekind.Kinds {
		if _, ok := catalog.Factory().Version(catalog.BasicRecipe(k), 1); !ok {
			t.Errorf("%s has no basic recipe (what existing resources are pinned to)", k)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A released version never changes: resources and approvals are pinned to
// it. testdata/versions.json is every version as released; a new version is
// added there (UPDATE_GOLDEN=1), a changed one fails.
func TestReleasedVersionsNeverChange(t *testing.T) {
	path := filepath.Join("testdata", "versions.json")
	current := map[string]json.RawMessage{}
	for _, v := range catalog.Factory() {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		current[v.Ref()] = raw
	}
	released := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &released); err != nil {
			t.Fatal(err)
		}
	}
	changed := false
	for ref, was := range released {
		now, ok := current[ref]
		switch {
		case !ok:
			t.Errorf("%s was released and is gone: a version is never removed", ref)
		case compact(t, now) != compact(t, was):
			t.Errorf("%s was released and changed: add a new version instead\nwas %s\nnow %s", ref, compact(t, was), compact(t, now))
		}
	}
	for ref := range current {
		if _, ok := released[ref]; !ok {
			changed = true
			if os.Getenv("UPDATE_GOLDEN") == "" {
				t.Errorf("%s is new: record it with UPDATE_GOLDEN=1 go test ./internal/catalog/", ref)
			}
		}
	}
	if changed && os.Getenv("UPDATE_GOLDEN") != "" && !t.Failed() {
		for ref, raw := range current {
			released[ref] = raw
		}
		data, err := json.MarshalIndent(released, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func compact(t *testing.T, raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestOfferingRanges(t *testing.T) {
	v, _ := catalog.Factory().Version(catalog.PostgresProduction, 1)
	p, _ := v.Param(catalog.ParamStorage)
	o := catalog.Offering{OfferingDraft: v.Factory}
	if b := o.Bounds(p); b != (catalog.Range{Min: 5, Max: resourcekind.MaxStorageGi}) {
		t.Fatalf("bounds %+v", b)
	}
	if r := o.PreapprovedRange(p); r != (catalog.Range{Min: 5, Max: 50}) {
		t.Fatalf("pre-approved %+v", r)
	}
	// a default below the offering's floor is lifted to it
	o.Limits = map[string]catalog.Range{catalog.ParamStorage: {Min: 20}}
	if d := o.DefaultOf(p); d != 20 {
		t.Fatalf("default %d", d)
	}
	if !o.Offers(project.TierProduction) || o.Offers(project.TierSandbox) || o.DefaultIn(project.TierProduction) {
		t.Fatalf("tiers %+v", o.OfferingDraft)
	}
}

// The payloads are the public contract: see testdata/events.
func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	v, _ := catalog.Factory().Version(catalog.PostgresProduction, 1)
	o := catalog.Offering{Recipe: v.Recipe, OfferingDraft: v.Factory.Normalize(), Revision: 3}
	o.Teams = []string{"grp_datos"}
	for _, e := range []event.Event{catalog.NewOfferingUpdated(org, user, v.Title, o)} {
		eventtest.Golden(t, e)
	}
	o.Active = false
	if e := catalog.NewOfferingUpdated(org, user, v.Title, o); e.Summary != "Dejó de ofrecer «PostgreSQL de producción» en el catálogo" {
		t.Fatalf("summary %q", e.Summary)
	}
}

func TestOrgRecipeValidate(t *testing.T) {
	ok := catalog.OrgRecipe{Organization: identity.NewOrganizationID(), Recipe: "org-rds-reportes", Version: 1,
		Kind: resourcekind.Postgres, Title: "Reportes", Engine: resourcespec.EngineExisting,
		CreatedBy: identity.MustParseUserID("00000000-0000-4000-8000-000000000002")}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*catalog.OrgRecipe){
		"factory-like id":  func(o *catalog.OrgRecipe) { o.Recipe = "rds-reportes" },
		"uppercase id":     func(o *catalog.OrgRecipe) { o.Recipe = "org-RDS" },
		"empty after org-": func(o *catalog.OrgRecipe) { o.Recipe = "org-" },
		"long id":          func(o *catalog.OrgRecipe) { o.Recipe = "org-" + strings.Repeat("a", catalog.MaxRecipeID) },
		"version 0":        func(o *catalog.OrgRecipe) { o.Version = 0 },
		"unknown kind":     func(o *catalog.OrgRecipe) { o.Kind = "queue" },
		"builtin engine":   func(o *catalog.OrgRecipe) { o.Engine = resourcespec.EngineBuiltin },
		"no title":         func(o *catalog.OrgRecipe) { o.Title = "  " },
		"long description": func(o *catalog.OrgRecipe) { o.Description = strings.Repeat("a", catalog.MaxRecipeDescription+1) },
		"no author":        func(o *catalog.OrgRecipe) { o.CreatedBy = identity.UserID{} },
	} {
		o := ok
		change(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
