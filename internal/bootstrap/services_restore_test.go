package bootstrap_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// TestRestoreTheWholeTopology is MS-3c's exit criterion: a version is the
// app's whole topology, so going back to one (a rollback, or the platform's
// own restore after a failed deployment) puts back exactly its services —
// the api a version did not have is retired — and IT's rules see the
// services a deploy or rollback brings, what changes and whether the
// topology does; a change of services can be held for IT's approval.
func TestRestoreTheWholeTopology(t *testing.T) {
	ctx := context.Background()
	s := newServicesApp(t, buildfake.Options{})
	token := httpxtest.Token(s.org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.DeploymentsRollback, permission.ProjectsTier)
	setServices := func(items ...map[string]any) httpxtest.Response {
		t.Helper()
		return checked(t, s.app, http.MethodPut, s.pbase+"/services", token, map[string]any{"items": items})
	}
	web := map[string]any{"name": "web", "root": "apps/web", "route": "/"}
	api := map[string]any{"name": "api", "root": "apps/api", "route": "/api", "strip_prefix": true}
	jobs := map[string]any{"name": "jobs", "kind": "worker", "root": "apps/jobs"}
	code := map[string]string{"apps/web": "w", "apps/api": "a", "apps/jobs": "j"}
	cfg := map[string]any{"port": 3000, "health_path": "/"}
	ready := func(d release.Deployment) release.Deployment {
		t.Helper()
		if d = s.await(t, d.ID); d.Status != release.StatusReady || d.Release == nil {
			t.Fatalf("deployment %+v %+v", d, d.Error)
		}
		return d
	}
	running := func() []string { // the services the cluster was last told to run
		t.Helper()
		a := s.cluster.Applied()
		if len(a) == 0 {
			t.Fatal("nothing applied")
		}
		var names []string
		for _, svc := range a[len(a)-1].App.Services {
			names = append(names, svc.Name)
		}
		return names
	}
	settled := func() release.Current {
		t.Helper()
		var c release.Current
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			c = httpxtest.JSON[release.Current](t, checked(t, s.app, http.MethodGet, s.base+"/current", token, nil))
			if c.Active == nil && c.Deployment != nil && c.Deployment.Status.Finished() {
				return c
			}
		}
		t.Fatalf("the environment never settled: %+v", c)
		return c
	}

	// v1 runs the web alone, v2 web and api.
	httpxtest.Want(t, setServices(web), http.StatusOK)
	v1 := ready(s.publish(t, "portal", monorepoOf(t, code), cfg))
	httpxtest.Want(t, setServices(web, api), http.StatusOK)
	code["apps/api"] = "a2"
	v2 := ready(s.publish(t, "portal", monorepoOf(t, code), cfg))
	if got := running(); !slices.Equal(got, []string{"web", "api"}) {
		t.Fatalf("v2 runs %v", got)
	}

	// Going back to v1 retires the api v1 did not have.
	res := checked(t, s.app, http.MethodPost, s.base+"/deployments/"+v1.ID.String()+"/rollback", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	back := ready(httpxtest.JSON[release.Deployment](t, res))
	if *back.Release != *v1.Release || !slices.Equal(running(), []string{"web"}) {
		t.Fatalf("rollback to v1 %+v runs %v", back, running())
	}
	if c := settled(); c.Release == nil || len(c.Release.Services) != 1 || c.Release.Services[0].Name != "web" {
		t.Fatalf("current after the rollback %+v", c.Release)
	}

	// A rule on the topology: going forward again to v2 adds a service, so
	// it is refused at once, before anything is created.
	rule, err := s.platform.Policy.Commands.Create(ctx, s.org, policy.Create{CreatedBy: s.user, Draft: policy.Draft{
		Name: "Una restauración no cambia los servicios", Expression: `action == 'rollback' && topology_changed`, Effect: policy.EffectDeny}})
	if err != nil {
		t.Fatal(err)
	}
	res = checked(t, s.app, http.MethodPost, s.base+"/deployments/"+v2.ID.String()+"/rollback", token, nil)
	if httpxtest.Want(t, res, http.StatusForbidden); httpxtest.ErrorCode(res) != gate.CodeDenied {
		t.Fatalf("rollback adding the api: %s", res.Body)
	}
	if err := s.platform.Policy.Commands.Delete(ctx, s.org, rule.ID, policy.Delete{DeletedBy: s.user}); err != nil {
		t.Fatal(err)
	}
	res = checked(t, s.app, http.MethodPost, s.base+"/deployments/"+v2.ID.String()+"/rollback", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	ready(httpxtest.JSON[release.Deployment](t, res))
	if got := running(); !slices.Equal(got, []string{"web", "api"}) {
		t.Fatalf("back on v2 runs %v", got)
	}

	// A rule on the services a deploy brings: no workers here. Judged from
	// the layout, before the folder is stored.
	httpxtest.Want(t, setServices(web, api, jobs), http.StatusOK)
	rule, err = s.platform.Policy.Commands.Create(ctx, s.org, policy.Create{CreatedBy: s.user, Draft: policy.Draft{
		Name: "Sin workers", Expression: `action == 'deploy' && services.exists(s, s.kind == 'worker')`, Effect: policy.EffectDeny}})
	if err != nil {
		t.Fatal(err)
	}
	res = upload(t, s.app, s.base+"/deployments", token, configPart(t, cfg), part{name: "source", file: "portal.tar.gz", body: monorepoOf(t, code)})
	if httpxtest.Want(t, res, http.StatusForbidden); httpxtest.ErrorCode(res) != gate.CodeDenied {
		t.Fatalf("a deploy with a worker: %s", res.Body)
	}
	if err := s.platform.Policy.Commands.Delete(ctx, s.org, rule.ID, policy.Delete{DeletedBy: s.user}); err != nil {
		t.Fatal(err)
	}

	// The platform's own restore after a failed deployment puts back the
	// whole version that served: web and api, the new jobs retired.
	s.unhealthy.Store("jobs", true)
	bad := s.await(t, s.publish(t, "portal", monorepoOf(t, code), cfg).ID)
	if bad.Status != release.StatusFailed {
		t.Fatalf("the deployment with the failing worker %+v", bad)
	}
	c := settled()
	if c.Deployment.Reason != release.ReasonAutoRollback || c.Release == nil || c.Release.ID != *v2.Release {
		t.Fatalf("current after the failed deployment %+v", c.Deployment)
	}
	if got := running(); !slices.Equal(got, []string{"web", "api"}) {
		t.Fatalf("the restore runs %v", got)
	}
	s.unhealthy.Delete("jobs")

	// Production: changing the services waits for IT (the built-in rule is
	// born off; IT switches it on). Nothing changes until IT approves; then
	// the change runs, as who asked, once.
	httpxtest.Want(t, checked(t, s.app, http.MethodPut, s.pbase+"/tier", token, map[string]any{"tier": "production"}), http.StatusOK)
	switchOn(t, s.platform, s.org, policy.BuiltinProductionServices)
	res = setServices(web)
	httpxtest.Want(t, res, http.StatusAccepted)
	held := httpxtest.JSON[pendingBody](t, res)
	if held.Status != "pending_approval" || held.RequestID == "" {
		t.Fatalf("held change %s", res.Body)
	}
	listed := func() []string {
		t.Helper()
		out := httpxtest.JSON[struct {
			Items []project.Service `json:"items"`
		}](t, checked(t, s.app, http.MethodGet, s.pbase+"/services", token, nil))
		var names []string
		for _, svc := range out.Items {
			names = append(names, svc.Name)
		}
		return names
	}
	if got := listed(); !slices.Equal(got, []string{"web", "api", "jobs"}) {
		t.Fatalf("a held change stored %v", got)
	}
	it := httpxtest.TokenAs("0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", s.org, permission.ProjectsRead, permission.ApprovalsDecide)
	req := httpxtest.JSON[approval.Request](t, checked(t, s.app, http.MethodGet, "/api/v1/approvals/"+held.RequestID, it, nil))
	if req.Action != gate.ActionSetServices || !req.Environment.IsZero() || req.Summary == "" {
		t.Fatalf("the request IT reads %+v", req)
	}
	httpxtest.Want(t, checked(t, s.app, http.MethodPost, "/api/v1/approvals/"+held.RequestID+"/approve", it, map[string]any{"note": "ok"}), http.StatusOK)
	for deadline := time.Now().Add(15 * time.Second); !slices.Equal(listed(), []string{"web"}); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the approved change never ran: %v", listed())
		}
	}
}
