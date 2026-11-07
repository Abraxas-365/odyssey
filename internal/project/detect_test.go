package project_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// A proposal is always something SetServices accepts.
func TestDetectProposesValidServices(t *testing.T) {
	d := project.Detect(project.DetectServices{Paths: []string{
		"apps/Web-App/package.json", "apps/api/go.mod", "apps/jobs/Dockerfile", "apps/2fa/Dockerfile", "apps/admin/package.json",
	}})
	if len(d.Items) != 5 {
		t.Fatalf("items %+v", d.Items)
	}
	in := project.SetServices{UpdatedBy: identity.MustParseUserID("00000000-0000-4000-8000-000000000001")}
	for _, it := range d.Items {
		in.Items = append(in.Items, it.ServiceSpec)
	}
	if err := in.Normalize().Validate(); err != nil {
		t.Fatalf("%v: %+v", err, in.Items)
	}
	if d.Items[0].Name != "fa" || d.Items[0].Route != "/" || d.Items[0].Evidence != "apps/2fa/Dockerfile" {
		t.Fatalf("first %+v", d.Items[0])
	}
}

func TestDetectServicesValidate(t *testing.T) {
	long := make([]byte, project.MaxDetectPath+1)
	for i := range long {
		long[i] = 'a'
	}
	for name, in := range map[string]project.DetectServices{
		"a path too long": {Paths: []string{string(long)}},
		"a NUL":           {Paths: []string{"a\x00b"}},
		"a bad root":      {Root: "../x"},
		"too many":        {Paths: make([]string, 20001)},
	} {
		if err := in.Normalize().Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (project.DetectServices{Paths: []string{"a/go.mod"}, Root: "/a/"}).Normalize().Validate(); err != nil {
		t.Fatal(err)
	}
	if d := project.Detect(project.DetectServices{}); d.Items == nil || len(d.Notes) != 1 {
		t.Fatalf("empty %+v", d)
	}
}
