package releasesvc

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// The fakes embed the port (nil) and answer only what Uses asks; anything
// else panics, which is how the tests also prove what Uses does NOT ask.

type usesProjects struct {
	project.Queries
	p     project.Project
	e     project.Environment
	finds int
}

func (f *usesProjects) Find(context.Context, identity.OrganizationID, project.ID) (project.Project, error) {
	f.finds++
	return f.p, nil
}

func (f *usesProjects) FindEnvironment(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (project.Environment, error) {
	return f.e, nil
}

type usesRepo struct {
	release.Repository
	settings      release.Settings
	state         release.EnvironmentState
	settingsReads int
}

func (r *usesRepo) FindSettings(context.Context, identity.OrganizationID, project.EnvironmentID) (release.Settings, error) {
	r.settingsReads++
	return r.settings, nil
}

func (r *usesRepo) FindState(context.Context, identity.OrganizationID, project.EnvironmentID) (release.EnvironmentState, error) {
	return r.state, nil
}

func (r *usesRepo) ListSecretRequests(context.Context, identity.OrganizationID, project.EnvironmentID) ([]release.SecretRequest, error) {
	return nil, nil
}

func (r *usesRepo) ListSecretVersions(context.Context, identity.OrganizationID, project.EnvironmentID) (map[string]int64, error) {
	return map[string]int64{}, nil
}

type usesCluster struct {
	release.Cluster
	secretsErr, egressErr error
}

func (c *usesCluster) Secrets(context.Context, release.Target) (release.ClusterSecrets, error) {
	if c.secretsErr != nil {
		return release.ClusterSecrets{}, c.secretsErr
	}
	return release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}, Stores: []string{}}, nil
}

func (c *usesCluster) Egress(context.Context, release.Target) (release.ClusterEgress, error) {
	if c.egressErr != nil {
		return release.ClusterEgress{}, c.egressErr
	}
	return release.ClusterEgress{Available: true, Mode: "internet", State: release.EgressApplied, Enforced: true}, nil
}

type usesResources struct {
	release.Resources
	err   error
	asked [][]string
}

func (r *usesResources) Statuses(_ context.Context, _ identity.OrganizationID, _ project.EnvironmentID, names []string) ([]release.ResourceState, error) {
	r.asked = append(r.asked, names)
	if r.err != nil {
		return nil, r.err
	}
	out := []release.ResourceState{}
	for _, n := range names {
		out = append(out, release.ResourceState{Name: n, Kind: resourcekind.Postgres, Status: release.ResourceReady})
	}
	return out, nil
}

type usesAI struct {
	err error
}

func (a usesAI) AIState(context.Context, identity.OrganizationID, project.ID, project.EnvironmentID) (release.AIState, error) {
	if a.err != nil {
		return release.AIState{}, a.err
	}
	return release.AIState{Available: true, Status: "off"}, nil
}

type usesRig struct {
	svc       *Service
	projects  *usesProjects
	repo      *usesRepo
	cluster   *usesCluster
	resources *usesResources
	ai        *usesAI
	org       identity.OrganizationID
}

func newUsesRig() *usesRig {
	org := identity.NewOrganizationID()
	e := project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}
	p := project.Project{ID: project.NewID(), Organization: org, Slug: "pagos"}
	settings := release.DefaultSettings(org, e.ID)
	settings.Resources = []release.ResourceUse{{Name: "main", Kind: resourcekind.Postgres}, {Name: "cache", Kind: resourcekind.Redis}}
	r := &usesRig{
		projects:  &usesProjects{p: p, e: e},
		repo:      &usesRepo{settings: settings},
		cluster:   &usesCluster{},
		resources: &usesResources{},
		ai:        &usesAI{},
		org:       org,
	}
	r.svc = &Service{repo: r.repo, projects: r.projects, cluster: r.cluster, resources: r.resources, aiStatus: r.ai}
	return r
}

func (r *usesRig) uses(t *testing.T) release.Uses {
	t.Helper()
	u, err := r.svc.Uses(context.Background(), r.org, r.projects.p.ID, r.projects.e.ID)
	if err != nil {
		t.Fatalf("Uses failed: %v", err)
	}
	return u
}

