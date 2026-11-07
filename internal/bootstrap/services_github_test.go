package bootstrap_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
)

// TestGitHubMonorepo is MS-5's exit criterion (with a fake GitHub; the real
// one is checked live): a monorepo is detected in GitHub (web + api, the
// workspace's library left out), the proposal is saved as the app's
// services, the repository is connected whole (no root) and its commit
// becomes one version built per service from its folder; a push that
// changes only the api builds only the api. A connection's root still
// composes with the services' folders (an app inside a bigger repository).
func TestGitHubMonorepo(t *testing.T) {
	const (
		sha1 = "1111111111111111111111111111111111111111"
		sha2 = "2222222222222222222222222222222222222222"
	)
	app := func(name, marker string) map[string]string {
		return map[string]string{
			name + "/package.json": `{"name":"x","scripts":{"start":"node index.js"}}`,
			name + "/index.js":     "// " + marker + "\nrequire('http').createServer((q, s) => s.end('ok')).listen(3000)\n",
		}
	}
	files := func(web, api string) map[string]string {
		out := map[string]string{"package.json": `{"workspaces":["apps/*","packages/*"]}`, "README.md": "# monorepo\n",
			"packages/ui/package.json": `{"name":"ui"}`}
		for k, v := range app("apps/web", web) {
			out[k] = v
		}
		for k, v := range app("apps/api", api) {
			out[k] = v
		}
		return out
	}
	// the same monorepo one level down in a bigger repository
	bigger := map[string]string{"go.mod": "module big\n"}
	for k, v := range files("w1", "a1") {
		bigger["platform/"+k] = v
	}
	fake := sourcefake.New(sourcefake.Options{})
	fake.AddInstallation(source.InstallationInfo{ID: 7, Account: "acme", AccountType: "Organization", AllRepositories: true}, "code-1")
	fake.AddRepo(7, source.Repo{ID: 50, FullName: "acme/monorepo", DefaultBranch: "main"},
		map[string]source.Commit{"main": {SHA: sha1, Title: "Both", Author: "ana"}})
	fake.SetFiles(50, sha1, files("w1", "a1"))
	fake.SetFiles(50, sha2, files("w1", "a2"))
	fake.AddRepo(7, source.Repo{ID: 51, FullName: "acme/big", DefaultBranch: "main"},
		map[string]source.Commit{"main": {SHA: sha1, Title: "Big", Author: "ana"}})
	fake.SetFiles(51, sha1, bigger)

	s := newServicesAppWith(t, buildfake.Options{}, fake)

	// link the installation (setup + claim)
	res := checked(t, s.app, http.MethodGet, "/api/v1/github/install", s.token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	link, err := url.Parse(httpxtest.JSON[struct {
		URL string `json:"url"`
	}](t, res).URL)
	if err != nil {
		t.Fatal(err)
	}
	res = checked(t, s.app, http.MethodGet, "/github/setup?installation_id=7&code=code-1&state="+url.QueryEscape(link.Query().Get("state")), "", nil)
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	res = checked(t, s.app, http.MethodPost, "/api/v1/github/installations", s.token, map[string]string{"claim": loc.Query().Get("github_claim")})
	httpxtest.Want(t, res, http.StatusOK)
	inst := httpxtest.JSON[struct {
		Installation source.Installation `json:"installation"`
	}](t, res).Installation

	// 1. detection in the repository: web at /, api at /api, the library out
	res = checked(t, s.app, http.MethodGet, "/api/v1/github/installations/"+inst.ID.String()+"/repositories/50/services", s.token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	detected := httpxtest.JSON[project.Detected](t, res)
	if len(detected.Items) != 2 || detected.Items[0].Name != "web" || detected.Items[0].Root != "apps/web" || detected.Items[0].Route != "/" ||
		detected.Items[1].Name != "api" || detected.Items[1].Root != "apps/api" || detected.Items[1].Route != "/api" || !detected.Items[1].StripPrefix {
		t.Fatalf("detected = %s", res.Body)
	}

	// 2. the proposal becomes the app's services, as is
	items := make([]project.ServiceSpec, len(detected.Items))
	for i, it := range detected.Items {
		items[i] = it.ServiceSpec
	}
	httpxtest.Want(t, checked(t, s.app, http.MethodPut, s.pbase+"/services", s.token, map[string]any{"items": items}), http.StatusOK)

	// 3. connect the whole repository: one version, one build per service
	res = checked(t, s.app, http.MethodPut, s.pbase+"/repository", s.token,
		map[string]any{"installation_id": inst.ID, "repository_id": 50, "port": 3000, "health_path": "/"})
	httpxtest.Want(t, res, http.StatusOK)
	if c := httpxtest.JSON[source.Connection](t, res); c.Root != "" {
		t.Fatalf("connection %s", res.Body)
	}
	deployed := func(want int) release.Deployment {
		t.Helper()
		var pushes []source.Push
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			pushes = httpxtest.JSON[query.Paginated[source.Push]](t, checked(t, s.app, http.MethodGet, s.pbase+"/repository/pushes", s.token, nil)).Items
			if len(pushes) == want && pushes[0].Status.Finished() {
				break
			}
		}
		if len(pushes) != want || pushes[0].Status != source.StatusDeployed || pushes[0].Deployment == nil {
			t.Fatalf("pushes %+v", pushes)
		}
		d := s.await(t, *pushes[0].Deployment)
		if d.Status != release.StatusReady || d.Release == nil {
			t.Fatalf("deployment %+v %+v", d, d.Error)
		}
		return d
	}
	builtOf := func(d release.Deployment) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, sb := range d.Builds {
			if b := s.build(t, sb.Build); b.ReusedFrom == nil {
				out[sb.Root] = true
			}
		}
		return out
	}
	v1 := deployed(1)
	if len(v1.Builds) != 2 || v1.Builds[0].Root != "apps/web" || v1.Builds[1].Root != "apps/api" || len(builtOf(v1)) != 2 {
		t.Fatalf("v1 builds %+v", v1.Builds)
	}
	r1 := httpxtest.JSON[release.Release](t, checked(t, s.app, http.MethodGet, s.pbase+"/releases/"+v1.Release.String(), s.token, nil))
	if len(r1.Services) != 2 || r1.Services[1].Route != "/api" || v1.Source == nil || v1.Source.Git == nil || v1.Source.Git.Commit != sha1 {
		t.Fatalf("v1 %+v / %+v", r1.Services, v1.Source)
	}

	// 4. a push that changes only the api builds only the api
	fake.Push(50, "main", source.Commit{SHA: sha2, Title: "Api", Author: "ana"})
	res = checked(t, s.app, http.MethodPost, s.pbase+"/repository/publish", s.token, nil)
	httpxtest.Want(t, res, http.StatusAccepted)
	v2 := deployed(2)
	if got := builtOf(v2); len(got) != 1 || !got["apps/api"] {
		t.Fatalf("only the api changed, built %v", got)
	}

	// 5. the same monorepo inside a bigger repository: the connection's root
	// is the app's folder and the services' folders are relative to it
	res = checked(t, s.app, http.MethodGet, "/api/v1/github/installations/"+inst.ID.String()+"/repositories/51/services?root=platform", s.token, nil)
	if d := httpxtest.JSON[project.Detected](t, res); len(d.Items) != 2 || d.Items[0].Root != "apps/web" || d.Items[0].Evidence != "platform/apps/web/package.json" {
		t.Fatalf("detected under platform = %s", res.Body)
	}
	res = checked(t, s.app, http.MethodGet, "/api/v1/github/installations/"+inst.ID.String()+"/repositories/51/services", s.token, nil)
	if d := httpxtest.JSON[project.Detected](t, res); len(d.Items) != 2 || d.Items[0].Root != "platform/apps/web" {
		t.Fatalf("detected in the bigger repository = %s", res.Body)
	}
	res = checked(t, s.app, http.MethodPut, s.pbase+"/repository", s.token,
		map[string]any{"installation_id": inst.ID, "repository_id": 51, "root": "platform", "port": 3000, "health_path": "/"})
	httpxtest.Want(t, res, http.StatusOK)
	v3 := deployed(3)
	if len(v3.Builds) != 2 || v3.Builds[0].Root != "apps/web" {
		t.Fatalf("v3 builds %+v", v3.Builds)
	}
}
