package bootstrap_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event"
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

// TestGitHubPushDeploys: a push to the connected branch reaches the app
// without anyone pressing "Publicar": the signed webhook queues the commit,
// the import deploys it, and the commit ends with a success status. Also: a
// repeated delivery, another branch, a bad signature, the poll catching a
// push whose webhook never came, and a repository removed from the
// installation.
func TestGitHubPushDeploys(t *testing.T) {
	const (
		sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		sha3 = "cccccccccccccccccccccccccccccccccccccccc"
		sha4 = "dddddddddddddddddddddddddddddddddddddddd"
	)
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	fake := sourcefake.New(sourcefake.Options{})
	fake.AddInstallation(source.InstallationInfo{ID: 7, Account: "acme", AccountType: "Organization", AllRepositories: true}, "code-1")
	fake.AddRepo(7, source.Repo{ID: 42, FullName: "acme/web", DefaultBranch: "main"},
		map[string]source.Commit{"main": {SHA: sha1, Title: "First", Author: "ana"}, "dev": {SHA: sha4, Title: "Dev", Author: "luis"}})
	for _, sha := range []string{sha1, sha2, sha3, sha4} {
		fake.SetFiles(42, sha, map[string]string{"package.json": `{"name":"web"}`, "index.js": sha[:4] + "\n"})
	}

	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow:    workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:     releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }}),
		Runner:      buildfake.New(buildfake.Options{}),
		Build:       buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release:     releasesvc.Options{PollEvery: 20 * time.Millisecond},
		GitProvider: fake,
		GitHub:      config.GitHub{ConsoleURL: "http://console.test"},
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
		permission.DeploymentsCreate, permission.IntegrationsManage, permission.AuditRead)

	// -- link the installation (setup + claim) and connect a project
	res := checked(t, app, http.MethodGet, "/api/v1/github/install", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	link, err := url.Parse(httpxtest.JSON[struct {
		URL string `json:"url"`
	}](t, res).URL)
	if err != nil {
		t.Fatal(err)
	}
	res = checked(t, app, http.MethodGet, "/github/setup?installation_id=7&code=code-1&state="+url.QueryEscape(link.Query().Get("state")), "", nil)
	httpxtest.Want(t, res, http.StatusFound)
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	res = checked(t, app, http.MethodPost, "/api/v1/github/installations", token, map[string]string{"claim": loc.Query().Get("github_claim")})
	httpxtest.Want(t, res, http.StatusOK)
	inst := httpxtest.JSON[struct {
		Installation source.Installation `json:"installation"`
	}](t, res).Installation

	res = checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Web"})
	httpxtest.Want(t, res, http.StatusCreated)
	proj := httpxtest.JSON[project.Project](t, res)
	projPath := "/api/v1/projects/" + proj.ID.String()
	res = checked(t, app, http.MethodPut, projPath+"/repository", token,
		map[string]any{"installation_id": inst.ID, "repository_id": 42}) // no port: the API's default
	httpxtest.Want(t, res, http.StatusOK)
	if c := httpxtest.JSON[source.Connection](t, res); c.Config.Port != release.DefaultPort || c.Config.HealthPath != "/" || c.Config.Replicas != 1 {
		t.Fatalf("connection defaults = %s", res.Body)
	}

	pushes := func(wantCount int) []source.Push {
		t.Helper()
		var items []source.Push
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			items = httpxtest.JSON[query.Paginated[source.Push]](t, res).Items
			if len(items) == wantCount && items[0].Status.Finished() {
				break
			}
		}
		if len(items) != wantCount {
			t.Fatalf("pushes = %d, want %d: %+v", len(items), wantCount, items)
		}
		return items
	}
	if first := pushes(1)[0]; first.Status != source.StatusDeployed {
		t.Fatalf("first push = %+v", first)
	}

	// deliver sends a webhook exactly as GitHub would.
	deliver := func(id, event string, body []byte, signature string) httpxtest.Response {
		t.Helper()
		res := httpxtest.DoWithHeaders(t, app, http.MethodPost, "/webhooks/github", "", body, map[string]string{
			"X-GitHub-Delivery": id, "X-GitHub-Event": event, "X-Hub-Signature-256": signature})
		validate(t, http.MethodPost, "/webhooks/github", res)
		return res
	}
	push := func(branch, sha string) []byte {
		return sourcefake.PushPayload(7, 42, "acme/web", branch, source.Commit{SHA: sha, Title: "Second", Author: "ana"})
	}

	// -- a bad signature is refused and changes nothing
	body := push("main", sha2)
	res = deliver("d-bad", "push", body, "sha256=deadbeef")
	httpxtest.Want(t, res, http.StatusUnauthorized)
	res = deliver("d-none", "push", body, "")
	httpxtest.Want(t, res, http.StatusUnauthorized)

	// -- the push deploys, and the commit ends green
	fake.Push(42, "main", source.Commit{SHA: sha2, Title: "Second", Author: "ana"})
	res = deliver("d-1", "push", body, fake.Sign(body))
	httpxtest.Want(t, res, http.StatusAccepted)
	items := pushes(2)
	webhook := items[0]
	if webhook.Git.Commit != sha2 || webhook.Trigger != source.TriggerWebhook || webhook.Status != source.StatusDeployed ||
		webhook.Deployment == nil || webhook.Git.Branch != "main" || webhook.Git.Title != "Second" {
		t.Fatalf("webhook push = %+v", webhook)
	}
	res = checked(t, app, http.MethodGet, projPath+"/environments", token, nil)
	env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
	depPath := projPath + "/environments/" + env.ID.String() + "/deployments/" + webhook.Deployment.String()
	viaOf := func(id release.DeploymentID) string {
		t.Helper()
		res := checked(t, app, http.MethodGet, projPath+"/environments/"+env.ID.String()+"/deployments/"+id.String(), token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[release.Deployment](t, res).RequestedVia
	}
	if via := viaOf(*webhook.Deployment); via != release.ViaGitHub {
		t.Fatalf("webhook deployment requested_via %q", via)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d := httpxtest.JSON[release.Deployment](t, checked(t, app, http.MethodGet, depPath, token, nil)); d.Status.Finished() {
			if d.Status != release.StatusReady || d.Source == nil || d.Source.Git == nil || d.Source.Git.Commit != sha2 {
				t.Fatalf("deployment = %+v", d)
			}
			break
		}
	}
	waitStatus := func(commit, state string) []source.CommitStatus {
		t.Helper()
		var got []source.CommitStatus
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if got = fake.Statuses(42, commit); len(got) > 0 && got[len(got)-1].State == state {
				return got
			}
		}
		t.Fatalf("statuses of %s = %+v, want the last to be %s", commit[:4], got, state)
		return nil
	}
	got := waitStatus(sha2, source.CommitSuccess)
	if got[0].State != source.CommitPending || got[len(got)-1].Description != "En línea" ||
		got[0].TargetURL != "http://console.test/apps/"+proj.ID.String() {
		t.Fatalf("statuses = %+v", got)
	}

	// -- the same delivery again, the same commit in a new delivery, another branch
	res = deliver("d-1", "push", body, fake.Sign(body))
	httpxtest.Want(t, res, http.StatusAccepted)
	if got := httpxtest.JSON[map[string]string](t, res); got["status"] != "accepted" {
		t.Fatalf("body = %s", res.Body)
	}
	res = deliver("d-2", "push", body, fake.Sign(body))
	httpxtest.Want(t, res, http.StatusAccepted)
	dev := push("dev", sha4)
	res = deliver("d-3", "push", dev, fake.Sign(dev))
	httpxtest.Want(t, res, http.StatusAccepted)
	ping := []byte(`{"zen":"Keep it logically awesome."}`)
	httpxtest.Want(t, deliver("d-4", "ping", ping, fake.Sign(ping)), http.StatusAccepted)
	malformed := []byte(`not json`)
	httpxtest.Want(t, deliver("d-5", "push", malformed, fake.Sign(malformed)), http.StatusBadRequest)
	time.Sleep(200 * time.Millisecond)
	pushes(2)

	// -- a push whose webhook never came: the poll finds it
	fake.Push(42, "main", source.Commit{SHA: sha3, Title: "Third", Author: "ana"})
	n, err := platform.Sources.Commands.Reconcile(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reconcile = %d, %v", n, err)
	}
	items = pushes(3)
	if items[0].Git.Commit != sha3 || items[0].Trigger != source.TriggerReconcile || items[0].Status != source.StatusDeployed {
		t.Fatalf("reconciled push = %+v", items[0])
	}
	if via := viaOf(*items[0].Deployment); via != release.ViaGitHub {
		t.Fatalf("reconciled deployment requested_via %q", via)
	}
	waitStatus(sha3, source.CommitSuccess)
	if n, err := platform.Sources.Commands.Reconcile(ctx); err != nil || n != 0 {
		t.Fatalf("second reconcile = %d, %v", n, err)
	}

	// -- "Publicar ahora": the deployment says who asked, not that the code is on GitHub
	user := identity.MustParseUserID(httpxtest.User)
	publishAs := func(client string, want int) string {
		t.Helper()
		ctx := event.WithMeta(ctx, event.Meta{CorrelationID: "req-" + client, Client: client})
		if _, err := platform.Sources.Commands.Publish(ctx, org, proj.ID, source.Publish{RequestedBy: user}); err != nil {
			t.Fatal(err)
		}
		items := pushes(want)
		if items[0].Trigger != source.TriggerManual || items[0].Status != source.StatusDeployed {
			t.Fatalf("manual push = %+v", items[0])
		}
		return viaOf(*items[0].Deployment)
	}
	if via := publishAs("mcp:https://chatgpt.com/oauth/codex/client.json", 4); via != "assistant:codex" {
		t.Fatalf("publish_now from Codex requested_via %q", via)
	}
	if via := publishAs("mcp:some-other-agent", 5); via != "assistant:other" {
		t.Fatalf("publish_now from an unknown assistant requested_via %q", via)
	}
	if via := publishAs("web-console-client", 6); via != release.ViaConsole {
		t.Fatalf("console publish requested_via %q", via)
	}
	if via := viaOf(*pushes(6)[3].Deployment); via != release.ViaGitHub { // the reconciled one is still github
		t.Fatalf("earlier reconcile requested_via %q", via)
	}

	// -- the repository leaves the installation: the project is disconnected
	removed := sourcefake.InstallationPayload("removed", 7, 42)
	httpxtest.Want(t, deliver("d-6", "installation_repositories", removed, fake.Sign(removed)), http.StatusAccepted)
	httpxtest.Want(t, checked(t, app, http.MethodGet, projPath+"/repository", token, nil), http.StatusNotFound)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		res = checked(t, app, http.MethodGet, "/api/v1/audit?type="+source.EventDisconnected, token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		if len(httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Items) > 0 {
			break
		}
	}
	if items := httpxtest.JSON[query.Paginated[audit.Entry]](t, res).Items; len(items) != 1 {
		t.Fatalf("disconnected entries = %s", res.Body)
	}
	// and a later push finds nothing to deploy
	late := push("main", sha4)
	httpxtest.Want(t, deliver("d-7", "push", late, fake.Sign(late)), http.StatusAccepted)
	if n := len(httpxtest.JSON[query.Paginated[source.Push]](t, checked(t, app, http.MethodGet, projPath+"/repository/pushes", token, nil)).Items); n != 6 {
		t.Fatalf("pushes after the removal = %d", n)
	}
}

// TestGitHubWebhookDisabled: without a GitHub App the webhook says so.
func TestGitHubWebhookDisabled(t *testing.T) {
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
	res := httpxtest.DoWithHeaders(t, app, http.MethodPost, "/webhooks/github", "", []byte(`{}`), map[string]string{
		"X-GitHub-Delivery": "d", "X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=00"})
	validate(t, http.MethodPost, "/webhooks/github", res)
	httpxtest.Want(t, res, http.StatusUnprocessableEntity)
	if c := httpxtest.ErrorCode(res); c != source.CodeNotConfigured {
		t.Fatalf("code = %s", c)
	}
}
