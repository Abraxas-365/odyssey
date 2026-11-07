package bootstrap_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
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
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
	"github.com/Abraxas-365/odyssey/internal/source/sourcesvc"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestGitHubHTTP: the whole GitHub integration through the API, with the
// workers running on a fake provider: link an installation (the public
// callback, with and without a good state), browse repositories, connect a
// project (the import runs and deploys the commit), publish again,
// disconnect.
func TestGitHubHTTP(t *testing.T) {
	const (
		console = "http://console.test"
		sha1    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sha2    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	fake := sourcefake.New(sourcefake.Options{})
	fake.AddInstallation(source.InstallationInfo{ID: 7, Account: "acme", AccountType: "Organization", AllRepositories: true}, "code-1")
	fake.AddRepo(7, source.Repo{ID: 42, FullName: "acme/web", DefaultBranch: "main"},
		map[string]source.Commit{"main": {SHA: sha1, Title: "First", Author: "ana"}, "dev": {SHA: sha2, Title: "Dev", Author: "luis"}})
	fake.AddRepo(7, source.Repo{ID: 43, FullName: "acme/api", DefaultBranch: "main", Private: true},
		map[string]source.Commit{"main": {SHA: sha2, Title: "api", Author: "ana"}})
	fake.SetFiles(42, sha1, map[string]string{"package.json": `{"name":"web"}`, "index.js": "1\n"})
	fake.SetFiles(42, sha2, map[string]string{"package.json": `{"name":"web"}`, "index.js": "2\n"})
	fake.SetFiles(43, sha2, map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "apps/web/package.json": "{}",
		"apps/api/go.mod": "module api\n", "apps/api/main.go": "package main\n", "packages/ui/package.json": "{}"})

	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:    workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:     cluster,
		Runner:      buildfake.New(buildfake.Options{}),
		Build:       buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release:     releasesvc.Options{PollEvery: 20 * time.Millisecond},
		GitProvider: fake,
		GitHub:      config.GitHub{ConsoleURL: console},
		Source:      sourcesvc.Options{StateKey: []byte("0123456789abcdef0123456789abcdef")},
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
		permission.DeploymentsCreate, permission.IntegrationsManage)
	reader := httpxtest.Token(org, permission.ProjectsRead)

	// -- availability and the install link
	res := checked(t, app, http.MethodGet, "/api/v1/github", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if info := httpxtest.JSON[source.Info](t, res); !info.Enabled || info.Slug == "" {
		t.Fatalf("info = %s", res.Body)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/github", "", nil), http.StatusUnauthorized)
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/github/install", reader, nil), http.StatusForbidden)

	res = checked(t, app, http.MethodGet, "/api/v1/github/install?return_to="+url.QueryEscape("/proyectos"), token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	link := httpxtest.JSON[struct {
		URL string `json:"url"`
	}](t, res).URL
	linkURL, err := url.Parse(link)
	if err != nil || linkURL.Query().Get("state") == "" {
		t.Fatalf("install url %q (%v)", link, err)
	}
	state := linkURL.Query().Get("state")
	res = checked(t, app, http.MethodGet, "/api/v1/github/install?return_to="+url.QueryEscape("https://evil.example"), token, nil)
	httpxtest.Want(t, res, http.StatusBadRequest)

	// -- the public callback: a bad state, then a good one (no token on either)
	res = checked(t, app, http.MethodGet, "/github/setup?installation_id=7&code=code-1&state=forged.state", "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	if loc := res.Header.Get("Location"); loc != console+"/administracion/github?github_error=GITHUB_STATE_INVALID" {
		t.Fatalf("bad state redirects to %q", loc)
	}
	res = checked(t, app, http.MethodGet, "/github/setup?installation_id=7&code=nope&state="+url.QueryEscape(state), "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	if loc := res.Header.Get("Location"); loc != console+"/administracion/github?github_error=GITHUB_NOT_YOURS" {
		t.Fatalf("bad code redirects to %q", loc)
	}
	if items := httpxtest.JSON[query.Paginated[source.Installation]](t, checked(t, app, http.MethodGet, "/api/v1/github/installations", token, nil)).Items; len(items) != 0 {
		t.Fatalf("a refused callback linked %+v", items)
	}
	res = checked(t, app, http.MethodGet, "/github/setup?installation_id=7&code=code-1&state="+url.QueryEscape(state), "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || loc.Scheme+"://"+loc.Host+loc.Path != console+"/administracion/github" || loc.Query().Get("github_claim") == "" {
		t.Fatalf("good state redirects to %q (%v)", res.Header.Get("Location"), err)
	}
	claim := loc.Query().Get("github_claim")
	if items := httpxtest.JSON[query.Paginated[source.Installation]](t, checked(t, app, http.MethodGet, "/api/v1/github/installations", token, nil)).Items; len(items) != 0 {
		t.Fatalf("the public callback linked %+v", items)
	}

	// -- the claim links it, only for the user who asked, with the permission
	claimBody := map[string]string{"claim": claim}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/github/installations", "", claimBody), http.StatusUnauthorized)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/github/installations", reader, claimBody), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/github/installations", token, map[string]string{"claim": ""}), http.StatusBadRequest)
	stranger := httpxtest.TokenAs("0b0c9a9e-4d2e-4a77-9a6c-5f0f6c1d2e33", org, permission.IntegrationsManage)
	res = checked(t, app, http.MethodPost, "/api/v1/github/installations", stranger, claimBody)
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if code := httpxtest.ErrorCode(res); code != source.CodeStateInvalid {
		t.Fatalf("another user's claim = %q", code)
	}
	elsewhere := httpxtest.Token(identity.NewOrganizationID(), permission.IntegrationsManage)
	res = checked(t, app, http.MethodPost, "/api/v1/github/installations", elsewhere, claimBody)
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if items := httpxtest.JSON[query.Paginated[source.Installation]](t, checked(t, app, http.MethodGet, "/api/v1/github/installations", token, nil)).Items; len(items) != 0 {
		t.Fatalf("a refused claim linked %+v", items)
	}
	res = checked(t, app, http.MethodPost, "/api/v1/github/installations", token, claimBody)
	httpxtest.Want(t, res, http.StatusOK)
	claimed := httpxtest.JSON[struct {
		Installation source.Installation `json:"installation"`
		ReturnTo     string              `json:"return_to"`
	}](t, res)
	if claimed.Installation.ExternalID != 7 || claimed.Installation.Organization != org || claimed.ReturnTo != "/proyectos" {
		t.Fatalf("claim = %s", res.Body)
	}
	// an owner approval pending on GitHub lands on the console too
	res = checked(t, app, http.MethodGet, "/github/setup?setup_action=request", "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	if loc := res.Header.Get("Location"); loc != console+"/administracion/github?github=requested" {
		t.Fatalf("request redirects to %q", loc)
	}

	// -- installations and repositories
	res = checked(t, app, http.MethodGet, "/api/v1/github/installations", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	insts := httpxtest.JSON[struct {
		Items []source.Installation `json:"items"`
	}](t, res).Items
	if len(insts) != 1 || insts[0].ExternalID != 7 || insts[0].Account != "acme" || insts[0].Organization != org {
		t.Fatalf("installations = %s", res.Body)
	}
	inst := insts[0]
	// another organization sees none
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead)
	res = checked(t, app, http.MethodGet, "/api/v1/github/installations", other, nil)
	if got := httpxtest.JSON[struct {
		Items []source.Installation `json:"items"`
	}](t, res).Items; len(got) != 0 {
		t.Fatalf("another organization sees %s", res.Body)
	}

	res = checked(t, app, http.MethodGet, "/api/v1/github/repositories", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	repos := httpxtest.JSON[query.Paginated[source.Repo]](t, res)
	if len(repos.Items) != 2 || repos.Items[0].FullName != "acme/api" || repos.Items[1].FullName != "acme/web" ||
		repos.Items[1].Installation != inst.ID || repos.Page.Total != 2 {
		t.Fatalf("repositories = %s", res.Body)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/github/repositories?search=WEB&limit=1", token, nil)
	if got := httpxtest.JSON[query.Paginated[source.Repo]](t, res); len(got.Items) != 1 || got.Items[0].ID != 42 {
		t.Fatalf("search = %s", res.Body)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/github/repositories", reader, nil), http.StatusForbidden)
	res = checked(t, app, http.MethodGet, "/api/v1/github/installations/"+inst.ID.String()+"/repositories/42/branches", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if bs := httpxtest.JSON[struct {
		Items []source.Branch `json:"items"`
	}](t, res).Items; len(bs) != 2 || bs[0].Name != "dev" || bs[1].Name != "main" {
		t.Fatalf("branches = %s", res.Body)
	}

	// -- detection in the repository (MS-5): a proposal, nothing written
	servicesPath := "/api/v1/github/installations/" + inst.ID.String() + "/repositories/43/services"
	res = checked(t, app, http.MethodGet, servicesPath, token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	detected := httpxtest.JSON[project.Detected](t, res)
	if len(detected.Items) != 2 || detected.Items[0].Name != "web" || detected.Items[0].Root != "apps/web" ||
		detected.Items[1].Route != "/api" || detected.Truncated || len(detected.Notes) != 0 {
		t.Fatalf("detected = %s", res.Body)
	}
	res = checked(t, app, http.MethodGet, servicesPath+"?branch=main&root=apps", token, nil)
	if d := httpxtest.JSON[project.Detected](t, res); len(d.Items) != 2 || d.Items[1].Root != "api" {
		t.Fatalf("detected under root = %s", res.Body)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, servicesPath, reader, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodGet, servicesPath+"?branch=a%20b", token, nil), http.StatusBadRequest)
	httpxtest.Want(t, checked(t, app, http.MethodGet, servicesPath+"?root=../x", token, nil), http.StatusBadRequest)
	res = checked(t, app, http.MethodGet, servicesPath+"?branch=nope", token, nil)
	if httpxtest.ErrorCode(res) != source.CodeBranchMissing {
		t.Fatalf("missing branch = %s", res.Body)
	}
	fake.TreeTruncated(43, true)
	res = checked(t, app, http.MethodGet, servicesPath, token, nil)
	if d := httpxtest.JSON[project.Detected](t, res); !d.Truncated || d.Notes[len(d.Notes)-1] != project.TruncatedNote {
		t.Fatalf("truncated = %s", res.Body)
	}
	fake.TreeTruncated(43, false)

	// -- connect a project: the import runs and deploys the commit
	res = checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Web"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	projPath := "/api/v1/projects/" + proj.ID.String()
	res = checked(t, app, http.MethodGet, projPath+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	envPath := projPath + "/environments/" + env.ID.String()

	httpxtest.Want(t, checked(t, app, http.MethodGet, projPath+"/repository", token, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil), http.StatusOK)
	body := map[string]any{"installation_id": inst.ID, "repository_id": 42, "port": 3000, "health_path": "/"}
	httpxtest.Want(t, checked(t, app, http.MethodPut, projPath+"/repository", reader, body), http.StatusForbidden)
	res = checked(t, app, http.MethodPut, projPath+"/repository", token, map[string]any{"installation_id": inst.ID, "repository_id": 42,
		"branch": "nope", "port": 3000, "health_path": "/"})
	httpxtest.Want(t, res, http.StatusNotFound)
	if c := httpxtest.ErrorCode(res); c != source.CodeBranchMissing {
		t.Fatalf("missing branch: %s", c)
	}
	res = checked(t, app, http.MethodPut, projPath+"/repository", token, body)
	httpxtest.Want(t, res, http.StatusOK)
	conn := httpxtest.JSON[source.Connection](t, res)
	if conn.Repository != "acme/web" || conn.Branch != "main" || conn.LastCommit != sha1 || conn.Project != proj.ID || conn.Installation != inst.ID {
		t.Fatalf("connection = %s", res.Body)
	}
	got := httpxtest.JSON[source.Connection](t, checked(t, app, http.MethodGet, projPath+"/repository", token, nil))
	if got.ID != conn.ID {
		t.Fatalf("GET repository = %+v", got)
	}

	pushes := func(want string) []source.Push {
		t.Helper()
		var items []source.Push
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			items = httpxtest.JSON[query.Paginated[source.Push]](t, res).Items
			if len(items) > 0 && items[0].Status.Finished() && string(items[0].Status) == want {
				break
			}
		}
		return items
	}
	items := pushes("deployed")
	if len(items) != 1 || items[0].Status != source.StatusDeployed || items[0].Deployment == nil || items[0].Git.Commit != sha1 || items[0].Error != nil {
		t.Fatalf("pushes after connect = %+v", items)
	}
	first := items[0]

	// the deployment came from the commit
	res = checked(t, app, http.MethodGet, envPath+"/deployments/"+first.Deployment.String(), token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	dep := httpxtest.JSON[release.Deployment](t, res)
	if dep.Source == nil || dep.Source.Git == nil || dep.Source.Git.Commit != sha1 || dep.Source.Git.Repository != "acme/web" ||
		dep.Source.Git.Branch != "main" || dep.Source.Git.Provider != build.ProviderGitHub || dep.Source.Git.Title != "First" {
		t.Fatalf("deployment = %s", res.Body)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d := httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, envPath+"/deployments/"+first.Deployment.String(), token, nil)); d.Status.Finished() {
			if d.Status != release.StatusReady {
				t.Fatalf("deployment %s: %+v", d.Status, d.Error)
			}
			break
		}
	}

	// -- publish again (the same commit still queues) and a moved branch
	httpxtest.Want(t, checked(t, app, http.MethodPost, projPath+"/repository/publish", reader, nil), http.StatusForbidden)
	res = checked(t, app, http.MethodPost, projPath+"/repository/publish", token, map[string]any{"rebuild": true})
	httpxtest.Want(t, res, http.StatusAccepted)
	again := httpxtest.JSON[source.Push](t, res)
	if again.ID == first.ID || again.Status != source.StatusQueued || again.Git.Commit != sha1 || !again.Rebuild {
		t.Fatalf("publish = %s", res.Body)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		res = checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)
		if items = httpxtest.JSON[query.Paginated[source.Push]](t, res).Items; len(items) == 2 && items[0].ID == again.ID && items[0].Status.Finished() {
			break
		}
	}
	if len(items) != 2 || items[0].Status != source.StatusDeployed || items[0].Deployment == nil || *items[0].Deployment == *first.Deployment {
		t.Fatalf("pushes after publish = %+v", items)
	}
	if d := httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, envPath+"/deployments/"+items[0].Deployment.String(), token, nil)); !d.Rebuild || !items[0].Rebuild {
		t.Fatalf("a rebuild publish deployed %+v", d)
	}
	res = checked(t, app, http.MethodGet, projPath+"/repository/pushes?status=deployed&limit=1", token, nil)
	if got := httpxtest.JSON[query.Paginated[source.Push]](t, res); len(got.Items) != 1 || got.Page.Total != 2 {
		t.Fatalf("filtered pushes = %s", res.Body)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, projPath+"/repository/pushes?status=bogus", token, nil), http.StatusBadRequest)

	fake.Push(42, "main", source.Commit{SHA: sha2, Title: "Second", Author: "ana"})
	res = checked(t, app, http.MethodPost, projPath+"/repository/publish", token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	moved := httpxtest.JSON[source.Push](t, res)
	if moved.Git.Commit != sha2 || moved.Git.Title != "Second" {
		t.Fatalf("publish after a push = %s", res.Body)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		res = checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)
		if items = httpxtest.JSON[query.Paginated[source.Push]](t, res).Items; len(items) == 3 && items[0].Status.Finished() {
			break
		}
	}
	if len(items) != 3 || items[0].ID != moved.ID || items[0].Status != source.StatusDeployed {
		t.Fatalf("pushes after the move = %+v", items)
	}

	// -- disconnect, then the project has no repository; history stays
	httpxtest.Want(t, checked(t, app, http.MethodDelete, projPath+"/repository", reader, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, projPath+"/repository", token, nil), http.StatusNoContent)
	httpxtest.Want(t, checked(t, app, http.MethodGet, projPath+"/repository", token, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, projPath+"/repository", token, nil), http.StatusNotFound)
	httpxtest.Want(t, checked(t, app, http.MethodPost, projPath+"/repository/publish", token, nil), http.StatusNotFound)
	res = checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)
	if n := len(httpxtest.JSON[query.Paginated[source.Push]](t, res).Items); n != 3 {
		t.Fatalf("history after disconnect: %s", res.Body)
	}

	// -- unlink the installation
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/github/installations/"+inst.ID.String(), reader, nil), http.StatusForbidden)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/github/installations/"+inst.ID.String(), token, nil), http.StatusNoContent)
	httpxtest.Want(t, checked(t, app, http.MethodDelete, "/api/v1/github/installations/"+inst.ID.String(), token, nil), http.StatusNotFound)
	res = checked(t, app, http.MethodGet, "/api/v1/github/installations", token, nil)
	if n := len(httpxtest.JSON[struct {
		Items []source.Installation `json:"items"`
	}](t, res).Items); n != 0 {
		t.Fatalf("installations after unlink: %s", res.Body)
	}
}

