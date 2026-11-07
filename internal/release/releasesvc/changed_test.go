package releasesvc

import (
	"context"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// changedRepo answers what the deployment reads and ServiceStates ask, and
// records the batch reads of services.
type changedRepo struct {
	usesRepo
	services    map[release.ID][]release.Service
	deployments map[release.DeploymentID]release.Deployment
	order       []release.DeploymentID
	reads       [][]release.ID
}

func (r *changedRepo) ReleaseServices(_ context.Context, _ identity.OrganizationID, ids []release.ID) (map[release.ID][]release.Service, error) {
	r.reads = append(r.reads, ids)
	out := map[release.ID][]release.Service{}
	for _, id := range ids {
		if s, ok := r.services[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func (r *changedRepo) FindRelease(_ context.Context, _ identity.OrganizationID, id release.ID) (release.Release, error) {
	return release.Release{ID: id, Services: r.services[id]}, nil
}

func (r *changedRepo) FindDeployment(_ context.Context, _ identity.OrganizationID, id release.DeploymentID) (release.Deployment, error) {
	return r.deployments[id], nil
}

func (r *changedRepo) ListDeployments(_ context.Context, _ identity.OrganizationID, _ project.EnvironmentID, _ release.DeploymentFilter, page query.Pagination) (query.Paginated[release.Deployment], error) {
	items := []release.Deployment{}
	for _, id := range r.order {
		items = append(items, r.deployments[id])
	}
	return query.NewPaginated(items, len(items), page), nil
}

type changedCluster struct {
	release.Cluster
	status release.AppStatus
	asked  []string
}

func (c *changedCluster) Status(_ context.Context, _ release.Target, name string) (release.AppStatus, error) {
	c.asked = append(c.asked, name)
	return c.status, nil
}

func svc(name, image string) release.Service {
	return release.Service{Name: name, Kind: project.ServiceWeb, Route: "/" + name, Image: image, Replicas: 2}
}

type changedRig struct {
	svc     *Service
	repo    *changedRepo
	cluster *changedCluster
	org     identity.OrganizationID
	proj    project.ID
	env     project.EnvironmentID
}

func newChangedRig() *changedRig {
	u := newUsesRig()
	repo := &changedRepo{usesRepo: *u.repo, services: map[release.ID][]release.Service{}, deployments: map[release.DeploymentID]release.Deployment{}}
	cluster := &changedCluster{}
	return &changedRig{
		svc:     &Service{repo: repo, projects: u.projects, cluster: cluster},
		repo:    repo,
		cluster: cluster,
		org:     u.org,
		proj:    u.projects.p.ID,
		env:     u.projects.e.ID,
	}
}

func (r *changedRig) release(services ...release.Service) release.ID {
	id := release.NewID()
	r.repo.services[id] = services
	return id
}

func (r *changedRig) deployment(rel, prev *release.ID) release.Deployment {
	d := release.Deployment{ID: release.NewDeploymentID(), Organization: r.org, Project: r.proj, Environment: r.env, Release: rel, Previous: prev,
		Status: release.StatusReady, Reason: release.ReasonDeploy}
	r.repo.deployments[d.ID] = d
	r.repo.order = append(r.repo.order, d.ID)
	return d
}

func TestChangedOnListIsOneBatch(t *testing.T) {
	r := newChangedRig()
	v1 := r.release(svc("web", "img-1"), svc("api", "img-1"))
	v2 := r.release(svc("web", "img-1"), svc("api", "img-2"), svc("jobs", "img-1"))
	bare := r.release()                // a release without services
	first := r.deployment(&v1, nil)    // nothing before: every service is new
	second := r.deployment(&v2, &v1)   // api changed, jobs appeared
	config := r.deployment(&v2, &v2)   // the same release again
	building := r.deployment(nil, &v2) // no release yet
	empty := r.deployment(&bare, nil)  // no services and no previous

	page, err := r.svc.ListDeployments(context.Background(), r.org, r.proj, r.env, release.DeploymentFilter{}, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.repo.reads) != 1 {
		t.Fatalf("%d reads of services for one list, want 1", len(r.repo.reads))
	}
	got := map[release.DeploymentID][]string{}
	for _, d := range page.Items {
		got[d.ID] = d.Changed
	}
	for name, c := range map[string]struct {
		id   release.DeploymentID
		want []string
	}{
		"first":    {first.ID, []string{"web", "api"}},
		"second":   {second.ID, []string{"api", "jobs"}},
		"config":   {config.ID, []string{}},
		"building": {building.ID, nil},
		"empty":    {empty.ID, nil},
	} {
		if !slices.Equal(got[c.id], c.want) || (c.want == nil) != (got[c.id] == nil) {
			t.Errorf("%s: changed %#v, want %#v", name, got[c.id], c.want)
		}
	}
}

func TestChangedOnFindAndCurrent(t *testing.T) {
	r := newChangedRig()
	v1 := r.release(svc("web", "img-1"))
	v2 := r.release(svc("web", "img-2"))
	live := r.deployment(&v1, nil)
	next := r.deployment(&v2, &v1)
	ctx := context.Background()

	d, err := r.svc.FindDeployment(ctx, r.org, r.proj, r.env, next.ID)
	if err != nil || !slices.Equal(d.Changed, []string{"web"}) || len(r.repo.reads) != 1 {
		t.Fatalf("find: %#v %v after %d reads", d.Changed, err, len(r.repo.reads))
	}

	r.repo.reads = nil
	r.repo.state = release.EnvironmentState{Current: &v1, CurrentDeployment: &live.ID, Active: &next.ID}
	cur, err := r.svc.Current(ctx, r.org, r.proj, r.env)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.repo.reads) != 1 {
		t.Fatalf("%d reads of services for current, want 1", len(r.repo.reads))
	}
	if cur.Deployment == nil || !slices.Equal(cur.Deployment.Changed, []string{"web"}) || cur.Active == nil || !slices.Equal(cur.Active.Changed, []string{"web"}) {
		t.Fatalf("current: deployment %#v active %#v", cur.Deployment, cur.Active)
	}
}

func TestServiceStates(t *testing.T) {
	ctx := context.Background()
	web, api := svc("web", "i1"), svc("api", "i2")
	web.Replicas, api.Replicas = 3, 2

	t.Run("per service", func(t *testing.T) {
		r := newChangedRig()
		v := r.release(web, api)
		r.repo.state = release.EnvironmentState{Current: &v, App: "pagos"}
		r.cluster.status = release.AppStatus{ObservedRevision: 4, Phase: release.PhaseFailed, Reason: release.FailureCrash, ReadyReplicas: 3,
			Services: []release.ServiceStatus{
				{Name: "api", Phase: release.PhaseFailed, Reason: release.FailureCrash, Message: "boom", ReadyReplicas: 0},
				{Name: "web", Phase: release.PhaseReady, ReadyReplicas: 3},
			}}
		got, err := r.svc.ServiceStates(ctx, r.org, r.proj, r.env)
		if err != nil {
			t.Fatal(err)
		}
		want := []release.ServiceState{
			{Name: "web", Kind: project.ServiceWeb, Route: "/web", Phase: release.PhaseReady, ReadyReplicas: 3, Replicas: 3},
			{Name: "api", Kind: project.ServiceWeb, Route: "/api", Phase: release.PhaseFailed, Reason: release.FailureCrash, Message: "boom", Replicas: 2},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
		if !slices.Equal(r.cluster.asked, []string{"pagos"}) {
			t.Fatalf("cluster asked %v", r.cluster.asked)
		}
	})

	t.Run("aggregate fallback", func(t *testing.T) {
		r := newChangedRig()
		v := r.release(web, api)
		r.repo.state = release.EnvironmentState{Current: &v, App: "pagos"}
		r.cluster.status = release.AppStatus{ObservedRevision: 4, Phase: release.PhaseFailed, Reason: release.FailureUnhealthy, Message: "503", ReadyReplicas: 1}
		got, _ := r.svc.ServiceStates(ctx, r.org, r.proj, r.env)
		if len(got) != 2 {
			t.Fatalf("got %+v", got)
		}
		for _, s := range got {
			if s.Phase != release.PhaseFailed || s.Reason != release.FailureUnhealthy || s.Message != "503" || s.ReadyReplicas != 0 {
				t.Fatalf("several services take the aggregate, no ready replicas: %+v", s)
			}
		}
		one := r.release(web)
		r.repo.state.Current = &one
		got, _ = r.svc.ServiceStates(ctx, r.org, r.proj, r.env)
		if len(got) != 1 || got[0].ReadyReplicas != 1 || got[0].Replicas != 3 {
			t.Fatalf("one service takes the aggregate replicas: %+v", got)
		}
	})

	t.Run("unknown status", func(t *testing.T) {
		r := newChangedRig()
		v := r.release(web)
		r.repo.state = release.EnvironmentState{Current: &v, App: "pagos"}
		r.cluster.status = release.AppStatus{Phase: release.PhaseProgressing, ReadyReplicas: 1,
			Services: []release.ServiceStatus{{Name: "web", Phase: release.PhaseReady, ReadyReplicas: 1}}}
		got, _ := r.svc.ServiceStates(ctx, r.org, r.proj, r.env)
		if len(got) != 1 || got[0].Phase != "" || got[0].ReadyReplicas != 0 || got[0].Replicas != 3 {
			t.Fatalf("an unobserved app has no phase: %+v", got)
		}
	})

	t.Run("nothing serving", func(t *testing.T) {
		r := newChangedRig()
		got, err := r.svc.ServiceStates(ctx, r.org, r.proj, r.env)
		if err != nil || got == nil || len(got) != 0 || len(r.cluster.asked) != 0 || len(r.repo.reads) != 0 {
			t.Fatalf("got %#v %v; cluster asked %v, reads %v", got, err, r.cluster.asked, r.repo.reads)
		}
	})
}
