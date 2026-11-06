package releasesvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// heldProjects answers Hold with the project as the test sets it.
type heldProjects struct {
	usesProjects
	holds int
}

func (h *heldProjects) Hold(context.Context, identity.OrganizationID, project.ID) (project.Project, error) {
	h.holds++
	return h.p, nil
}

// applies counts what reached the cluster.
type applies struct {
	release.Cluster
	applied int
}

func (a *applies) Apply(context.Context, release.Target, release.DesiredApp) error {
	a.applied++
	return nil
}

// applyRepo is approvalRepo plus the write of the settings the cluster got.
type applyRepo struct {
	approvalRepo
	appliedSettings int
}

func (r *applyRepo) SetAppliedSettings(context.Context, identity.OrganizationID, project.EnvironmentID, int64, int64) error {
	r.appliedSettings++
	return nil
}

func handOverRig(archived bool) (*Service, *heldProjects, *applies, *applyRepo, *eventtest.Recorder, job) {
	org := identity.NewOrganizationID()
	e := project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}
	p := project.Project{ID: project.NewID(), Organization: org, Name: "Pagos", Slug: "pagos", UpdatedBy: requester}
	if archived {
		now := time.Now()
		p.Status, p.ArchivedAt = project.StatusArchived, &now
	}
	d := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: p.ID, Environment: e.ID,
		Status: release.StatusApplying, Step: release.StepApply, Reason: release.ReasonDeploy, RequestedBy: requester, Revision: 3}
	projects := &heldProjects{usesProjects: usesProjects{p: p, e: e}}
	cluster := &applies{}
	repo := &applyRepo{approvalRepo: approvalRepo{dep: d}}
	events := &eventtest.Recorder{}
	svc := &Service{repo: repo, projects: projects, tx: directTx{}, cluster: cluster, events: events, builder: &buildCancels{}, now: time.Now}
	return svc, projects, cluster, repo, events, job{deployment: d, app: "pagos",
		target: release.Target{Organization: org, Project: p.ID, Environment: e.ID}}
}

func TestHandOverReachesTheClusterWhileTheProjectIsHeld(t *testing.T) {
	svc, projects, cluster, repo, events, j := handOverRig(false)
	if err := svc.handOver(context.Background(), j, release.DesiredApp{}, 1); err != nil {
		t.Fatal(err)
	}
	if projects.holds != 1 || cluster.applied != 1 || repo.appliedSettings != 1 || repo.settled || len(events.Events()) != 0 {
		t.Fatalf("holds %d, applied %d, settings %d, settled %v, events %v",
			projects.holds, cluster.applied, repo.appliedSettings, repo.settled, events.Types())
	}
}

// A deployment that gets to apply after its project was archived never
// reaches the cluster: it is cancelled (reason archived) and the run ends
// quietly, as if RetireArchived had stopped it.
func TestHandOverOfAnArchivedProjectCancelsWithoutTheCluster(t *testing.T) {
	svc, _, cluster, repo, events, j := handOverRig(true)
	err := svc.handOver(context.Background(), j, release.DesiredApp{}, 1)
	if !errors.Is(err, errSuperseded) {
		t.Fatalf("want the run to end quietly, got %v", err)
	}
	if cluster.applied != 0 || repo.appliedSettings != 0 {
		t.Fatalf("an archived app reached the cluster (%d applies)", cluster.applied)
	}
	if repo.dep.Status != release.StatusCancelled || !repo.settled {
		t.Fatalf("deployment %s, settled %v", repo.dep.Status, repo.settled)
	}
	got := events.Events()
	if len(got) != 1 || got[0].Type != release.EventDeploymentCancelled || got[0].Actor != requester {
		t.Fatalf("events %v", events.Types())
	}
	c, err := event.Decode[release.DeploymentCancelled](got[0])
	if err != nil || c.Reason != release.CancelArchived {
		t.Fatalf("cancelled %+v %v", c, err)
	}
}

// --- RetireArchived ---

// retireProjects is a project that may be missing, with its environments.
type retireProjects struct {
	heldProjects
	missing bool
	envs    []project.Environment
}

func (r *retireProjects) Find(ctx context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	if r.missing {
		return project.Project{}, errx.NotFound("project not found")
	}
	return r.heldProjects.Find(ctx, org, id)
}

func (r *retireProjects) ListEnvironments(context.Context, identity.OrganizationID, project.ID, project.EnvironmentFilter) ([]project.Environment, error) {
	return r.envs, nil
}

// retireRepo is approvalRepo plus the retirement write.
type retireRepo struct {
	approvalRepo
	retired []int64
}

func (r *retireRepo) Retire(_ context.Context, _ identity.OrganizationID, _ project.EnvironmentID, revision int64) error {
	r.retired = append(r.retired, revision)
	return nil
}

// retirements records what the cluster was told to remove.
type retirements struct {
	release.Cluster
	names []string
	revs  []int64
}

