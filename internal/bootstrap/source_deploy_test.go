package bootstrap_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
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

// folder is a tar.gz of a tiny app; marker makes each one distinct.
func folder(t *testing.T, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"package.json": `{"name":"visitas","scripts":{"start":"node index.js"}}`,
		"index.js":     "// " + marker + "\nrequire('http').createServer((q, s) => s.end('ok')).listen(3000)\n",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
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

// part is one multipart part, in order.
type part struct {
	name, file string
	body       []byte
}

// upload posts a multipart form and validates the response against the
// documented operation, like checked.
func upload(t *testing.T, app http.Handler, path, token string, parts ...part) httpxtest.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		var dst io.Writer
		var err error
		if p.file != "" {
			dst, err = w.CreateFormFile(p.name, p.file)
		} else {
			dst, err = w.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dst.Write(p.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	r := httpxtest.Serve(app, req)
	validate(t, http.MethodPost, path, r)
	return r
}

func configPart(t *testing.T, v map[string]any) part {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return part{name: "config", body: raw}
}

// archiving is a source that archives its project while it uploads.
type archiving struct {
	io.Reader
	archive func()
	once    bool
}

func (a *archiving) Read(p []byte) (int, error) {
	if !a.once {
		a.once = true
		a.archive()
	}
	return a.Reader.Read(p)
}

// TestDeploySourceArchivedWhileUploading: an app archived while its folder
// uploads gets no deployment, and the uploaded source is dropped at once
// (not left to the orphan sweep).
func TestDeploySourceArchivedWhileUploading(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Runner: buildfake.New(buildfake.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	pid, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: "Visitas", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	p := project.Project{ID: pid}
	envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, p.ID, project.EnvironmentFilter{})
	if err != nil || len(envs) == 0 {
		t.Fatalf("environments %v %v", envs, err)
	}
	body := &archiving{Reader: bytes.NewReader(folder(t, "x")), archive: func() {
		if err := platform.Projects.Commands.Archive(ctx, org, p.ID, project.Archive{ArchivedBy: user}); err != nil {
			t.Errorf("archive: %v", err)
		}
	}}
	_, err = platform.Releases.Commands.DeploySource(ctx, org, p.ID, envs[0].ID,
		release.DeploySource{Name: "visitas", Config: release.Config{Port: 3000, HealthPath: "/"}, RequestedBy: user}, body)
	var x *errx.Error
	if !errors.As(err, &x) || x.Code != release.CodeArchived {
		t.Fatalf("deploy to an app archived meanwhile: %v", err)
	}
	var sources []struct {
		Expired bool `db:"expired"`
	}
	if err := db.SelectContext(ctx, &sources, `SELECT expired_at IS NOT NULL AS expired FROM build.sources WHERE organization_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || !sources[0].Expired {
		t.Fatalf("the uploaded source was kept: %+v", sources)
	}
	if n, _ := platform.Releases.Queries.ListDeployments(ctx, org, p.ID, envs[0].ID, release.DeploymentFilter{}, query.Pagination{}); n.Page.Total != 0 {
		t.Fatalf("deployments %+v", n.Items)
	}
}

// TestDeployFromSource is phase 1b's exit criterion on the control plane:
// publishing a folder builds it, records its release with the configuration
// and goes live through build → apply → wait_ready → promote; a failed build
// never touches the cluster (nothing to restore); a newer deployment
// cancels the build in progress; "Volver a esta" redeploys the built
// release without building again; and the trail has all of it.
func TestDeployFromSource(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	// builds end at their first observation; "lento" runs until cancelled
	runner := buildfake.New(buildfake.Options{Behavior: func(j build.Job) buildfake.Outcome {
		switch j.Source.Name {
		case "roto":
			return buildfake.Outcome{Reason: build.ReasonUnsupported, Message: "no start script"}
		case "lento":
			return buildfake.Outcome{Hold: true}
		}
		return buildfake.Outcome{}
	}})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:  cluster,
		Runner:   runner,
		Build:    buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release:  releasesvc.Options{PollEvery: 20 * time.Millisecond},
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
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate, permission.DeploymentsRollback, permission.AuditRead)
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Visitas"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	base := "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String()
	target := release.Target{Organization: org, Project: proj.ID, Environment: env.ID}
	cfg := map[string]any{"port": 3000, "health_path": "/", "env": map[string]string{"TZ": "America/Lima"}, "message": "Primera\tversión"}

	publish := func(name, marker string) release.Deployment {
		t.Helper()
		res := upload(t, app, base+"/deployments", token, configPart(t, cfg), part{name: "source", file: name, body: folder(t, marker)})
		httpxtest.Want(t, res, http.StatusAccepted)
		d := httpxtest.JSON[release.Deployment](t, res)
		if d.Status != release.StatusBuilding || d.Step != release.StepBuild || d.Release != nil || d.Build == nil ||
			d.Source == nil || d.Source.Name+".tar.gz" != name || len(d.Steps) != 4 || d.Steps[0].Name != release.StepBuild ||
			d.Message != "Primera versión" {
			t.Fatalf("accepted %+v", d)
		}
		return d
	}
	await := func(id release.DeploymentID, want release.Status) release.Deployment {
		t.Helper()
		var d release.Deployment
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status == want {
				return d
			}
			if d.Status.Finished() {
				break
			}
		}
		t.Fatalf("deployment %s is %s at %s (want %s): %+v", id, d.Status, d.Step, want, d.Error)
		return d
	}
	current := func() release.Current {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/current", token, nil)
			if c := httpxtest.JSON[release.Current](t, res); c.Active == nil {
				return c
			}
		}
		t.Fatal("the environment never settled")
		return release.Current{}
	}

	// 1. a folder goes live: its build's image, with the deployment's configuration
	v1 := await(publish("visitas.tar.gz", "1").ID, release.StatusReady)
	if v1.Release == nil || !strings.HasPrefix(v1.Image, "registry.odyssey.localhost/") || len(v1.Steps) != 4 {
		t.Fatalf("built deployment %+v", v1)
	}
	for _, s := range v1.Steps {
		if s.Status != release.StepDone {
			t.Fatalf("steps %+v", v1.Steps)
		}
	}
	c := current()
	if c.Release == nil || c.Release.ID != *v1.Release || c.Release.Image != v1.Image || c.Release.Config.Port != 3000 ||
		c.Release.Config.Env["TZ"] != "America/Lima" || cluster.Running(target, "visitas") != v1.Image {
		t.Fatalf("current after a build %+v", c)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/builds/"+v1.Build.String(), token, nil)
	if httpxtest.Want(t, res, http.StatusOK); httpxtest.JSON[build.Build](t, res).Status != build.StatusSucceeded {
		t.Fatalf("build %s", res.Body)
	}

	// 2. a build that fails fails the deployment with its code and leaves
	// the app as it was: no restore, the cluster never changed
	broken := await(publish("roto.tar.gz", "2").ID, release.StatusFailed)
	if broken.Error == nil || broken.Error.Code != build.CodeUnsupported || broken.Step != release.StepBuild ||
		broken.Release != nil || broken.Steps[0].Status != release.StepFailed || broken.Steps[1].Status != release.StepSkipped {
		t.Fatalf("failed build %+v", broken)
	}
	if c = current(); c.Deployment.ID != v1.ID || cluster.Running(target, "visitas") != v1.Image {
		t.Fatalf("current after a failed build %+v", c)
	}

	// 3. "Volver a esta" on a built version deploys its release, no build
	v2 := await(publish("visitas.tar.gz", "3").ID, release.StatusReady)
	res = checked(t, app, http.MethodPost, base+"/deployments/"+v1.ID.String()+"/rollback", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	back := httpxtest.JSON[release.Deployment](t, res)
	if back.Build != nil || back.Release == nil || *back.Release != *v1.Release || len(back.Steps) != 3 {
		t.Fatalf("rollback of a built version %+v", back)
	}
	await(back.ID, release.StatusReady)
	if c = current(); cluster.Running(target, "visitas") != v1.Image || v2.Image == v1.Image {
		t.Fatalf("current after rollback %+v", c)
	}

	// 4. a newer deployment cancels the one still building, and its build
	superseded := publish("lento.tar.gz", "4")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		res := httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/builds/"+superseded.Build.String(), token, nil)
		if res.Status == http.StatusOK && httpxtest.JSON[build.Build](t, res).Status == build.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the build never started running")
		}
	}
	v4 := await(publish("visitas.tar.gz", "5").ID, release.StatusReady)
	gone := await(superseded.ID, release.StatusCancelled)
	if gone.Release != nil || gone.Error != nil || gone.Build == nil {
		t.Fatalf("superseded deployment %+v", gone)
	}
	// the build's own run stops it in the runner (archive), asynchronously
	for deadline := time.Now().Add(15 * time.Second); !runner.Cancelled(*gone.Build); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the superseded build kept running in the runner")
		}
	}
	res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/builds/"+gone.Build.String(), token, nil)
	if httpxtest.Want(t, res, http.StatusOK); httpxtest.JSON[build.Build](t, res).Status != build.StatusCancelled {
		t.Fatalf("superseded build %s", res.Body)
	}
	if c = current(); cluster.Running(target, "visitas") != v4.Image {
		t.Fatalf("current after supersede %+v", c)
	}

	// refusals: the form's order and contents
	res = upload(t, app, base+"/deployments", token, part{name: "source", file: "a.tar.gz", body: folder(t, "x")}, configPart(t, cfg))
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorField(res) != "config" {
		t.Fatalf("source first: %s", res.Body)
	}
	res = upload(t, app, base+"/deployments", token, configPart(t, cfg))
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorField(res) != "source" {
		t.Fatalf("no source: %s", res.Body)
	}
	res = upload(t, app, base+"/deployments", token, configPart(t, map[string]any{"port": 70000}), part{name: "source", file: "a.tar.gz", body: folder(t, "x")})
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorField(res) != "port" {
		t.Fatalf("bad config: %s", res.Body)
	}
	res = upload(t, app, base+"/deployments", token, configPart(t, map[string]any{"message": strings.Repeat("a", release.MaxMessage+1)}), part{name: "source", file: "a.tar.gz", body: folder(t, "x")})
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorField(res) != "message" {
		t.Fatalf("long message: %s", res.Body)
	}
	res = upload(t, app, base+"/deployments", token, configPart(t, cfg), part{name: "source", file: "a.tar.gz", body: []byte("not a tarball")})
	if httpxtest.Want(t, res, http.StatusBadRequest); httpxtest.ErrorCode(res) != build.CodeSourceInvalid || httpxtest.ErrorField(res) != "source" {
		t.Fatalf("not an archive: %s", res.Body)
	}
	reader := httpxtest.Token(org, permission.ProjectsRead, permission.DeploymentsRead)
	res = upload(t, app, base+"/deployments", reader, configPart(t, cfg), part{name: "source", file: "a.tar.gz", body: folder(t, "x")})
	httpxtest.Want(t, res, http.StatusForbidden)

	// the trail: three releases built, one failed build, one superseded
	count := func(typ string) int {
		res := checked(t, app, http.MethodGet, "/api/v1/audit?type="+typ, token, nil)
		return httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Page.Total
	}
	want := map[string]int{release.EventCreated: 3, release.EventDeploymentStarted: 6, release.EventDeploymentReady: 4,
		release.EventDeploymentFailed: 1, release.EventDeploymentCancelled: 1}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		missing := ""
		for typ, n := range want {
			if got := count(typ); got != n {
				missing += typ + " "
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trail: wrong counts for %s", missing)
		}
	}
}
