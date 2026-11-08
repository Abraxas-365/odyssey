package resource_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	r := resource.Resource{ID: resource.MustParseID("00000000-0000-4000-8000-0000000000f1"), Organization: org,
		Project:     project.MustParseID("00000000-0000-4000-8000-0000000000aa"),
		Environment: project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb"),
		Name:        "db", Kind: resourcekind.Postgres, StorageGi: 5, Recipe: "postgres-basico", RecipeVersion: 1,
		Params: map[string]int{"storage_gi": 5}, Generation: 2, Status: resource.StatusActive, CreatedBy: user}
	n := resource.Names{Project: "Visitas", Environment: "production"}
	for _, e := range []event.Event{
		resource.NewCreated(org, user, r, n),
		resource.NewResized(org, user, r, 1, n),
		resource.NewDeleted(org, user, r, n),
		resource.NewRemoved(org, r),
	} {
		eventtest.Golden(t, e)
	}
}

func TestCreateValidation(t *testing.T) {
	// the catalog fills the defaults and checks the values: the request only
	// carries what was asked for
	ok := resource.Create{Name: "db", Kind: resourcekind.Postgres}.Normalize()
	if err := ok.Validate(); err != nil || len(ok.Params) != 0 {
		t.Fatalf("defaults %+v %v", ok, err)
	}
	if in := (resource.Create{Name: "db", Kind: resourcekind.Postgres, StorageGi: 5}).Normalize(); in.Params["storage_gi"] != 5 {
		t.Fatalf("storage_gi folds into params: %+v", in)
	}
	many := map[string]int{}
	for i := range resource.MaxParams + 1 {
		many["p"+strconv.Itoa(i)] = 1
	}
	for name, in := range map[string]resource.Create{
		"name":    {Name: "Bad Name", Kind: resourcekind.Postgres, StorageGi: 1},
		"empty":   {Name: "", Kind: resourcekind.Postgres, StorageGi: 1},
		"long":    {Name: strings.Repeat("a", 31), Kind: resourcekind.Postgres, StorageGi: 1},
		"kind":    {Name: "db", Kind: "mysql", StorageGi: 1},
		"twice":   {Name: "db", Kind: resourcekind.Postgres, StorageGi: 2, Params: map[string]int{"storage_gi": 3}},
		"many":    {Name: "db", Kind: resourcekind.Postgres, Params: many},
		"minus":   {Name: "db", Kind: resourcekind.Postgres, StorageGi: -5},
		"prefix":  {Name: "db", Kind: resourcekind.Postgres, StorageGi: 1, Connect: &resource.ConnectOptions{Prefix: "lower"}},
		"prefixU": {Name: "db", Kind: resourcekind.Postgres, StorageGi: 1, Connect: &resource.ConnectOptions{Prefix: "ODYSSEY_"}},
	} {
		if err := in.Normalize().Validate(); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (resource.Create{Name: "db", Kind: resourcekind.Redis, StorageGi: 100, Connect: &resource.ConnectOptions{Prefix: "CACHE"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestChangeValidation(t *testing.T) {
	if err := (resource.Change{}).Normalize().Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("nothing to change: %v", err)
	}
	in := resource.Change{StorageGi: 7}.Normalize()
	if err := in.Validate(); err != nil || in.Params["storage_gi"] != 7 {
		t.Fatalf("storage_gi %+v %v", in, err)
	}
	if err := (resource.Change{StorageGi: 7, Params: map[string]int{"storage_gi": 8}}).Normalize().Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("two sizes: %v", err)
	}
	if err := (resource.Change{StorageGi: -1}).Normalize().Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("a negative size: %v", err)
	}
}

// TestApprovalPayloads: a held create or change re-runs from its payload as
// IT saw it (recipe@version and parameters pinned); a payload from before
// the catalog (v1) is refused with APPROVAL_OUTDATED, never reinterpreted.
func TestApprovalPayloads(t *testing.T) {
	step := resource.CreateStep{Name: "db", Kind: "postgres", Recipe: "postgres-basico", RecipeVersion: 1,
		Params: map[string]int{"storage_gi": 20}, Connect: &resource.CreateConnect{Prefix: "DB"}}
	raw, err := json.Marshal(resource.CreateApproval{Op: resource.OpCreate, SchemaVersion: resource.PayloadVersion, Steps: []resource.CreateStep{step}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resource.DecodeCreate(raw)
	if err != nil || len(got.Steps) != 1 {
		t.Fatalf("v2 create %+v %v", got, err)
	}
	in := got.Steps[0].Create()
	if in.Recipe != "postgres-basico" || in.RecipeVersion != 1 || in.Params["storage_gi"] != 20 || in.Connect == nil || in.Connect.Prefix != "DB" {
		t.Fatalf("re-run create %+v", in)
	}
	raw, err = json.Marshal(resource.ChangeApproval{Op: resource.OpChange, SchemaVersion: resource.PayloadVersion, Name: "db",
		Recipe: "postgres-basico", RecipeVersion: 1, Params: map[string]int{"storage_gi": 30}})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := resource.DecodeChange(raw); err != nil || c.Name != "db" || c.RecipeVersion != 1 || c.Params["storage_gi"] != 30 {
		t.Fatalf("v2 change %+v %v", c, err)
	}
	for name, v1 := range map[string]string{
		"create": `{"op":"resource.create","name":"db","kind":"postgres","storage_gi":5}`,
		"change": `{"op":"resource.change","name":"db","storage_gi":5}`,
	} {
		var err error
		if name == "create" {
			_, err = resource.DecodeCreate(json.RawMessage(v1))
		} else {
			_, err = resource.DecodeChange(json.RawMessage(v1))
		}
		var x *errx.Error
		if !errx.As(err, &x) || x.Code != resource.CodeOldApproval {
			t.Errorf("v1 %s: %v", name, err)
		}
	}
}

func TestView(t *testing.T) {
	r := resource.Resource{Name: "db", Kind: resourcekind.Postgres}
	v := resource.NewView(r, resource.Observed{}, nil)
	if v.Phase != resource.PhaseProvisioning || v.Connected || len(v.Variables) == 0 {
		t.Fatalf("unconnected %+v", v)
	}
	v = resource.NewView(r, resource.Observed{Phase: resource.PhaseReady}, &resource.Connection{Name: "db", Prefix: "A"})
	if !v.Connected || v.Prefix != "A" || !strings.HasPrefix(v.Variables[0], "A_") {
		t.Fatalf("connected %+v", v)
	}
}

func TestDesiredOfCarriesThePinnedSpec(t *testing.T) {
	version := func(r resource.Resource) (catalog.RecipeVersion, bool) {
		return catalog.Factory().Version(r.Recipe, r.RecipeVersion)
	}
	desired := func(r resource.Resource) (resource.Desired, error) {
		v, ok := version(r)
		return resource.DesiredOf(r, v, ok)
	}
	r := resource.Resource{Name: "db", Kind: resourcekind.Postgres, Recipe: "postgres-produccion", RecipeVersion: 1, StorageGi: 10, Generation: 1}
	d, err := desired(r)
	if err != nil || d.Recipe != "postgres-produccion@1" || d.Spec.Postgres == nil || d.Spec.Postgres.Instances != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	r.Recipe, r.RecipeVersion = "postgres-basico", 1
	if d, err = desired(r); err != nil || !d.Spec.IsZero() || d.Recipe != "postgres-basico@1" {
		t.Fatalf("basic: %+v %v", d, err)
	}
	// A version handed over for another pin is not the resource's.
	prod, _ := catalog.Factory().Version("postgres-produccion", 1)
	if _, err := resource.DesiredOf(r, prod, true); !errx.IsType(err, errx.TypeInternal) {
		t.Fatalf("mismatched version: %v", err)
	}

	// A version the organization does not know is never sent as a basic
	// resource...
	r.Recipe, r.RecipeVersion = "postgres-produccion", 99
	if _, err := desired(r); !errx.IsType(err, errx.TypeInternal) {
		t.Fatalf("unknown version: %v", err)
	}
	// ...but it can still be deleted: the agent only removes it.
	r.Status = resource.StatusDeleting
	if d, err = desired(r); err != nil || !d.Deleted || !d.Spec.IsZero() {
		t.Fatalf("deleting: %+v %v", d, err)
	}
}
