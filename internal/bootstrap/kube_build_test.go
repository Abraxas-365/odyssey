package bootstrap_test

// End-to-end build on a real cluster (phase 1b-2c): users upload folders
// through the HTTP API, the worker runs the deploy workflow with the fleet's
// builder (buildfleet) and cluster (releasefleet), the gateway carries the
// build to a real agent (in-process, builds on) that runs BuildKit pods in
// the k3d cluster, pushes to its zot and deploys the image. Skipped unless
// ODYSSEY_TEST_KUBECONFIG and ODYSSEY_TEST_BUILD=1 are set (`make
// test-build`): it builds real images and takes minutes.
//
// The agent runs on the host, so build pods must reach its source server
// there: the test opens a NetworkPolicy to host.k3d.internal for its port
// and stops the cluster's own agent while it runs (two build controllers
// would fight over the same builds).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/Abraxas-365/odyssey/agent"
	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/app"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
)

const (
	buildTestRegistry   = "zot.odyssey-registry.svc.cluster.local:5000/odyssey"
	buildTestSourcePort = "18099"
	buildTestAgentImage = "odyssey-agent:dev"
	buildTestHostName   = "host.k3d.internal"
	buildNamespace      = buildctl.Namespace
)

func TestKubeBuild(t *testing.T) {
	kubeconfig := os.Getenv("ODYSSEY_TEST_KUBECONFIG")
	if kubeconfig == "" || os.Getenv("ODYSSEY_TEST_BUILD") != "1" || testing.Short() {
		t.Skip("set ODYSSEY_TEST_KUBECONFIG and ODYSSEY_TEST_BUILD=1 (make test-build)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	log.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	port, _ := strconv.Atoi(os.Getenv("ODYSSEY_TEST_APPS_PORT"))
	if port == 0 {
		port = 8088
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitForCRD(t, ctx, kubeconfig)

	// The cluster, made ready for the in-process agent. Cleanups run in
	// reverse: the agent stops before the cluster's own comes back.
	restoreClusterAgent := stopClusterAgent(t, ctx, cs)
	t.Cleanup(restoreClusterAgent)
	allowBuildsToHost(t, ctx, cs)
	plantRegistryCredentials(t, ctx, cs)
	// The builds target the cluster's nodes (k3d's run the host's arch).
	arch := runtime.GOARCH
	if nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil && len(nodes.Items) > 0 {
		arch = nodes.Items[0].Status.NodeInfo.Architecture
	}

	// The platform: the fleet's builder and cluster, the worker, the gateway
	// and the API, like a deployment of the control plane.
	h := startFleet(t, func(o *bootstrap.Options) {
		o.BuilderConfig = config.Builder{Kind: config.BuilderFleet}
		o.ClusterConfig = config.Cluster{Kind: config.ClusterFleet}
		o.Workflow.PollInterval = 100 * time.Millisecond
		o.Build = buildsvc.Options{PollEvery: time.Second}
		o.Release = releasesvc.Options{PollEvery: time.Second}
	})
	wctx, stopWorker := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, h.platform, config.Worker{}, outboxpg.Options{PollInterval: 100 * time.Millisecond})
	}()

	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	created, err := h.platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "k3d-build", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}

	// The agent, in-process, as the cluster's builder.
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: created.Token.Gateway, CAPin: created.Token.CAPin})
	mgr, err := agent.NewManager(cfg, agent.Options{
		App:         app.Config{Domain: "apps.odyssey.localhost", URLScheme: "http", URLPort: port, ReadyDeadline: 120},
		HealthGrace: 30 * time.Second, HealthAddr: "0", MetricsAddr: "0",
		Tunnel: tunnel.Options{Store: store, MinBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		Builds: agent.BuildOptions{
			Registry: buildTestRegistry, InsecureRegistry: true, Platform: "linux/" + arch, AgentImage: buildTestAgentImage,
			SourceAddr: "0.0.0.0:" + buildTestSourcePort, SourceURL: "http://" + buildTestHostName + ":" + buildTestSourcePort,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	go func() { agentDone <- mgr.Start(actx) }()
	k := &kubeBuild{t: t, ctx: ctx, port: port, org: org, cs: cs}
	t.Cleanup(k.cleanup(cfg))
	t.Cleanup(func() {
		stopAgent()
		if err := <-agentDone; err != nil {
			t.Errorf("agent: %v", err)
		}
	})
	t.Cleanup(func() {
		stopWorker()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	})

	waitFor(t, ctx, "the agent to connect and announce its builder", 2*time.Minute, func() bool {
		c, err := h.platform.Fleet.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected && c.Agent.Capabilities.Builder != nil
	})
	c, _ := h.platform.Fleet.Queries.Find(ctx, org, created.Cluster.ID)
	if b := c.Agent.Capabilities.Builder; b.Registry != buildTestRegistry || !slices.Contains(b.Platforms, "linux/"+arch) {
		t.Fatalf("announced builder %+v", b)
	}

	k.app = bootstrap.NewServer(h.platform, config.Config{}, httpxtest.Auth(), nil)
	k.token = httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:6]

	// (a) a Go app without a Dockerfile: Railpack detects and builds it.
	t.Run("go app without a Dockerfile", func(t *testing.T) {
		k.t = t
		marker := "marker-go-" + suffix
		p := k.project("gobuild-" + suffix)
		d := k.deploy(p, map[string]string{
			"go.mod": "module example.com/marker\n\ngo 1.22\n",
			"main.go": `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "` + marker + `") })
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		panic(err)
	}
}
`,
		})
		d = k.settle(p, d, release.StatusReady)
		if d.Release == nil || d.Build == nil {
			t.Fatalf("deployment %+v", d)
		}
		b := k.build(p, *d.Build)
		image := regexp.MustCompile("^" + regexp.QuoteMeta(buildTestRegistry+"/"+org.String()+"/"+p.proj.ID.String()) + "@sha256:[0-9a-f]{64}$")
		if b.Status != build.StatusSucceeded || !image.MatchString(b.Image) || d.Image != b.Image || b.Error != nil {
			t.Fatalf("build %+v; deployment image %q", b, d.Image)
		}
		cur := k.current(p)
		if cur.Release == nil || cur.Release.ID != *d.Release || cur.Release.Image != b.Image {
			t.Fatalf("current %+v", cur)
		}
		if want := "http://" + p.proj.Slug + ".apps.odyssey.localhost:" + strconv.Itoa(port); cur.URL != want {
			t.Fatalf("url %q, want %q", cur.URL, want)
		}
		expectServing(t, ctx, cur.URL, marker)

		lines, finished := k.logs(p, b.ID)
		if len(lines) == 0 || !finished {
			t.Fatalf("build log: %d lines, finished=%v", len(lines), finished)
		}
		t.Logf("build log: %d lines", len(lines))

		// The build did not wait for the scan: Trivy runs after it, as a Job,
		// and the control plane records what it found (or why it could not).
		var scanned build.Build
		waitFor(t, ctx, "the image's scan to be recorded", 8*time.Minute, func() bool {
			scanned = k.build(p, b.ID)
			return scanned.Scan != nil
		})
		s := scanned.Scan
		if s.Known() && (s.Digest != build.DigestOf(b.Image) || s.Tool == "" || s.Critical < 0 || s.High < 0) {
			t.Fatalf("scan %+v for image %s", s, b.Image)
		}
		t.Logf("scan: %+v", *s) // known=false is acceptable offline: the reason is in Error
	})

	// (b) an app with its own Dockerfile: built with it, not with Railpack.
	t.Run("app with its own Dockerfile", func(t *testing.T) {
		k.t = t
		marker := "marker-docker-" + suffix
		p := k.project("dockerbuild-" + suffix)
		d := k.deploy(p, map[string]string{"Dockerfile": busyboxDockerfile(marker, "")})
		d = k.settle(p, d, release.StatusReady)
		b := k.build(p, *d.Build)
		if b.Status != build.StatusSucceeded || !strings.Contains(b.Image, "@sha256:") {
			t.Fatalf("build %+v", b)
		}
		lines, _ := k.logs(p, b.ID)
		if !anyLine(lines, "Dockerfile") {
			t.Errorf("the log never mentions the Dockerfile:\n%s", strings.Join(lines, "\n"))
		}
		expectServing(t, ctx, k.current(p).URL, marker)
	})

	// (c) a folder that cannot be started: the build, and so the
	// deployment, fail with UNSUPPORTED_APP and nothing is released.
	t.Run("folder with no way to start", func(t *testing.T) {
		k.t = t
		p := k.project("nostart-" + suffix)
		d := k.deploy(p, map[string]string{"README.md": "# nothing to run here\n"})
		d = k.settle(p, d, release.StatusFailed)
		if d.Error == nil || d.Error.Code != build.CodeUnsupported || d.Step != release.StepBuild || d.Release != nil {
			t.Fatalf("failed deployment %+v (error %+v)", d, d.Error)
		}
		b := k.build(p, *d.Build)
		if b.Status != build.StatusFailed || b.Error == nil || b.Error.Code != build.CodeUnsupported || b.Image != "" {
			t.Fatalf("build %+v", b)
		}
		if cur := k.current(p); cur.Release != nil || cur.Deployment != nil {
			t.Fatalf("something was released: %+v", cur)
		}
	})

	// (d) isolation: a Dockerfile's RUN steps are the app's code. They
	// reach the internet but not the Kubernetes API, the metadata endpoint
	// nor the registry credentials (the test plants them in the build
	// namespace, so the build container really has them mounted, and the
	// probe looks for them through /proc). The markers are spelled so that
	// the echoed command never contains them: only the output does.
	t.Run("RUN steps are isolated", func(t *testing.T) {
		k.t = t
		marker := "marker-isolated-" + suffix
		probe := `RUN (nc -w 3 -z 10.43.0.1 443 && echo "REACHED-"API || echo "BLOCKED-"API); \
    (nc -w 3 -z 169.254.169.254 80 && echo "REACHED-"META || echo "BLOCKED-"META); \
    (nc -w 3 -z 1.1.1.1 443 && echo "INTERNET-"OK || echo "INTERNET-"DOWN); \
    ls -la /proc/*/root/var/run/odyssey/registry/ 2>&1 | head -5 | sed 's/^/CRED-LS: /'; \
    n=$(ls /proc/*/root/var/run/odyssey/registry/ 2>/dev/null | grep -c config.json || true); \
    if [ "$n" = 0 ]; then echo "NO-"CRED; else echo "FOUND-"CRED; fi
`
		p := k.project("isolated-" + suffix)
		d := k.deploy(p, map[string]string{"Dockerfile": busyboxDockerfile(marker, probe)})
		d = k.settle(p, d, release.StatusReady)
		b := k.build(p, *d.Build)
		lines, finished := k.logs(p, b.ID)
		if !finished {
			t.Error("the build log never finished")
		}
		for _, want := range []string{"BLOCKED-API", "BLOCKED-META", "NO-CRED", "INTERNET-OK"} {
			if !anyLine(lines, want) {
				t.Errorf("the build log has no %q", want)
			}
		}
		for _, bad := range []string{"REACHED-API", "REACHED-META", "FOUND-CRED"} {
			if anyLine(lines, bad) {
				t.Errorf("isolation broken: the build log has %q", bad)
			}
		}
		for _, l := range lines {
			if strings.Contains(l, "CRED-LS: ") && !strings.Contains(l, "sed") { // why the credentials are unreachable
				t.Log(l)
			}
		}
		if t.Failed() {
			t.Logf("build log:\n%s", strings.Join(lines, "\n"))
		}
		expectServing(t, ctx, k.current(p).URL, marker)
	})
}

// busyboxDockerfile serves marker with busybox's httpd on 8080; extra
// instructions go before the final ones.
func busyboxDockerfile(marker, extra string) string {
	return "FROM mirror.gcr.io/library/busybox\n" + extra +
		"RUN mkdir -p /www && echo '" + marker + "' > /www/index.html\n" +
		"EXPOSE 8080\n" +
		"CMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n"
}

func anyLine(lines []string, want string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) })
}

