package project_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

var editor = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

func set(items ...project.ServiceSpec) project.SetServices {
	return project.SetServices{Items: items, UpdatedBy: editor}.Normalize()
}

func TestValidServiceName(t *testing.T) {
	for s, want := range map[string]bool{
		"web": true, "api2": true, "a": true, strings.Repeat("a", 20): true,
		"": false, "2api": false, "my-api": false, "Api": false, "a_b": false, strings.Repeat("a", 21): false,
	} {
		if got := project.ValidServiceName(s); got != want {
			t.Errorf("ValidServiceName(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSetServicesNormalize(t *testing.T) {
	got := set(project.ServiceSpec{Name: " api ", Root: " /apps/api/ ", Route: " /api/ "}).Items[0]
	want := project.ServiceSpec{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api"}
	if got != want {
		t.Fatalf("Normalize = %+v, want %+v", got, want)
	}
	if r := set(project.ServiceSpec{Name: "web", Route: "/"}).Items[0].Route; r != "/" {
		t.Fatalf("route / normalized to %q", r)
	}
	if r := set(project.ServiceSpec{Name: "web", Route: "///"}).Items[0].Route; r != "/" {
		t.Fatalf("route /// normalized to %q", r)
	}
}

func TestSetServicesValidate(t *testing.T) {
	web := project.ServiceSpec{Name: "web", Route: "/"}
	api := project.ServiceSpec{Name: "api", Root: "apps/api", Route: "/api", StripPrefix: true}
	jobs := project.ServiceSpec{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"}
	internal := project.ServiceSpec{Name: "auth", Root: "apps/auth"}
	ok := []project.SetServices{
		set(web),
		set(web, api, jobs, internal),
		set(api), // no service at "/": the app answers only under /api
		set(project.ServiceSpec{Name: "docs", Route: "/v1.2/docs~x"}),
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", s.Items, err)
		}
	}
	nine := make([]project.ServiceSpec, 9)
	for i := range nine {
		nine[i] = project.ServiceSpec{Name: "s" + string(rune('a'+i))}
	}
	bad := map[string]project.SetServices{
		"no editor":         {Items: []project.ServiceSpec{web}},
		"empty":             set(),
		"nine":              set(nine...),
		"bad name":          set(project.ServiceSpec{Name: "my-api", Route: "/"}),
		"bad kind":          set(project.ServiceSpec{Name: "web", Kind: "cron"}),
		"duplicate name":    set(web, project.ServiceSpec{Name: "web", Route: "/x"}),
		"duplicate route":   set(web, project.ServiceSpec{Name: "site", Route: "/"}),
		"dotdot root":       set(project.ServiceSpec{Name: "web", Root: "apps/../secret", Route: "/"}),
		"dot root":          set(project.ServiceSpec{Name: "web", Root: "./apps", Route: "/"}),
		"backslash root":    set(project.ServiceSpec{Name: "web", Root: `apps\web`, Route: "/"}),
		"long root":         set(project.ServiceSpec{Name: "web", Root: strings.Repeat("a", 201), Route: "/"}),
		"relative route":    set(project.ServiceSpec{Name: "web", Route: "api"}),
		"query route":       set(project.ServiceSpec{Name: "web", Route: "/api?x=1"}),
		"dotdot route":      set(project.ServiceSpec{Name: "web", Route: "/a/../b"}),
		"reserved route":    set(project.ServiceSpec{Name: "web", Route: "/_odicea"}),
		"reserved subroute": set(project.ServiceSpec{Name: "web", Route: "/_odicea/callback"}),
		"long route":        set(project.ServiceSpec{Name: "web", Route: "/" + strings.Repeat("a", 100)}),
		"strip root route":  set(project.ServiceSpec{Name: "web", Route: "/", StripPrefix: true}),
		"strip internal":    set(project.ServiceSpec{Name: "web", StripPrefix: true}),
		"worker route":      set(project.ServiceSpec{Name: "jobs", Kind: project.ServiceWorker, Route: "/jobs"}),
		"worker strip":      set(project.ServiceSpec{Name: "jobs", Kind: project.ServiceWorker, StripPrefix: true}),
	}
	for name, s := range bad {
		if err := s.Validate(); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("%s: Validate = %v, want a validation error", name, err)
		}
	}
	if err := set(project.ServiceSpec{Name: "_odiceax", Route: "/_odiceax"}).Validate(); err == nil {
		t.Error("an invalid name passed")
	}
	if err := set(project.ServiceSpec{Name: "web", Route: "/_odiceax"}).Validate(); err != nil {
		t.Errorf("/_odiceax is not the platform's route: %v", err)
	}
}

func TestSimple(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	id := project.MustParseID("00000000-0000-4000-8000-0000000000aa")
	if !project.Simple(project.DefaultServices(org, id)) {
		t.Fatal("the default services are not Simple")
	}
	for name, s := range map[string][]project.Service{
		"two":    {{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "api", Kind: project.ServiceWeb, Route: "/api"}},
		"folder": {{Name: "web", Kind: project.ServiceWeb, Route: "/", Root: "apps/web"}},
		"route":  {{Name: "web", Kind: project.ServiceWeb, Route: "/app"}},
		"worker": {{Name: "jobs", Kind: project.ServiceWorker}},
		"none":   nil,
	} {
		if project.Simple(s) {
			t.Errorf("%s is Simple", name)
		}
	}
}

func TestSameServices(t *testing.T) {
	a := []project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}}
	b := []project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/", Position: 3}}
	if !project.SameServices(a, b) {
		t.Fatal("same services differ")
	}
	b[0].Root = "web"
	if project.SameServices(a, b) {
		t.Fatal("different roots are the same")
	}
}
