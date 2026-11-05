package inventorysvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/inventory/inventorysvc"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/google/uuid"
)

type fakes struct {
	projects  []project.Project
	envs      []project.Environment
	serving   map[project.EnvironmentID]release.Serving
	scans     map[build.ID]build.Scan
	clusters  []fleet.Cluster
	egress    map[fleet.ClusterID]fleet.EgressState
	placed    map[project.EnvironmentID]fleet.ClusterID
	uses      map[project.EnvironmentID]release.Uses
	usesCalls int
	down      map[string]bool // "scans", "clusters", "approvals", "placements"
}

var errDown = errors.New("down")

func (f *fakes) List(_ context.Context, _ identity.OrganizationID, filter project.Filter, p query.Pagination) (query.Paginated[project.Project], error) {
	var out []project.Project
	for _, x := range f.projects {
		if !filter.CreatedBy.IsZero() && x.CreatedBy != filter.CreatedBy {
			continue
		}
		out = append(out, x)
	}
	end := min(p.Offset+p.Limit, len(out))
	if p.Offset > len(out) {
		return query.NewPaginated[project.Project](nil, len(out), p), nil
	}
	return query.NewPaginated(out[p.Offset:end], len(out), p), nil
}
func (f *fakes) EnvironmentsOf(context.Context, identity.OrganizationID, []project.ID) ([]project.Environment, error) {
	return f.envs, nil
}
func (f *fakes) FindMany(_ context.Context, _ identity.OrganizationID, ids []project.ID) ([]project.Project, error) {
	var out []project.Project
	for _, x := range f.projects {
		for _, id := range ids {
			if x.ID == id {
				out = append(out, x)
			}
		}
	}
	return out, nil
}
func (f *fakes) Serving(_ context.Context, _ identity.OrganizationID, envs []project.EnvironmentID) ([]release.Serving, error) {
	var out []release.Serving
	for _, e := range envs {
		if s, ok := f.serving[e]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakes) Uses(_ context.Context, _ identity.OrganizationID, _ project.ID, env project.EnvironmentID) (release.Uses, error) {
	f.usesCalls++
	return f.uses[env], nil
}
func (f *fakes) Scans(_ context.Context, _ identity.OrganizationID, ids []build.ID) ([]build.BuildScan, error) {
	if f.down["scans"] {
		return nil, errDown
	}
	var out []build.BuildScan
	for _, id := range ids {
		if s, ok := f.scans[id]; ok {
			out = append(out, build.BuildScan{Build: id, Scan: s})
		}
	}
	return out, nil
}

type clusterPort struct{ *fakes }

func (c clusterPort) List(context.Context, identity.OrganizationID, fleet.Filter, query.Pagination) (query.Paginated[fleet.Cluster], error) {
	if c.down["clusters"] {
		return query.Paginated[fleet.Cluster]{}, errDown
	}
	return query.NewPaginated(c.clusters, len(c.clusters), query.Pagination{Limit: 100}), nil
}
func (c clusterPort) Placements(context.Context, identity.OrganizationID, []project.EnvironmentID) (map[project.EnvironmentID]fleet.ClusterID, error) {
	if c.down["placements"] {
		return nil, errDown
	}
	return c.placed, nil
}
func (c clusterPort) Egress(_ context.Context, _ identity.OrganizationID, id fleet.ClusterID) (fleet.EgressStatus, error) {
	st := fleet.EgressStatus{State: c.egress[id]}
	st.Mode = fleet.EgressRestricted
	return st, nil
}

type approvalPort struct{ *fakes }

func (a approvalPort) List(context.Context, identity.OrganizationID, approval.Viewer, approval.Filter, query.Pagination) (query.Paginated[approval.View], error) {
	if a.down["approvals"] {
		return query.Paginated[approval.View]{}, errDown
	}
	return query.NewPaginated[approval.View](nil, 0, query.Pagination{Limit: 100}), nil
}

func newService(f *fakes) *inventorysvc.Service {
	return inventorysvc.New(f, f, f, clusterPort{f}, approvalPort{f}).WithClock(func() time.Time { return now })
}

var now = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func ctxOf(user identity.UserID, perms ...string) context.Context {
	return actor.With(context.Background(), actor.Actor{User: user, Permissions: perms})
}

// userID is a fresh IAMKit user id (IAMKit generates them).
func userID() identity.UserID { return identity.MustParseUserID(uuid.NewString()) }

func TestClusterItemsAndDegradation(t *testing.T) {
	user := userID()
	org := identity.NewOrganizationID()
	disconnected := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Lima", Status: fleet.StatusActive, Connected: false}
	notApplied := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Cusco", Status: fleet.StatusActive, Connected: true}
	applied := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Piura", Status: fleet.StatusActive, Connected: true}
	revoked := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Vieja", Status: fleet.StatusRevoked}
	f := &fakes{
		clusters: []fleet.Cluster{disconnected, notApplied, applied, revoked},
		egress:   map[fleet.ClusterID]fleet.EgressState{notApplied.ID: fleet.EgressFailed, applied.ID: fleet.EgressApplied},
		down:     map[string]bool{},
	}
	svc := newService(f)
	it := ctxOf(user, permission.ProjectsRead, permission.InventoryRead)

	got, err := svc.Attention(it, org)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[inventory.Kind]int{}
	for _, i := range got.Items {
		kinds[i.Kind]++
		if i.Scope != inventory.ScopeIT || i.Cluster == nil || i.Sentence == "" || i.Link == "" {
			t.Fatalf("item %+v", i)
		}
	}
	if kinds[inventory.KindClusterDisconnected] != 1 || kinds[inventory.KindClusterPolicy] != 1 || len(got.Items) != 2 {
		t.Fatalf("items %+v", got.Items)
	}
	if got.Items[0].Severity != inventory.SeverityCritical {
		t.Fatalf("a cluster that does not answer comes first: %+v", got.Items)
	}

	// a builder never sees the clusters
	got, _ = svc.Attention(ctxOf(user, permission.ProjectsRead), org)
	if len(got.Items) != 0 {
		t.Fatalf("builder %+v", got.Items)
	}

	// a source that fails is named, the others still answer
	f.down["clusters"], f.down["approvals"] = true, true
	got, err = svc.Attention(ctxOf(user, permission.ProjectsRead, permission.InventoryRead, permission.ApprovalsDecide), org)
	if err != nil {
		t.Fatal(err)
	}
	want := map[inventory.Source]bool{inventory.SourceClusters: true, inventory.SourceApprovals: true, inventory.SourceRequests: true}
	if len(got.Unavailable) != len(want) {
		t.Fatalf("unavailable %v", got.Unavailable)
	}
	for _, s := range got.Unavailable {
		if !want[s] {
			t.Fatalf("unavailable %v", got.Unavailable)
		}
	}
}

func TestListScansAreBatchedAndDegrade(t *testing.T) {
	org := identity.NewOrganizationID()
	user := userID()
	f := &fakes{serving: map[project.EnvironmentID]release.Serving{}, scans: map[build.ID]build.Scan{}, uses: map[project.EnvironmentID]release.Uses{},
		placed: map[project.EnvironmentID]fleet.ClusterID{}, down: map[string]bool{}}
	for i := 0; i < 30; i++ {
		p := project.Project{ID: project.NewID(), Name: "App", Slug: "app", Tier: project.TierInternal, CreatedBy: user}
		e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
		f.projects, f.envs = append(f.projects, p), append(f.envs, e)
	}
	svc := newService(f)
	it := ctxOf(user, permission.ProjectsRead, permission.InventoryRead)

	// a page reads the uses of the page only, never of the 30
	page, err := svc.List(it, org, inventory.Filter{}, query.Pagination{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page.Total != 30 || len(page.Items) != 5 || f.usesCalls != 5 {
		t.Fatalf("total %d, items %d, uses read %d", page.Page.Total, len(page.Items), f.usesCalls)
	}
	// the attention list of the owner reads the uses of their own apps only
	f.usesCalls = 0
	if _, err := svc.Attention(it, org); err != nil || f.usesCalls != 30 {
		t.Fatalf("uses read %d (%v)", f.usesCalls, err)
	}
	// list_apps' flag reads none
	f.usesCalls = 0
	ids := make([]project.ID, len(f.projects))
	for i, p := range f.projects {
		ids[i] = p.ID
	}
	if _, err := svc.NeedingAttention(it, org, ids); err != nil || f.usesCalls != 0 {
		t.Fatalf("NeedingAttention read %d uses (%v)", f.usesCalls, err)
	}

	// scans that cannot be read: rows say so instead of failing
	f.down["scans"] = true
	e := f.envs[0]
	b := build.NewID()
	f.serving[e.ID] = release.Serving{Environment: e.ID, Project: e.Project, Image: "r/x:1", Build: &b}
	page, err = svc.List(it, org, inventory.Filter{}, query.Pagination{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, r := range page.Items {
		if r.Environment.ID == e.ID {
			seen = true
			if r.Scan.State != inventory.ScanPending || len(r.Unavailable) == 0 || r.Unavailable[0] != "scan" {
				t.Fatalf("row %+v", r)
			}
		}
	}
	if !seen {
		t.Fatal("row missing")
	}
}

// ?cluster= with the clusters unreadable must say so: an empty list would
// read as "nothing runs there". The CSV (Export) says it too.
func TestClusterFilterSaysWhenClustersAreUnavailable(t *testing.T) {
	org, user := identity.NewOrganizationID(), userID()
	p := project.Project{ID: project.NewID(), Name: "App", Slug: "app", Tier: project.TierInternal, CreatedBy: user}
	e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
	c := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Lima", Status: fleet.StatusActive, Connected: true}
	f := &fakes{projects: []project.Project{p}, envs: []project.Environment{e}, clusters: []fleet.Cluster{c},
		placed: map[project.EnvironmentID]fleet.ClusterID{e.ID: c.ID}, down: map[string]bool{}}
	svc := newService(f)
	it := ctxOf(user, permission.ProjectsRead, permission.InventoryRead)
	filter := inventory.Filter{Cluster: c.ID}

	// healthy: the app is on the cluster, nothing unavailable
	page, err := svc.List(it, org, filter, query.Pagination{})
	if err != nil || len(page.Items) != 1 || len(page.Unavailable) != 0 || page.Unavailable == nil {
		t.Fatalf("healthy: %+v %v", page, err)
	}

	for _, source := range []string{"clusters", "placements"} {
		f.down = map[string]bool{source: true}
		page, err = svc.List(it, org, filter, query.Pagination{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 || len(page.Unavailable) != 1 || page.Unavailable[0] != inventory.SourceClusters {
			t.Fatalf("%s down: want no items and clusters unavailable, got %+v", source, page)
		}
		got, err := svc.Export(it, org, filter)
		if err != nil || len(got.Unavailable) != 1 || got.Unavailable[0] != inventory.SourceClusters {
			t.Fatalf("%s down, export: %+v %v", source, got, err)
		}
		// without the filter nothing depends on the clusters: not flagged
		page, err = svc.List(it, org, inventory.Filter{}, query.Pagination{})
		if err != nil || len(page.Unavailable) != 0 || len(page.Items) != 1 {
			t.Fatalf("%s down, no filter: %+v %v", source, page, err)
		}
	}
}

// list_apps's flag follows /attention's scope: a builder is told about their
// own apps only, TI about all; the others are absent (not a false).
func TestNeedingAttentionIsScopedLikeAttention(t *testing.T) {
	org := identity.NewOrganizationID()
	mine, theirs := userID(), userID()
	f := &fakes{serving: map[project.EnvironmentID]release.Serving{}, down: map[string]bool{}}
	var ids []project.ID
	for _, owner := range []identity.UserID{mine, theirs} {
		p := project.Project{ID: project.NewID(), Name: "App", Slug: "app", Tier: project.TierInternal, CreatedBy: owner}
		e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
		f.projects, f.envs = append(f.projects, p), append(f.envs, e)
		f.serving[e.ID] = release.Serving{Environment: e.ID, Project: p.ID,
			Last: &release.LastDeployment{Status: release.StatusFailed}}
		ids = append(ids, p.ID)
	}
	svc := newService(f)

	got, err := svc.NeedingAttention(ctxOf(mine, permission.ProjectsRead), org, ids)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got[ids[0]]; !ok || !v {
		t.Fatalf("a builder is told about their own failed app: %v", got)
	}
	if _, ok := got[ids[1]]; ok {
		t.Fatalf("a builder must not learn about a colleague's app: %v", got)
	}
	got, _ = svc.NeedingAttention(ctxOf(userID(), permission.ProjectsRead), org, ids)
	if len(got) != 0 {
		t.Fatalf("somebody with no apps: %v", got)
	}
	got, _ = svc.NeedingAttention(ctxOf(userID(), permission.ProjectsRead, permission.InventoryRead), org, ids)
	if !got[ids[0]] || !got[ids[1]] {
		t.Fatalf("TI sees every app: %v", got)
	}
}

func TestAppsAboveTheCapAreTruncated(t *testing.T) {
	user := userID()
	f := &fakes{down: map[string]bool{}}
	for i := 0; i < inventory.MaxApps+30; i++ {
		f.projects = append(f.projects, project.Project{ID: project.NewID(), Name: "A", CreatedBy: user})
	}
	page, err := newService(f).List(ctxOf(user, permission.InventoryRead), identity.NewOrganizationID(), inventory.Filter{}, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated {
		t.Fatal("not marked truncated")
	}
}
