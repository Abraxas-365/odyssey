package fleetgrpc

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

func TestResourceToProtoCarriesEachSpec(t *testing.T) {
	basic := resourceToProto(fleet.DesiredResource{Name: "db", Kind: resourcekind.Postgres, Generation: 1, StorageGi: 2}).GetResource()
	if basic.GetSpec() != nil || basic.GetSpecSchemaVersion() != 0 || basic.GetStorage() != "2Gi" {
		t.Errorf("a basic resource carries no spec: %v", basic)
	}

	pg := resourceToProto(fleet.DesiredResource{Name: "db", Kind: resourcekind.Postgres, Generation: 1, StorageGi: 2,
		Spec: resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17}}}).GetResource()
	if pg.GetSpecSchemaVersion() != resourcespec.SchemaVersion || pg.GetBuiltinPostgres().GetInstances() != 2 || pg.GetBuiltinPostgres().GetMajor() != 17 {
		t.Errorf("postgres settings: %v", pg)
	}

	ex := resourceToProto(fleet.DesiredResource{Name: "reportes", Kind: resourcekind.Postgres, Generation: 3,
		Recipe: "org-rds-reportes@1", Spec: resourcespec.ExamplePostgres("vault")}).GetResource()
	if ex.GetSpecSchemaVersion() != resourcespec.SchemaVersion || ex.GetStorage() != "" {
		t.Fatalf("an existing resource has a spec and no size: %v", ex)
	}
	values := ex.GetExisting().GetValues()
	want := resourcespec.ExamplePostgres("vault").Existing.Values
	if len(values) != len(want) {
		t.Fatalf("values %v, want %v", values, want)
	}
	for i, w := range want {
		got := values[i]
		if got.GetVariable() != w.Variable || got.GetValue() != w.Value {
			t.Errorf("value %d: %v, want %+v", i, got, w)
		}
		if (w.Ref == nil) != (got.GetRef() == nil) {
			t.Errorf("value %d: ref %v, want %+v", i, got.GetRef(), w.Ref)
			continue
		}
		if w.Ref != nil {
			r := got.GetRef()
			if r.GetStore() != w.Ref.Store || r.GetKey() != w.Ref.Key || r.GetProperty() != w.Ref.Property {
				t.Errorf("value %d: ref %v, want %+v", i, r, *w.Ref)
			}
		}
	}
	if _, ok := ex.GetSpec().(*fleetv1.Resource_Existing); !ok {
		t.Errorf("arm %T", ex.GetSpec())
	}
}
