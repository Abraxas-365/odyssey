package bootstrap_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestScanGatesAProductionDeployment: 3c with the real wiring. A build's scan
// is recorded after it (the build never waits), and the deployment's
// authorize_scan step judges it with the built-in rule "Producción no publica
// con vulnerabilidades críticas": criticals fail the deployment with
// POLICY_DENIED before anything reaches the cluster; a clean scan, a scan that
// could not be done (known=false) and a sandbox app go live.
func TestScanGatesAProductionDeployment(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var critical atomic.Int32 // what the next scan finds (-1: it cannot be done)
	runner := buildfake.New(buildfake.Options{Scan: func(b build.Build) build.ScanObservation {
		switch n := int(critical.Load()); {
		case n < 0:
			return build.ScanObservation{Phase: build.ScanDone, Scan: build.Scan{Digest: build.DigestOf(b.Image), Error: build.ScanFailedMessage}}
		default:
			return build.ScanObservation{Phase: build.ScanDone, Scan: build.Scan{Digest: build.DigestOf(b.Image), Critical: n, High: 4, Tool: "trivy test"}}
		}
	}})
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:  cluster, Runner: runner,
		Build:   buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release: releasesvc.Options{PollEvery: 20 * time.Millisecond},
	})
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
	org := identity.NewOrganizationID()
	switchOff(t, platform, org, policy.BuiltinProductionDeploy) // only the scan rule stands in the way here
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.ProjectsTier,
		permission.DeploymentsRead, permission.DeploymentsCreate)
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Pagos"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	httpxtest.Want(t, checked(t, app, http.MethodPut, "/api/v1/projects/"+proj.ID.String()+"/tier", token, map[string]any{"tier": "production"}), http.StatusOK)

	cfg := map[string]any{"port": 3000, "health_path": "/"}
	publish := func(marker string) release.Deployment {
		t.Helper()
		res := upload(t, app, base+"/deployments", token, configPart(t, cfg), part{name: "source", file: "pagos.tar.gz", body: folder(t, marker)})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.Deployment](t, res)
	}
	settled := func(d release.Deployment) release.Deployment {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+d.ID.String(), token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status.Finished() {
				return d
			}
		}
		t.Fatalf("deployment %s never finished: %s at %s", d.ID, d.Status, d.Step)
		return d
	}
	scanOf := func(d release.Deployment) *build.Scan {
		t.Helper()
		if d.Build == nil {
			t.Fatal("no build")
		}
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/builds/"+d.Build.String(), token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if s := httpxtest.JSON[build.Build](t, res).Scan; s != nil {
				return s
			}
		}
		t.Fatal("the scan was never recorded")
		return nil
	}

	// criticals: refused after the build, nothing released, the scan is on the build
	critical.Store(3)
	d := settled(publish("critical"))
	if d.Status != release.StatusFailed || d.Error == nil || d.Error.Code != release.CodePolicyDenied || d.Step != release.StepBuild ||
		cluster.Running(release.Target{Organization: org, Project: proj.ID, Environment: env.ID}, "pagos") != "" {
		t.Fatalf("a production app with criticals: %s at %s, error %+v", d.Status, d.Step, d.Error)
	}
	if s := scanOf(d); !s.Known() || s.Critical != 3 || s.High != 4 || s.Tool != "trivy test" {
		t.Fatalf("scan %+v", s)
	}

	// clean: goes live
	critical.Store(0)
	if d := settled(publish("clean")); d.Status != release.StatusReady {
		t.Fatalf("a clean image: %s, error %+v", d.Status, d.Error)
	}

	// could not be scanned: known=false, the rule (scan.known && …) does not match
	critical.Store(-1)
	d = settled(publish("unknown"))
	if d.Status != release.StatusReady {
		t.Fatalf("an unscanned image: %s, error %+v", d.Status, d.Error)
	}
	if s := scanOf(d); s.Known() || s.Error == "" {
		t.Fatalf("scan %+v, want it recorded as not done with a reason", s)
	}
}