func (c *retirements) Retire(_ context.Context, _ release.Target, name string, revision int64) error {
	c.names, c.revs = append(c.names, name), append(c.revs, revision)
	return nil
}

// cancels records the runs stopped.
type cancels struct {
	workflow.Engine
	runs []workflow.RunID
}

func (c *cancels) Cancel(_ context.Context, _ identity.OrganizationID, run workflow.RunID, _ string) error {
	c.runs = append(c.runs, run)
	return nil
}

type retireRig struct {
	svc      *Service
	projects *retireProjects
	repo     *retireRepo
	cluster  *retirements
	engine   *cancels
	events   *eventtest.Recorder
}

func newRetireRig(archived bool, state release.EnvironmentState, active *release.Deployment) retireRig {
	svc, held, _, _, events, _ := handOverRig(archived)
	projects := &retireProjects{heldProjects: *held, envs: []project.Environment{held.e}}
	repo := &retireRepo{}
	repo.state = state
	if active != nil {
		repo.dep = *active
		repo.state.Active = &active.ID
	}
	cluster, engine := &retirements{}, &cancels{}
	svc.projects, svc.repo, svc.cluster, svc.engine = projects, repo, cluster, engine
	return retireRig{svc, projects, repo, cluster, engine, events}
}

func (r retireRig) run(t *testing.T) {
	t.Helper()
	if err := r.svc.RetireArchived(context.Background(), r.projects.p.Organization, r.projects.p.ID, requester); err != nil {
		t.Fatal(err)
	}
}

func deployed(rev int64) release.EnvironmentState {
	current := release.NewID()
	return release.EnvironmentState{App: "pagos", Revision: rev, Current: &current}
}

func TestRetireArchivedRetiresAboveEveryDeployment(t *testing.T) {
	r := newRetireRig(true, deployed(4), nil)
	r.run(t)
	if len(r.repo.retired) != 1 || r.repo.retired[0] != 5 {
		t.Fatalf("retired at %v, want [5] (above revision 4)", r.repo.retired)
	}
	if len(r.cluster.names) != 1 || r.cluster.names[0] != "pagos" || r.cluster.revs[0] != 5 {
		t.Fatalf("cluster told %v at %v", r.cluster.names, r.cluster.revs)
	}
	if r.projects.holds != 1 || len(r.events.Events()) != 0 || len(r.engine.runs) != 0 {
		t.Fatalf("holds %d, events %v, cancelled runs %v", r.projects.holds, r.events.Types(), r.engine.runs)
	}
}

func TestRetireArchivedStopsTheDeploymentInProgress(t *testing.T) {
	d := release.Deployment{ID: release.NewDeploymentID(), Run: workflow.NewRunID(), Status: release.StatusVerifying,
		Step: release.StepWaitReady, Reason: release.ReasonDeploy, RequestedBy: requester, Revision: 5}
	r := newRetireRig(true, deployed(5), &d)
	r.run(t)
	if r.repo.dep.Status != release.StatusCancelled || !r.repo.settled {
		t.Fatalf("deployment %s, settled %v", r.repo.dep.Status, r.repo.settled)
	}
	if len(r.repo.retired) != 1 || r.repo.retired[0] != 6 || len(r.cluster.names) != 1 {
		t.Fatalf("retired %v, cluster %v", r.repo.retired, r.cluster.names)
	}
	if len(r.engine.runs) != 1 || r.engine.runs[0] != d.Run {
		t.Fatalf("the stopped deployment's run must be cancelled: %v", r.engine.runs)
	}
	got := r.events.Events()
	if len(got) != 1 || got[0].Type != release.EventDeploymentCancelled {
		t.Fatalf("events %v", r.events.Types())
	}
}

// What leaves the cluster alone: a project gone or not archived, an
// environment that never deployed, and a retirement already done (the
// event redelivered).
func TestRetireArchivedLeavesAlone(t *testing.T) {
	retiredAt := time.Now()
	done := deployed(5)
	done.RetiredAt = &retiredAt
	for _, c := range []struct {
		name string
		rig  func() retireRig
	}{
		{"project gone", func() retireRig {
			r := newRetireRig(true, deployed(4), nil)
			r.projects.missing = true
			return r
		}},
		{"project not archived", func() retireRig { return newRetireRig(false, deployed(4), nil) }},
		{"never deployed", func() retireRig { return newRetireRig(true, release.EnvironmentState{}, nil) }},
		{"retired already", func() retireRig { return newRetireRig(true, done, nil) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.rig()
			r.run(t)
			if len(r.repo.retired) != 0 || len(r.cluster.names) != 0 || len(r.events.Events()) != 0 || len(r.engine.runs) != 0 {
				t.Fatalf("retired %v, cluster %v, events %v, runs %v", r.repo.retired, r.cluster.names, r.events.Types(), r.engine.runs)
			}
		})
	}
}