// One source down never fails the call: its section is left out and named,
// the others are still answered. One case per source.
func TestUsesDegradesPerSource(t *testing.T) {
	down := errors.New("unreachable")
	cases := map[string]struct {
		section release.UsesSection
		break_  func(*usesRig)
		left    func(release.Uses) bool // the section is out
		others  func(release.Uses) bool // the rest is still there
	}{
		"secrets": {release.SectionSecrets, func(r *usesRig) { r.cluster.secretsErr = down },
			func(u release.Uses) bool { return u.Secrets == nil },
			func(u release.Uses) bool {
				return u.AI != nil && u.Egress != nil && u.Resources[0].Status == release.ResourceReady
			}},
		"resources": {release.SectionResources, func(r *usesRig) { r.resources.err = down },
			func(u release.Uses) bool {
				return len(u.Resources) == 2 && u.Resources[0].Status == release.ResourceUnknown
			},
			func(u release.Uses) bool { return u.AI != nil && u.Egress != nil && u.Secrets != nil }},
		"ai": {release.SectionAI, func(r *usesRig) { r.ai.err = down },
			func(u release.Uses) bool { return u.AI == nil },
			func(u release.Uses) bool {
				return u.Secrets != nil && u.Egress != nil && u.Resources[0].Status == release.ResourceReady
			}},
		"egress": {release.SectionEgress, func(r *usesRig) { r.cluster.egressErr = down },
			func(u release.Uses) bool { return u.Egress == nil },
			func(u release.Uses) bool {
				return u.Secrets != nil && u.AI != nil && u.Resources[0].Status == release.ResourceReady
			}},
		// connected resources and no resource domain wired: unknown, never
		// "the resource does not exist"
		"resources not wired": {release.SectionResources, func(r *usesRig) { r.svc.resources = nil },
			func(u release.Uses) bool {
				return len(u.Resources) == 2 && u.Resources[0].Status == release.ResourceUnknown && len(u.Attention) == 0
			},
			func(u release.Uses) bool { return u.AI != nil && u.Egress != nil && u.Secrets != nil }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newUsesRig()
			c.break_(r)
			u := r.uses(t)
			if !slices.Equal(u.Unavailable, []release.UsesSection{c.section}) || !c.left(u) || !c.others(u) {
				t.Fatalf("%s down: %+v", name, u)
			}
			if len(u.Attention) != 0 {
				t.Fatalf("an unknown source raised attention: %+v", u.Attention)
			}
		})
	}
	// everything down at once is still an answer
	r := newUsesRig()
	r.cluster.secretsErr, r.cluster.egressErr, r.resources.err, r.ai.err = down, down, down, down
	if u := r.uses(t); len(u.Unavailable) != 4 || u.Secrets != nil || u.AI != nil || u.Egress != nil {
		t.Fatalf("all down: %+v", u)
	}
}

// Uses asks the resource domain only for the connected resources (by name),
// resolves the project once, and reads the settings once.
func TestUsesCostsLittle(t *testing.T) {
	r := newUsesRig()
	r.uses(t)
	if len(r.resources.asked) != 1 || !slices.Equal(r.resources.asked[0], []string{"cache", "main"}) { // the settings keep them by name
		t.Fatalf("resource asks %v", r.resources.asked)
	}
	if r.projects.finds != 1 || r.repo.settingsReads != 1 {
		t.Fatalf("project found %d times, settings read %d", r.projects.finds, r.repo.settingsReads)
	}
	// with nothing connected the resource domain is not asked at all
	r = newUsesRig()
	r.repo.settings.Resources = nil
	r.uses(t)
	if len(r.resources.asked) != 0 {
		t.Fatalf("asked with nothing connected: %v", r.resources.asked)
	}
}

// Without an active deployment, settings newer than the applied ones failed
// to apply (nothing retries): config_failed; with one, config_pending.
func TestUsesConfigPendingAndFailed(t *testing.T) {
	r := newUsesRig()
	cur, applied := release.NewID(), int64(1)
	r.repo.state = release.EnvironmentState{Current: &cur, AppliedSettings: &applied}
	r.repo.settings.Revision = 2
	if u := r.uses(t); len(u.Attention) != 1 || u.Attention[0].Kind != release.AttentionConfigFailed || u.Attention[0].Who != release.WhoOwner || !u.Access.Failed {
		t.Fatalf("no deployment on its way: %+v", u.Attention)
	}
	active := release.NewDeploymentID()
	r.repo.state.Active = &active
	if u := r.uses(t); len(u.Attention) != 1 || u.Attention[0].Kind != release.AttentionConfigPending || u.Attention[0].Who != release.WhoWait || u.NeedsPeople() {
		t.Fatalf("deployment active: %+v", u.Attention)
	}
}
