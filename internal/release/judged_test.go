package release_test

import (
	"reflect"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// MS-3c: what IT's rules see of the services a deploy or rollback brings
// against what the environment serves.
func TestCompareNamesWhatChanges(t *testing.T) {
	web := release.Service{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r/web@1"}
	api := release.Service{Name: "api", Kind: project.ServiceWeb, Route: "/api", StripPrefix: true, Root: "apps/api", Image: "r/api@1"}
	jobs := release.Service{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs", Image: "r/jobs@1"}
	serving := &release.Release{Services: []release.Service{web, api, jobs}}
	newer := func(s release.Service, img string) release.Service { s.Image = img; return s }
	moved := api
	moved.Route = "/v2"

	for name, c := range map[string]struct {
		next     []release.Service
		serving  *release.Release
		changed  []string
		topology bool
	}{
		"nothing serves yet":        {[]release.Service{web}, nil, []string{"web"}, true},
		"same version again":        {[]release.Service{web, api, jobs}, serving, []string{}, false},
		"only the api's code":       {[]release.Service{web, newer(api, "r/api@2"), jobs}, serving, []string{"api"}, false},
		"the api moves":             {[]release.Service{web, moved, jobs}, serving, []string{"api"}, true},
		"a version without the api": {[]release.Service{web, jobs}, serving, []string{"api"}, true},
		"a new service, one gone": {[]release.Service{web, api, {Name: "admin", Kind: project.ServiceWeb, Route: "/admin", Image: "r/admin@1"}},
			serving, []string{"admin", "jobs"}, true},
	} {
		got := release.Compare(c.next, c.serving)
		if !reflect.DeepEqual(got.Changed, c.changed) || got.TopologyChanged != c.topology {
			t.Errorf("%s: %+v, want changed %v topology %v", name, got, c.changed, c.topology)
		}
	}
}

// Before a build only the layout is known: a service whose code may change is
// not a change yet (it joins at the second judgment), a moved one is.
func TestCompareLayoutKnowsOnlyTheLayout(t *testing.T) {
	serving := &release.Release{Services: []release.Service{
		{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r/web@1"},
		{Name: "api", Kind: project.ServiceWeb, Route: "/api", Root: "apps/api", Image: "r/api@1"},
	}}
	same := []project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "api", Kind: project.ServiceWeb, Route: "/api", Root: "apps/api"}}
	if got := release.CompareLayout(same, serving); len(got.Changed) != 0 || got.TopologyChanged {
		t.Fatalf("the same layout %+v", got)
	}
	added := append(same[:1:1], project.Service{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"})
	if got := release.CompareLayout(added, serving); !reflect.DeepEqual(got.Changed, []string{"jobs", "api"}) || !got.TopologyChanged {
		t.Fatalf("api gone, jobs added %+v", got)
	}
	if got := release.CompareLayout(same, nil); !reflect.DeepEqual(got.Changed, []string{"web", "api"}) || !got.TopologyChanged {
		t.Fatalf("nothing serves %+v", got)
	}
}

// An app of one service reads exactly like one of several: a rule about
// services decides the same, and the request is the same shape.
func TestJudgedReadsOneServiceLikeSeveral(t *testing.T) {
	one := []release.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r/web@2"}}
	serving := &release.Release{Services: []release.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: "r/web@1"}}}
	got := release.Compare(one, serving).Judged(gate.Request{Action: gate.ActionDeploy}, release.GateServices(one))
	want := gate.Request{Action: gate.ActionDeploy, Services: []gate.Service{{Name: "web", Kind: "web", Route: "/", Public: true}},
		Changed: []string{"web"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("one service %+v, want %+v", got, want)
	}
	// a worker is never public, whatever it says
	if s := release.GateServices([]release.Service{{Name: "jobs", Kind: project.ServiceWorker}}); s[0].Public || s[0].Route != "" {
		t.Fatalf("worker %+v", s)
	}
	// the layout and the version agree on what is public
	layout := project.GateServices([]project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "jobs", Kind: project.ServiceWorker}})
	version := release.GateServices([]release.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "jobs", Kind: project.ServiceWorker}})
	if !reflect.DeepEqual(layout, version) {
		t.Fatalf("layout %+v, version %+v", layout, version)
	}
}