// kubeBuild drives the HTTP API like a user does.
type kubeBuild struct {
	t     *testing.T
	ctx   context.Context
	app   http.Handler
	token string
	port  int
	org   identity.OrganizationID
	cs    kubernetes.Interface

	namespaces []string
	builds     []string
}

type buildProject struct {
	proj project.Project
	env  project.Environment
	base string // the environment's path
}

// cleanup deletes what the apps and builds left in the cluster.
func (k *kubeBuild) cleanup(cfg *rest.Config) func() {
	return func() {
		ctx := context.Background()
		for _, ns := range k.namespaces {
			_ = k.cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
		}
		scheme, err := agent.Scheme()
		if err != nil {
			return
		}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			return
		}
		for _, id := range k.builds {
			ob := &v1alpha1.OdysseyBuild{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: buildctl.Namespace}}
			_ = client.IgnoreNotFound(c.Delete(ctx, ob, client.PropagationPolicy(metav1.DeletePropagationBackground)))
		}
	}
}

func (k *kubeBuild) project(name string) buildProject {
	k.t.Helper()
	res := checked(k.t, k.app, http.MethodPost, "/api/v1/projects", k.token, map[string]any{"name": name})
	httpxtest.Want(k.t, res, http.StatusCreated)
	p := httpxtest.JSON[project.Project](k.t, res)
	res = checked(k.t, k.app, http.MethodGet, "/api/v1/projects/"+p.ID.String()+"/environments", k.token, nil)
	envs := httpxtest.JSON[query.Paginated[project.Environment]](k.t, res).Items
	if len(envs) == 0 {
		k.t.Fatalf("project %s has no environments", p.ID)
	}
	env := envs[0]
	for _, e := range envs {
		if e.Kind == project.KindProduction {
			env = e
		}
	}
	k.namespaces = append(k.namespaces, desired.Namespace(env.ID.String()))
	return buildProject{proj: p, env: env, base: "/api/v1/projects/" + p.ID.String() + "/environments/" + env.ID.String()}
}

