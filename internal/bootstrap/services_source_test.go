package bootstrap_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// monorepo is a tar.gz with one tiny app in each of folders (marker makes
// each upload distinct).
func monorepo(t *testing.T, marker string, folders ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, dir := range folders {
		for name, body := range map[string]string{
			"package.json": `{"name":"` + strings.ReplaceAll(dir, "/", "-") + `","scripts":{"start":"node index.js"}}`,
			"index.js":     "// " + marker + "\nrequire('http').createServer((q, s) => s.end('ok')).listen(3000)\n",
		} {
			if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDeployServicesFromSource is MS-3a's exit criterion: an app of three
// services (web, api, a worker), each in its own folder, deploys from one
// upload as three builds — each of its own folder, all at once — and one
// version with the three images; one build that fails fails the deployment
// naming its service, cancels the others and publishes nothing; a folder
// missing from the code fails with SOURCE_ROOT_MISSING; and the version is
// made of the services as they were when it was asked for.
func TestDeployServicesFromSource(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := fleetca.Generate("test fleet CA", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := fleetca.Parse(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	// "roto": the api's build fails and the others run until cancelled;
	// "lento": every build runs until cancelled.
	runner := buildfake.New(buildfake.Options{Behavior: func(j build.Job) buildfake.Outcome {
		switch {
		case j.Source.Name == "roto" && j.Source.Root == "apps/api":
			return buildfake.Outcome{Reason: build.ReasonFailed, Message: "npm ERR! missing script: build"}
		case j.Source.Name == "roto", j.Source.Name == "lento":
			return buildfake.Outcome{Hold: true}
		}
		return buildfake.Outcome{}
	}})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:  workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:   cluster,
		Runner:    runner,
		Build:     buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release:   releasesvc.Options{PollEvery: 20 * time.Millisecond},
		Authority: ca,
		Gateway:   "localhost:0",
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
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Portal"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	pbase := "/api/v1/projects/" + proj.ID.String()
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, checked(t, app, http.MethodGet, pbase+"/environments", token, nil)).Items[0]
	base := pbase + "/environments/" + env.ID.String()
	target := release.Target{Organization: org, Project: proj.ID, Environment: env.ID}

	// a cluster that runs several services, then the app's three
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.Fleet.Agents.Connect(ctx, fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial},
		fleet.NewSession(), fleet.Agent{Version: "test", Capabilities: fleet.Capabilities{APIs: []string{fleet.CapabilityAccess, fleet.CapabilityServices}}},
		"10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPut, pbase+"/services", token, map[string]any{"items": []map[string]any{
		{"name": "web", "root": "apps/web", "route": "/"},
		{"name": "api", "root": "apps/api", "route": "/api", "strip_prefix": true},
		{"name": "jobs", "kind": "worker", "root": "apps/jobs"},
	}}), http.StatusOK)

	cfg := map[string]any{"port": 3000, "health_path": "/"}
	publish := func(name string, body []byte) release.Deployment {
		t.Helper()
		res := upload(t, app, base+"/deployments", token, configPart(t, cfg), part{name: "source", file: name + ".tar.gz", body: body})
		httpxtest.Want(t, res, http.StatusAccepted)
		return httpxtest.JSON[release.Deployment](t, res)
	}
	await := func(id release.DeploymentID) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if d = httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)); d.Status.Finished() {
				return d
			}
		}
		t.Fatalf("deployment %s is %s at %s", id, d.Status, d.Step)
		return d
	}
	buildOf := func(id build.ID) build.Build {
		t.Helper()
		res := checked(t, app, http.MethodGet, pbase+"/builds/"+id.String(), token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[build.Build](t, res)
	}
	var sources []struct {
		Name string `db:"name"`
		Root string `db:"root"`
	}
	rootsOf := func(name string) []string {
		t.Helper()
		sources = nil
		if err := db.SelectContext(ctx, &sources, `SELECT name, root FROM build.sources
			WHERE organization_id = $1 AND name = $2 AND parent_id IS NOT NULL ORDER BY root`, org, name); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(sources))
		for i, s := range sources {
			out[i] = s.Root
		}
		return out
	}

	// 1. one upload, three builds (each of its folder), one version
	started := publish("portal", monorepo(t, "1", "apps/web", "apps/api", "apps/jobs"))
	if len(started.Builds) != 3 || started.Builds[0].Name != "web" || started.Builds[1].Root != "apps/api" ||
		started.Builds[2].Kind != project.ServiceWorker || started.Build == nil || *started.Build != started.Builds[0].Build {
		t.Fatalf("accepted %+v", started.Builds)
	}
	v1 := await(started.ID)
	if v1.Status != release.StatusReady || v1.Release == nil {
		t.Fatalf("deployment of three services %+v %+v", v1, v1.Error)
	}
	rel := httpxtest.JSON[release.Release](t, checked(t, app, http.MethodGet, pbase+"/releases/"+v1.Release.String(), token, nil))
	if len(rel.Services) != 3 {
		t.Fatalf("version's services %+v", rel.Services)
	}
	images := map[string]bool{}
	for i, s := range rel.Services {
		b := buildOf(v1.Builds[i].Build)
		if b.Status != build.StatusSucceeded || s.Image != b.Image || s.Name != v1.Builds[i].Name {
			t.Fatalf("service %s: image %s, build %+v", s.Name, s.Image, b)
		}
		images[s.Image] = true
	}
	web, api, jobs := rel.Services[0], rel.Services[1], rel.Services[2]
	if len(images) != 3 || rel.Image != web.Image || web.Port != 3000 || web.HealthPath != "/" || api.Route != "/api" || !api.StripPrefix ||
		jobs.Kind != project.ServiceWorker || jobs.Port != 0 || jobs.HealthPath != "" || jobs.Route != "" {
		t.Fatalf("version's services %+v", rel.Services)
	}
	if got := rootsOf("portal"); strings.Join(got, ",") != "apps/api,apps/jobs,apps/web" {
		t.Fatalf("derived sources %v", got)
	}
	if a := cluster.Applied(); len(a) == 0 || len(a[len(a)-1].App.Services) != 3 || a[len(a)-1].App.Services[1] != api {
		t.Fatalf("the cluster was not told the three services: %+v", a)
	}

	// 2. all or nothing: the api's build fails → the deployment fails naming
	// it, the other builds are cancelled, no version, v1 stays online
	broken := await(publish("roto", monorepo(t, "2", "apps/web", "apps/api", "apps/jobs")).ID)
	if broken.Status != release.StatusFailed || broken.Release != nil || broken.Error == nil || broken.Error.Code != build.CodeFailed ||
		!strings.Contains(broken.Error.Message, "api") || len(broken.Builds) != 3 {
		t.Fatalf("a failed service build %+v %+v", broken, broken.Error)
	}
	for _, sb := range []release.ServiceBuild{broken.Builds[0], broken.Builds[2]} {
		for deadline := time.Now().Add(15 * time.Second); buildOf(sb.Build).Status != build.StatusCancelled; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the %s build kept running after the api's failed: %+v", sb.Name, buildOf(sb.Build))
			}
		}
	}
	var versions int
	if err := db.GetContext(ctx, &versions, `SELECT count(*) FROM release.releases WHERE project_id = $1`, proj.ID); err != nil || versions != 1 {
		t.Fatalf("versions after a failed service build: %d %v", versions, err)
	}
	if cur := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil)); cur.Release == nil ||
		cur.Release.ID != *v1.Release {
		t.Fatalf("online after a failed service build %+v", cur)
	}
	if a := cluster.Applied(); a[len(a)-1].App.Services[0].Image != web.Image || a[len(a)-1].Target != target {
		t.Fatalf("the cluster was touched by a failed service build: %+v", a[len(a)-1])
	}

	// 3. a service's folder missing from the code
	missing := await(publish("sinjobs", monorepo(t, "3", "apps/web", "apps/api")).ID)
	if missing.Status != release.StatusFailed || missing.Error == nil || missing.Error.Code != build.CodeRootMissing ||
		!strings.Contains(missing.Error.Message, "jobs") {
		t.Fatalf("a missing folder %+v %+v", missing, missing.Error)
	}

	// 4. the version is made of the services as asked for: a change while
	// it builds waits for the next deployment
	slow := publish("lento", monorepo(t, "4", "apps/web", "apps/api", "apps/jobs"))
	for _, sb := range slow.Builds {
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			res := httpxtest.Do(t, app, http.MethodGet, pbase+"/builds/"+sb.Build.String(), token, nil)
			if res.Status == http.StatusOK && httpxtest.JSON[build.Build](t, res).Status == build.StatusRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the %s build never started running", sb.Name)
			}
		}
	}
	httpxtest.Want(t, checked(t, app, http.MethodPut, pbase+"/services", token, map[string]any{"items": []map[string]any{
		{"name": "web", "root": "apps/web", "route": "/"},
	}}), http.StatusOK)
	got := httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, base+"/deployments/"+slow.ID.String(), token, nil))
	if len(got.Builds) != 3 || got.Builds[2].Name != "jobs" {
		t.Fatalf("the deployment's services changed with the app's: %+v", got.Builds)
	}
	// a newer deployment (of the one service now) cancels all of its builds
	v5 := await(publish("portal", monorepo(t, "5", "apps/web")).ID)
	gone := await(slow.ID)
	if v5.Status != release.StatusReady || len(v5.Builds) != 1 || gone.Status != release.StatusCancelled {
		t.Fatalf("supersede %+v / %+v", v5, gone)
	}
	for _, sb := range gone.Builds {
		for deadline := time.Now().Add(15 * time.Second); buildOf(sb.Build).Status != build.StatusCancelled; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the superseded %s build kept running", sb.Name)
			}
		}
	}
}
