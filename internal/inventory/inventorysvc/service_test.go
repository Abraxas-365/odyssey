package inventorysvc_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/inventory/inventorysvc"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/google/uuid"
)

type fakes struct {
	projects   []project.Project
	envs       []project.Environment
	serving    map[project.EnvironmentID]release.Serving
	scans      map[build.ID]build.Scan
	clusters   []fleet.Cluster
	egress     map[fleet.ClusterID]fleet.EgressState
	placed     map[project.EnvironmentID]fleet.ClusterID
	uses       map[project.EnvironmentID]release.Uses
	usesCalls  int
	usesMu     sync.Mutex // Uses runs in parallel workers
	scansCalls int
	down       map[string]bool // "scans", "clusters", "approvals", "placements", "github", "github off"
	// installations are the organization's GitHub installations.
	installations []source.Installation
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
	f.usesMu.Lock()
	f.usesCalls++
	f.usesMu.Unlock()
	return f.uses[env], nil
}
func (f *fakes) Scans(_ context.Context, _ identity.OrganizationID, ids []build.ID) ([]build.BuildScan, error) {
	f.scansCalls++
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

// sourcePort is GitHub: on unless github is "off"; installations as set.
type sourcePort struct{ *fakes }

func (s sourcePort) Info(context.Context) source.Info {
	return source.Info{Enabled: !s.down["github off"]}
}
func (s sourcePort) ListInstallations(context.Context, identity.OrganizationID) ([]source.Installation, error) {
	if s.down["github"] {
		return nil, errDown
	}
	return s.installations, nil
}

func newService(f *fakes) *inventorysvc.Service {
	return inventorysvc.New(f, f, f, clusterPort{f}, approvalPort{f}, sourcePort{f}).WithClock(func() time.Time { return now })
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

// States say what each app's page says: a publication under way or waiting
// for TI first, then what is online (a failed attempt over a live version is
// still online), who can open it as the cluster enforces it, and where it
// runs; archived apps say so; unknown ids are absent, in the order asked.
func TestStatesSayWhatTheAppPageSays(t *testing.T) {
	org := identity.NewOrganizationID()
	owner := userID()
	since := now.Add(-time.Hour)
	cluster := fleet.Cluster{ID: fleet.NewClusterID(), Name: "Lima"}
	f := &fakes{serving: map[project.EnvironmentID]release.Serving{}, placed: map[project.EnvironmentID]fleet.ClusterID{},
		clusters: []fleet.Cluster{cluster}, down: map[string]bool{}}
	public := release.AccessPublic
	built := build.NewID()
	type seed struct {
		serving  *release.Serving
		archived bool
	}
	seeds := map[string]seed{
		"never":    {},
		"waiting":  {serving: &release.Serving{Last: &release.LastDeployment{Status: release.StatusWaitingApproval}}},
		"building": {serving: &release.Serving{Image: "img", URL: "https://b", Last: &release.LastDeployment{Status: release.StatusBuilding}}},
		"online": {serving: &release.Serving{Image: "img", URL: "https://o", Since: &since, Build: &built, Last: &release.LastDeployment{Status: release.StatusReady,
			Reason: release.ReasonDeploy, CreatedAt: since, RequestedBy: owner, RequestedByName: "Ana Torres", RequestedVia: "assistant:codex"},
			Access: &release.ServingAccess{Mode: release.AccessOrganization, Pending: &public}}},
		"failed over live": {serving: &release.Serving{Image: "img", URL: "https://f", Last: &release.LastDeployment{Status: release.StatusFailed}}},
		"failed first":     {serving: &release.Serving{URL: "", Last: &release.LastDeployment{Status: release.StatusFailed}}},
		"archived":         {serving: &release.Serving{Image: "img", URL: "https://a"}, archived: true},
	}
	ids := map[string]project.ID{}
	var asked []project.ID
	for name, s := range seeds {
		p := project.Project{ID: project.NewID(), Name: name, CreatedBy: owner, Status: project.StatusActive}
		if s.archived {
			p.Status = project.StatusArchived
		}
		e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
		f.projects, f.envs = append(f.projects, p), append(f.envs, e)
		if s.serving != nil {
			sv := *s.serving
			sv.Environment, sv.Project = e.ID, p.ID
			f.serving[e.ID] = sv
		}
		f.placed[e.ID] = cluster.ID
		ids[name] = p.ID
		asked = append(asked, p.ID)
	}
	unknown := project.NewID()
	asked = append([]project.ID{unknown}, asked...)
	got, err := newService(f).States(ctxOf(owner, permission.ProjectsRead), org, asked)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(seeds) || got.Items[0].App != asked[1] {
		t.Fatalf("items %+v (unknown ids absent, order kept)", got.Items)
	}
	if f.scansCalls != 0 {
		t.Fatalf("what a visitor finds read the scans %d times (Inicio polls it; it needs none)", f.scansCalls)
	}
	by := map[project.ID]inventory.AppState{}
	for _, s := range got.Items {
		by[s.App] = s
	}
	for name, want := range map[string]inventory.AppStatus{
		"never": inventory.AppNotDeployed, "waiting": inventory.AppWaitingApproval, "building": inventory.AppDeploying,
		"online": inventory.AppOnline, "failed over live": inventory.AppOnline, "failed first": inventory.AppFailed, "archived": inventory.AppArchived,
	} {
		if s := by[ids[name]]; s.Status != want {
			t.Errorf("%s: %s, want %s", name, s.Status, want)
		}
	}
	on := by[ids["online"]]
	if on.URL != "https://o" || on.Since == nil || on.Access == nil || on.Access.Mode != release.AccessOrganization ||
		on.Access.Pending == nil || *on.Access.Pending != public || on.Cluster == nil || on.Cluster.Name != "Lima" {
		t.Fatalf("online %+v", on)
	}
	if c := on.LastChange; c == nil || c.RequestedBy != owner || c.RequestedByName != "Ana Torres" || c.RequestedVia != "assistant:codex" ||
		!c.At.Equal(since) || c.Reason != release.ReasonDeploy || c.Status != release.StatusReady {
		t.Fatalf("last change %+v", on.LastChange)
	}
	if a := by[ids["archived"]]; a.URL != "" || a.Access != nil || a.Cluster != nil || a.LastChange != nil {
		t.Fatalf("an archived app claims to answer: %+v", a)
	}
	if n := by[ids["never"]]; n.URL != "" || n.Access != nil || n.LastChange != nil {
		t.Fatalf("never published: %+v", n)
	}

	// the clusters down: where it runs is unknown, and said
	f.down["clusters"] = true
	got, _ = newService(f).States(ctxOf(owner, permission.ProjectsRead), org, []project.ID{ids["online"]})
	if s := got.Items[0]; s.Cluster != nil || len(s.Unavailable) != 1 || s.Unavailable[0] != "cluster" || s.Status != inventory.AppOnline {
		t.Fatalf("clusters down %+v", s)
	}

	too := make([]project.ID, inventory.MaxStates+1)
	if _, err := newService(f).States(ctxOf(owner, permission.ProjectsRead), org, too); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("over the cap: %v", err)
	}
}

// UX v4 §5.2: a publication under way is on its owner's list (not TI's, and
// not one that waits for TI: that is the request), and every app item names
// the app's slug so the console can prepare the assistant's prompt.
func TestPublishingIsOnItsOwnersList(t *testing.T) {
	org := identity.NewOrganizationID()
	owner, other := userID(), userID()
	f := &fakes{serving: map[project.EnvironmentID]release.Serving{}, down: map[string]bool{}}
	add := func(name string, st release.Status) project.ID {
		p := project.Project{ID: project.NewID(), Name: name, Slug: name, Tier: project.TierInternal, CreatedBy: owner}
		e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
		f.projects, f.envs = append(f.projects, p), append(f.envs, e)
		f.serving[e.ID] = release.Serving{Environment: e.ID, Project: p.ID, Last: &release.LastDeployment{Status: st, CreatedAt: now.Add(-time.Minute)}}
		return p.ID
	}
	building := add("visitas", release.StatusBuilding)
	add("cobros", release.StatusWaitingApproval)
	add("listo", release.StatusReady)

	got, err := newService(f).Attention(ctxOf(owner, permission.ProjectsRead), org)
	if err != nil {
		t.Fatal(err)
	}
	var pub []inventory.Item
	for _, i := range got.Items {
		if i.Kind == inventory.KindPublishing {
			pub = append(pub, i)
		}
	}
	if len(pub) != 1 || *pub[0].App != building || pub[0].AppSlug != "visitas" || pub[0].Scope != inventory.ScopeMine ||
		pub[0].Severity != inventory.SeverityInfo || pub[0].At == nil || pub[0].Link != "/apps/"+building.String() {
		t.Fatalf("publishing %+v (all %+v)", pub, got.Items)
	}

	// someone else's app under way is not on my list, and TI is not told
	for _, ctx := range []context.Context{ctxOf(other, permission.ProjectsRead), ctxOf(other, permission.ProjectsRead, permission.InventoryRead)} {
		got, _ := newService(f).Attention(ctx, org)
		for _, i := range got.Items {
			if i.Kind == inventory.KindPublishing {
				t.Fatalf("publishing on someone else's list: %+v", i)
			}
		}
	}
}

// GitHub on the list of whoever manages the integrations: available and not
// connected, or an installation GitHub suspended. Not offered here: nothing.
func TestGitHubItemsForWhoManagesIt(t *testing.T) {
	org := identity.NewOrganizationID()
	user := userID()
	f := &fakes{down: map[string]bool{}}
	manager := ctxOf(user, permission.ProjectsRead, permission.IntegrationsManage)
	kinds := func(ctx context.Context) ([]inventory.Kind, inventory.Attention) {
		got, err := newService(f).Attention(ctx, org)
		if err != nil {
			t.Fatal(err)
		}
		var out []inventory.Kind
		for _, i := range got.Items {
			out = append(out, i.Kind)
		}
		return out, got
	}

	if k, got := kinds(manager); len(k) != 1 || k[0] != inventory.KindGitHubNotConnected || got.Items[0].Link != "/administracion/github" || got.Items[0].Scope != inventory.ScopeIT {
		t.Fatalf("not connected: %+v", got.Items)
	}
	if k, _ := kinds(ctxOf(user, permission.ProjectsRead)); len(k) != 0 {
		t.Fatalf("a builder is not told about GitHub: %v", k)
	}

	f.installations = []source.Installation{{Account: "acme"}, {Account: "vieja", Suspended: true}}
	if k, got := kinds(manager); len(k) != 1 || k[0] != inventory.KindGitHubSuspended || !strings.Contains(got.Items[0].Sentence, "vieja") {
		t.Fatalf("suspended: %+v", got.Items)
	}

	f.installations = nil
	f.down["github off"] = true
	if k, _ := kinds(manager); len(k) != 0 {
		t.Fatalf("GitHub not offered here: %v", k)
	}

	f.down["github off"], f.down["github"] = false, true
	if k, got := kinds(manager); len(k) != 0 || len(got.Unavailable) != 1 || got.Unavailable[0] != inventory.SourceGitHub {
		t.Fatalf("GitHub down is said, never \"todo en orden\": %+v", got)
	}
}

// A row lists the services of the online version, in order, and none ([],
// never null) when nothing is online.
func TestRowServices(t *testing.T) {
	org, user := identity.NewOrganizationID(), userID()
	f := &fakes{serving: map[project.EnvironmentID]release.Serving{}, down: map[string]bool{}}
	var envs [2]project.Environment
	for i := range envs {
		p := project.Project{ID: project.NewID(), Name: "App", Slug: "app", Tier: project.TierInternal, CreatedBy: user}
		envs[i] = project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Slug: "production", Kind: project.KindProduction}
		f.projects, f.envs = append(f.projects, p), append(f.envs, envs[i])
	}
	refs := []release.ServiceRef{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "jobs", Kind: project.ServiceWorker}}
	f.serving[envs[0].ID] = release.Serving{Environment: envs[0].ID, Project: envs[0].Project, Image: "img", URL: "https://a", Services: refs}
	page, err := newService(f).List(ctxOf(user, permission.ProjectsRead, permission.InventoryRead), org, inventory.Filter{}, query.Pagination{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, r := range page.Items {
		switch r.Environment.ID {
		case envs[0].ID:
			if len(r.Services) != 2 || r.Services[0] != refs[0] || r.Services[1] != refs[1] {
				t.Fatalf("online row services %+v", r.Services)
			}
		default:
			if r.Services == nil || len(r.Services) != 0 {
				t.Fatalf("offline row services %#v, want []", r.Services)
			}
		}
	}
}
