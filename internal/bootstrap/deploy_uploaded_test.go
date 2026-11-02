package bootstrap_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

// TestDeployUploaded: a source uploaded with a commit (what importing from
// git does) is deployed without uploading again; the deployment shows the
// commit; a source of another project, or one already swept, is refused and
// not discarded.
func TestDeployUploaded(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:  cluster,
		Runner:   buildfake.New(buildfake.Options{}),
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

	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth())
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead,
		permission.DeploymentsCreate)
	newProject := func(name string) (project.Project, string) {
		t.Helper()
		res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": name})
		httpxtest.Want(t, res, http.StatusCreated)
		p := httpxtest.JSON[project.Project](t, res)
		res = checked(t, app, http.MethodGet, "/api/v1/projects/"+p.ID.String()+"/environments", token, nil)
		env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
		return p, "/api/v1/projects/" + p.ID.String() + "/environments/" + env.ID.String()
	}
	proj, base := newProject("Visitas")
	other, _ := newProject("Otra")
	envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, proj.ID, project.EnvironmentFilter{})
	if err != nil || len(envs) == 0 {
		t.Fatalf("environments %v %v", envs, err)
	}
	env := envs[0].ID
	ref := build.GitRef{Provider: build.ProviderGitHub, Repository: "acme/visitas", Branch: "main",
		Commit: strings.Repeat("ab", 20), Title: "Fix the counter\n\nlong body", Author: "octocat"}
	importSource := func(p project.Project, git *build.GitRef) build.Source {
		t.Helper()
		src, err := platform.Builds.Commands.Upload(ctx, org, p.ID, build.Upload{Name: "acme/visitas", UploadedBy: user, Git: git},
			bytes.NewReader(folder(t, uuid.NewString())))
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	cfg := release.Config{Port: 3000, HealthPath: "/"}
	deploy := func(src build.Source) (release.Deployment, error) {
		return platform.Releases.Commands.DeployUploaded(ctx, org, proj.ID, env,
			release.DeployUploaded{Source: src.ID, Config: cfg, RequestedBy: user})
	}

	// 1. the commit travels from the source to the deployment, through the API
	src := importSource(proj, &ref)
	if src.Git == nil || src.Git.Title != "Fix the counter" {
		t.Fatalf("uploaded source %+v", src.Git)
	}
	d, err := deploy(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != release.StatusBuilding || d.Source == nil || d.Source.ID != src.ID || d.Source.Git == nil || *d.Source.Git != *src.Git {
		t.Fatalf("accepted %+v", d)
	}
	var got release.Deployment
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		res := checked(t, app, http.MethodGet, base+"/deployments/"+d.ID.String(), token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		if got = httpxtest.JSON[release.Deployment](t, res); got.Status.Finished() {
			break
		}
	}
	if got.Status != release.StatusReady || got.Source == nil || got.Source.Git == nil || *got.Source.Git != *src.Git {
		t.Fatalf("deployment %s is %s: source %+v error %+v", d.ID, got.Status, got.Source, got.Error)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if c := httpxtest.JSON[release.Current](t, checked(t, app, http.MethodGet, base+"/current", token, nil)); c.Active == nil {
			break
		}
	}
	// the list carries it too
	res := checked(t, app, http.MethodGet, base+"/deployments", token, nil)
	if items := httpxtest.JSON[query.Paginated[release.Deployment]](t, res).Items; len(items) != 1 || items[0].Source == nil ||
		items[0].Source.Git == nil || items[0].Source.Git.Commit != ref.Commit {
		t.Fatalf("listed %s", res.Body)
	}

	// 2. a folder's deployment has no commit
	res = upload(t, app, base+"/deployments", token, configPart(t, map[string]any{"port": 3000, "health_path": "/"}),
		part{name: "source", file: "visitas.tar.gz", body: folder(t, "folder")})
	httpxtest.Want(t, res, http.StatusAccepted)
	if f := httpxtest.JSON[release.Deployment](t, res); f.Source == nil || f.Source.Git != nil {
		t.Fatalf("folder deployment %s", res.Body)
	}

	// 3. another project's source is not found, and is kept
	foreign := importSource(other, &ref)
	var x *errx.Error
	if _, err := deploy(foreign); !errors.As(err, &x) || !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("source of another project: %v", err)
	}
	if kept, err := platform.Builds.Queries.FindSource(ctx, org, foreign.ID); err != nil || kept.Expired() {
		t.Fatalf("the refused source was dropped: %+v %v", kept, err)
	}
	if _, err := platform.Releases.Commands.DeployUploaded(ctx, org, proj.ID, env,
		release.DeployUploaded{Source: build.NewSourceID(), Config: cfg, RequestedBy: user}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("unknown source: %v", err)
	}

	// 4. an expired source is refused with its code
	gone := importSource(proj, &ref)
	if err := platform.Builds.Commands.Discard(ctx, org, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy(gone); !errors.As(err, &x) || x.Code != build.CodeSourceExpired {
		t.Fatalf("expired source: %v", err)
	}
}