// TestGitHubHTTPDisabled: without a GitHub App the API says so.
func TestGitHubHTTPDisabled(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Cluster: releasefake.New(releasefake.Options{}), Runner: buildfake.New(buildfake.Options{}),
		GitProvider: sourcefake.Disabled(), GitHub: config.GitHub{ConsoleURL: "http://console.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	token := httpxtest.Token(identity.NewOrganizationID(), permission.ProjectsRead, permission.ProjectsWrite, permission.IntegrationsManage)

	res := checked(t, app, http.MethodGet, "/api/v1/github", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if info := httpxtest.JSON[source.Info](t, res); info.Enabled {
		t.Fatalf("info = %s", res.Body)
	}
	res = checked(t, app, http.MethodGet, "/api/v1/github/install", token, nil)
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if c := httpxtest.ErrorCode(res); c != source.CodeNotConfigured {
		t.Fatalf("install: %s", c)
	}
	res = checked(t, app, http.MethodGet, "/github/setup?installation_id=7&code=c&state=x.y", "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "github_error=GITHUB_NOT_CONFIGURED") {
		t.Fatalf("setup redirects to %q", loc)
	}
	httpxtest.Want(t, checked(t, app, http.MethodGet, "/api/v1/github/repositories", token, nil), http.StatusUnprocessableEntity)
}
