package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestApprovalsEndToEnd: 3b over HTTP with the real wiring (worker, outbox,
// workflow engine). A direct action a rule holds is a 202 with a request, does
// nothing until someone else with the permission approves, and then runs as
// the requester; rejecting, cancelling, deciding twice, deciding your own,
// deciding without the permission and another organization all behave; a
// deployment of a production app parks at its authorize step as "waiting for
// approval", continues when approved, is cancelled when rejected, and a newer
// deployment takes the waiting one's request with it.
func TestApprovalsEndToEnd(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond}, Cluster: cluster})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	const itUser = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	dev := []string{permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.DeploymentsRollback, permission.AccessPublic}
	token := httpxtest.Token(org, dev...)                                                                     // the requester
	selfDeciding := httpxtest.Token(org, append(dev, permission.ApprovalsDecide)...)                          // the same person, who can also decide
	it := httpxtest.TokenAs(itUser, org, append(dev, permission.ProjectsTier, permission.ApprovalsDecide)...) // IT
	stranger := httpxtest.TokenAs(itUser, other, append(dev, permission.ApprovalsDecide)...)                  // IT of another company
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	tierURL := "/api/v1/projects/" + proj.ID.String() + "/tier"

	accessMode := func() release.AccessMode {
		t.Helper()
		res := checked(t, app, http.MethodGet, base+"/settings", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[release.Settings](t, res).Access.Mode
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out: %s", what)
	}
	getRequest := func(tok, id string) httpxtest.Response {
		return checked(t, app, http.MethodGet, "/api/v1/approvals/"+id, tok, nil)
	}
	decide := func(tok, id, verb string) httpxtest.Response {
		return checked(t, app, http.MethodPost, "/api/v1/approvals/"+id+"/"+verb, tok, map[string]any{"note": "revisado"})
	}
	statusOf := func(tok, id string) approval.Status {
		t.Helper()
		res := getRequest(tok, id)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[approval.Request](t, res).Status
	}
	hold := func(mode string) string { // an internal app asking for public: held
		t.Helper()
		res := checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": mode})
		httpxtest.Want(t, res, http.StatusAccepted)
		p := httpxtest.JSON[pendingBody](t, res)
		if p.Status != "pending_approval" || p.RequestID == "" || p.Message == "" {
			t.Fatalf("pending answer %s", res.Body)
		}
		return p.RequestID
	}

	// A direct action a rule holds: 202, nothing changes, one request however
	// many times it is asked (the same person, the same thing, still pending).
	id := hold("public")
	if again := hold("public"); again != id {
		t.Fatalf("asking twice opened another request: %s and %s", id, again)
	}
	if mode := accessMode(); mode == release.AccessPublic {
		t.Fatal("the held action ran before anyone approved it")
	}
	req := httpxtest.JSON[approval.Request](t, getRequest(token, id))
	if req.Status != approval.StatusPending || req.Action != "set_access" || req.Summary == "" || len(req.Rules) == 0 {
		t.Fatalf("the request %+v", req)
	}

	// Who sees and who decides: the requester sees their own but cannot decide
	// (no permission); another organization does not see it at all; the
	// requester WITH the permission still cannot decide their own (403
	// APPROVAL_SELF).
	httpxtest.Want(t, decide(token, id, "approve"), http.StatusForbidden)
	httpxtest.Want(t, getRequest(stranger, id), http.StatusNotFound)
	httpxtest.Want(t, decide(stranger, id, "approve"), http.StatusNotFound)
	if code := httpxtest.ErrorCode(decide(selfDeciding, id, "approve")); code != "APPROVAL_SELF" {
		t.Fatalf("self approval: %q", code)
	}
	if got := httpxtest.JSON[query.Paginated[approval.View]](t, checked(t, app, http.MethodGet, "/api/v1/approvals?status=pending&decidable=true", it, nil)); len(got.Items) != 1 {
		t.Fatalf("what IT can decide: %d", len(got.Items))
	}
	if got := httpxtest.JSON[query.Paginated[approval.View]](t, checked(t, app, http.MethodGet, "/api/v1/approvals?decidable=true", token, nil)); len(got.Items) != 0 {
		t.Fatalf("the requester cannot decide any: %d", len(got.Items))
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/approvals?status=nope", it, nil), http.StatusBadRequest)

	// IT approves: the action runs, as the requester, once.
	res = decide(it, id, "approve")
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[approval.Request](t, res); got.Status != approval.StatusApproved || got.DecisionNote != "revisado" {
		t.Fatalf("approved %+v", got)
	}
	waitFor("the approved action to run", func() bool { return accessMode() == release.AccessPublic })
	waitFor("the approval to be consumed", func() bool {
		return httpxtest.JSON[approval.Request](t, getRequest(it, id)).ConsumedAt != nil
	})
	if code := httpxtest.ErrorCode(decide(it, id, "reject")); code != "APPROVAL_DECIDED" {
		t.Fatalf("deciding twice: %q", code)
	}
	if code := httpxtest.ErrorCode(decide(it, id, "approve")); code != "APPROVAL_DECIDED" {
		t.Fatalf("approving twice: %q", code)
	}

	// Going back to team is not judged by any rule: it goes straight through.
	httpxtest.Want(t, checked(t, app, http.MethodPut, base+"/access", token, map[string]any{"mode": "team"}), http.StatusAccepted)
	if mode := accessMode(); mode != release.AccessTeam {
		t.Fatalf("mode %q", mode)
	}

	// Rejecting leaves things as they were; the requester can ask again.
	id = hold("public")
	httpxtest.Want(t, decide(it, id, "reject"), http.StatusOK)
	if s := statusOf(token, id); s != approval.StatusRejected {
		t.Fatalf("rejected: %q", s)
	}
	time.Sleep(200 * time.Millisecond) // the worker had its chance to (wrongly) run it
	if mode := accessMode(); mode != release.AccessTeam {
		t.Fatalf("a rejected action ran: %q", mode)
	}

	// Cancelling is the requester's alone.
	id = hold("public")
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/approvals/"+id+"/cancel", it, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/approvals/"+id+"/cancel", token, nil), http.StatusOK)
	if s := statusOf(it, id); s != approval.StatusCancelled {
		t.Fatalf("cancelled: %q", s)
	}
	httpxtest.Want(t, decide(it, id, "approve"), http.StatusConflict)

	// A deployment of a production app: accepted, parked at its authorize step.
	httpxtest.Want(t, checked(t, app, http.MethodPut, tierURL, it, map[string]any{"tier": "production"}), http.StatusOK)
	deploy := func(v string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": image(v), "port": 8080})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.Deployment](t, res)
	}
	deployment := func(d release.Deployment) release.Deployment {
		return httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+d.ID.String(), token, nil))
	}
	becomes := func(d release.Deployment, want release.Status) {
		t.Helper()
		waitFor("deployment to be "+string(want), func() bool { return deployment(d).Status == want })
	}
	requestOf := func(d release.Deployment) approval.Request {
		t.Helper()
		var found approval.Request
		waitFor("the deployment's request", func() bool {
			res := checked(t, app, http.MethodGet, "/api/v1/approvals?mine=true", token, nil)
			for _, r := range httpxtest.JSON[query.Paginated[approval.View]](t, res).Items {
				if r.Run == d.Run.String() {
					found = r.Request
					return true
				}
			}
			return false
		})
		return found
	}
	applied := len(cluster.Applied())

	d1 := deploy("1")
	becomes(d1, release.StatusWaitingApproval)
	r1 := requestOf(d1)
	if r1.Action != "deploy" || r1.Status != approval.StatusPending {
		t.Fatalf("the deployment's request %+v", r1)
	}
	if len(cluster.Applied()) != applied {
		t.Fatal("a deployment waiting for IT reached the cluster")
	}

	// A newer deployment replaces the waiting one and takes its request along.
	d2 := deploy("2")
	becomes(d1, release.StatusCancelled)
	waitFor("the replaced deployment's request to be cancelled", func() bool { return statusOf(it, r1.ID.String()) == approval.StatusCancelled })
	becomes(d2, release.StatusWaitingApproval)

	// Rejecting cancels the deployment; it never builds or applies.
	r2 := requestOf(d2)
	httpxtest.Want(t, decide(it, r2.ID.String(), "reject"), http.StatusOK)
	becomes(d2, release.StatusCancelled)

	// Approving lets it continue to ready; the approval is spent.
	d3 := deploy("3")
	becomes(d3, release.StatusWaitingApproval)
	r3 := requestOf(d3)
	httpxtest.Want(t, decide(it, r3.ID.String(), "approve"), http.StatusOK)
	becomes(d3, release.StatusReady)
	if len(cluster.Applied()) <= applied {
		t.Fatal("the approved deployment never reached the cluster")
	}
	if httpxtest.JSON[approval.Request](t, getRequest(it, r3.ID.String())).ConsumedAt == nil {
		t.Fatal("the approval was not consumed")
	}
}
