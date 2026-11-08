package resourcespec_test

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

func TestRequires(t *testing.T) {
	if got := (resourcespec.Spec{}).Requires(resourcekind.Redis); !slices.Equal(got, []string{"builtin:redis"}) {
		t.Fatalf("basic: %v", got)
	}
	// an empty postgres block is the basic database too
	if got := (resourcespec.Spec{Postgres: &resourcespec.Postgres{}}).Requires(resourcekind.Postgres); !slices.Equal(got, []string{"builtin:postgres"}) {
		t.Fatalf("empty block: %v", got)
	}
	s := resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Memory: "1Gi"}}
	want := []string{"builtin:postgres", resourcespec.PostgresInstances, resourcespec.PostgresResources, resourcespec.SpecV1}
	slices.Sort(want)
	if got := s.Requires(resourcekind.Postgres); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if m := resourcespec.Missing(want, []string{"builtin:postgres", resourcespec.SpecV1}); len(m) != 2 {
		t.Fatalf("missing %v", m)
	}
}

func TestValidate(t *testing.T) {
	ok := []resourcespec.Spec{
		{},
		{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17, CPU: "500m", Memory: "1.5Gi"}},
		{Postgres: &resourcespec.Postgres{CPU: "1"}},
	}
	for _, s := range ok {
		if err := s.Validate(resourcekind.Postgres); err != nil {
			t.Errorf("%+v: %v", s.Postgres, err)
		}
	}
	bad := []resourcespec.Postgres{
		{Instances: -1}, {Instances: resourcespec.MaxInstances + 1}, {Major: 12},
		{CPU: "lots"}, {Memory: "1GB"}, {CPU: "m"}, {Memory: "12345678901234567Gi"},
		{CPU: "."}, {CPU: ".m"}, {Memory: "1.5.2Gi"},
	}
	for _, p := range bad {
		if err := (resourcespec.Spec{Postgres: &p}).Validate(resourcekind.Postgres); err == nil {
			t.Errorf("%+v passed", p)
		}
	}
	if err := (resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2}}).Validate(resourcekind.Redis); err == nil {
		t.Error("redis took postgres settings")
	}
}