// deploy uploads files as the project's folder, exactly like the console.
func (k *kubeBuild) deploy(p buildProject, files map[string]string) release.Deployment {
	k.t.Helper()
	res := upload(k.t, k.app, p.base+"/deployments", k.token,
		configPart(k.t, map[string]any{"port": 8080, "health_path": "/"}),
		part{name: "source", file: "app.tar.gz", body: sourceArchive(k.t, files)})
	httpxtest.Want(k.t, res, http.StatusAccepted)
	d := httpxtest.JSON[release.Deployment](k.t, res)
	if d.Build == nil {
		k.t.Fatalf("accepted without a build: %+v", d)
	}
	k.builds = append(k.builds, d.Build.String())
	return d
}

// settle waits for the deployment to finish and checks how.
func (k *kubeBuild) settle(p buildProject, d release.Deployment, want release.Status) release.Deployment {
	k.t.Helper()
	start, last := time.Now(), ""
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); {
		res := checked(k.t, k.app, http.MethodGet, p.base+"/deployments/"+d.ID.String(), k.token, nil)
		httpxtest.Want(k.t, res, http.StatusOK)
		d = httpxtest.JSON[release.Deployment](k.t, res)
		if now := string(d.Status) + "/" + d.Step; now != last {
			last = now
			k.t.Logf("%s: deployment %s (+%s)", p.proj.Slug, now, time.Since(start).Round(time.Second))
		}
		if d.Status.Finished() {
			if d.Status != want {
				if d.Build != nil {
					lines, _ := k.logs(p, *d.Build)
					k.t.Logf("build log:\n%s", strings.Join(lines, "\n"))
				}
				k.t.Fatalf("deployment %s is %s at %s, want %s: %+v", d.ID, d.Status, d.Step, want, d.Error)
			}
			return d
		}
		select {
		case <-k.ctx.Done():
			k.t.Fatalf("deployment %s never finished (%s at %s): %v", d.ID, d.Status, d.Step, k.ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	k.t.Fatalf("deployment %s never finished (%s at %s)", d.ID, d.Status, d.Step)
	return d
}

func (k *kubeBuild) build(p buildProject, id build.ID) build.Build {
	k.t.Helper()
	res := checked(k.t, k.app, http.MethodGet, "/api/v1/projects/"+p.proj.ID.String()+"/builds/"+id.String(), k.token, nil)
	httpxtest.Want(k.t, res, http.StatusOK)
	return httpxtest.JSON[build.Build](k.t, res)
}

func (k *kubeBuild) current(p buildProject) release.Current {
	k.t.Helper()
	res := checked(k.t, k.app, http.MethodGet, p.base+"/current", k.token, nil)
	httpxtest.Want(k.t, res, http.StatusOK)
	return httpxtest.JSON[release.Current](k.t, res)
}

// logs reads a build's whole log through the API, page by page, until it
// reports finished (or a minute passes without more lines).
func (k *kubeBuild) logs(p buildProject, id build.ID) (lines []string, finished bool) {
	k.t.Helper()
	var after int64
	deadline := time.Now().Add(time.Minute)
	for {
		path := fmt.Sprintf("/api/v1/projects/%s/builds/%s/logs?after=%d", p.proj.ID, id, after)
		res := checked(k.t, k.app, http.MethodGet, path, k.token, nil)
		httpxtest.Want(k.t, res, http.StatusOK)
		page := httpxtest.JSON[build.LogPage](k.t, res)
		for _, l := range page.Lines {
			lines = append(lines, l.Text)
		}
		after = page.Next
		if page.Finished {
			return lines, true
		}
		if time.Now().After(deadline) {
			return lines, false
		}
		if len(page.Lines) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// sourceArchive is a tar.gz of files, like a browser-selected folder.
func sourceArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		body := files[name]
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

// waitFor polls ok every 500 ms until it holds, the timeout passes or ctx ends.
func waitFor(t *testing.T, ctx context.Context, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", what, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// stopClusterAgent scales the cluster's own agent to zero (two build
// controllers would fight over the builds) and returns what restores it.
func stopClusterAgent(t *testing.T, ctx context.Context, cs kubernetes.Interface) (restore func()) {
	t.Helper()
	const ns, name = "odyssey-system", "odyssey-agent"
	deployments := cs.AppsV1().Deployments(ns)
	scale, err := deployments.GetScale(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return func() {}
	}
	if err != nil {
		t.Fatal(err)
	}
	was := scale.Spec.Replicas
	if was == 0 {
		return func() {}
	}
	t.Logf("scaling %s/%s from %d to 0 for the test", ns, name, was)
	scale.Spec.Replicas = 0
	if _, err := deployments.UpdateScale(ctx, name, scale, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	restore = func() {
		ctx := context.Background()
		s, err := deployments.GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("restore %s: %v", name, err)
			return
		}
		s.Spec.Replicas = was
		if _, err := deployments.UpdateScale(ctx, name, s, metav1.UpdateOptions{}); err != nil {
			t.Errorf("restore %s: %v", name, err)
		}
	}
	waitFor(t, ctx, "the cluster's agent to stop", time.Minute, func() bool {
		d, err := deployments.Get(ctx, name, metav1.GetOptions{})
		return err == nil && agentStopped(d)
	})
	return restore
}

func agentStopped(d *appsv1.Deployment) bool { return d.Status.Replicas == 0 }

// allowBuildsToHost opens build pods' egress to the host's source server
// (the base policy blocks private ranges, and the k3d host is one), and
// removes the rule when the test ends.
func allowBuildsToHost(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	ip := k3dHostIP(t, ctx, cs)
	tcp, sourcePort := corev1.ProtocolTCP, intstr.FromInt32(mustPort(t, buildTestSourcePort))
	policies := cs.NetworkingV1().NetworkPolicies(buildNamespace)
	const name = "test-build-host"
	_ = policies.Delete(ctx, name, metav1.DeleteOptions{})
	_, err := policies.Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: buildNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: ip + "/32"}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &sourcePort}},
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("network policy for the host %s: %v", ip, err)
	}
	t.Cleanup(func() { _ = policies.Delete(context.Background(), name, metav1.DeleteOptions{}) })
}

// k3dHostIP is what host.k3d.internal resolves to inside the cluster (k3d
// writes it into CoreDNS' NodeHosts).
func k3dHostIP(t *testing.T, ctx context.Context, cs kubernetes.Interface) string {
	t.Helper()
	if ip := os.Getenv("ODYSSEY_TEST_HOST_IP"); ip != "" {
		return ip
	}
	cm, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read CoreDNS' config to find %s (or set ODYSSEY_TEST_HOST_IP): %v", buildTestHostName, err)
	}
	for _, line := range strings.Split(cm.Data["NodeHosts"], "\n") {
		if f := strings.Fields(line); len(f) >= 2 && slices.Contains(f[1:], buildTestHostName) {
			return f[0]
		}
	}
	t.Fatalf("CoreDNS' NodeHosts has no %s (set ODYSSEY_TEST_HOST_IP)", buildTestHostName)
	return ""
}

// plantRegistryCredentials makes sure the build namespace has the
// registry's Secret, so build pods mount a config.json at
// /var/run/odyssey/registry and the isolation probe means something. The
// credentials are for a host that does not exist: pushes to zot (which has
// no auth) are unaffected. A Secret already there is left alone.
func plantRegistryCredentials(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	secrets := cs.CoreV1().Secrets(buildNamespace)
	if _, err := secrets.Get(ctx, buildctl.RegistrySecret, metav1.GetOptions{}); err == nil {
		t.Logf("secret %s/%s already exists: left as it is", buildNamespace, buildctl.RegistrySecret)
		return
	} else if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	dockerConfig := `{"auths":{"registry.invalid":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("test:not-a-real-password")) + `"}}}`
	_, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: buildctl.RegistrySecret, Namespace: buildNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(dockerConfig)},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("plant the registry secret: %v", err)
	}
	t.Cleanup(func() {
		_ = secrets.Delete(context.Background(), buildctl.RegistrySecret, metav1.DeleteOptions{})
	})
}

func mustPort(t *testing.T, s string) int32 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return int32(n)
}
