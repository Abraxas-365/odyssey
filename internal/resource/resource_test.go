package resource_test

import (
	"strings"
	"testing"

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
		Name:        "db", Kind: resourcekind.Postgres, StorageGi: 5, Generation: 2, Status: resource.StatusActive, CreatedBy: user}
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
	ok := resource.Create{Name: "db", Kind: resourcekind.Postgres}.Normalize()
	if err := ok.Validate(); err != nil || ok.StorageGi != 1 {
		t.Fatalf("defaults %+v %v", ok, err)
	}
	for name, in := range map[string]resource.Create{
		"name":    {Name: "Bad Name", Kind: resourcekind.Postgres, StorageGi: 1},
		"empty":   {Name: "", Kind: resourcekind.Postgres, StorageGi: 1},
		"long":    {Name: strings.Repeat("a", 31), Kind: resourcekind.Postgres, StorageGi: 1},
		"kind":    {Name: "db", Kind: "mysql", StorageGi: 1},
		"big":     {Name: "db", Kind: resourcekind.Postgres, StorageGi: 101},
		"neg":     {Name: "db", Kind: resourcekind.Postgres, StorageGi: -1},
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
