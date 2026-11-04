package bootstrap_test

// End-to-end on a real cluster: the gateway and a real agent (its app
// controller plus tunnel, in-process) on a throwaway k3s; release deploys
// through releasefleet and the app answers through the cluster's ingress.
// `make test-kube` starts k3s with testcontainers (ODYSSEY_TEST_KUBE=1);
// ODYSSEY_TEST_KUBECONFIG points it at an existing cluster instead (k3d:
// stop any other agent first, two controllers would reconcile the same
// apps). Skipped otherwise: it takes about a minute.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent"
	"github.com/Abraxas-365/odyssey/agent/controllers/app"
	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefleet"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// whoami answers every path on WHOAMI_PORT_NUMBER, and /health with 200.
const whoami = "docker.io/traefik/whoami@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

func TestKube(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	log.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	kubeconfig, port := clusterFor(t, ctx)
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	// The platform and its gateway; the cluster is registered like the
	// console does, and the agent enrols with the token it got.
	h := startFleet(t)
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	created, err := h.platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "k3s", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: created.Token.Gateway, CAPin: created.Token.CAPin})
	mgr, err := agent.NewManager(cfg, agent.Options{
		App:         app.Config{Domain: "apps.odyssey.localhost", URLScheme: "http", URLPort: port, ReadyDeadline: 90},
		HealthGrace: 20 * time.Second, HealthAddr: "0", MetricsAddr: "0",
		Tunnel: tunnel.Options{Store: store, MinBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	actx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	go func() { agentDone <- mgr.Start(actx) }()
	t.Cleanup(func() {
		stopAgent()
		if err := <-agentDone; err != nil {
			t.Errorf("agent: %v", err)
		}
	})
	eventually(t, "the agent to connect", func() bool {
		c, err := h.platform.Fleet.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected
	})
	c, _ := h.platform.Fleet.Queries.Find(ctx, org, created.Cluster.ID)
	if !strings.HasPrefix(c.Agent.Capabilities.KubernetesVersion, "v1.") || len(c.Agent.Capabilities.IngressClasses) == 0 {
		t.Errorf("capabilities %+v", c.Agent.Capabilities)
	}

	k := releasefleet.New(h.platform.Fleet.Dispatch)
	target := release.Target{Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}
	name := "e2e-" + target.Environment.String()[:8]
	t.Cleanup(func() {
		cs, err := kubernetes.NewForConfig(cfg)
		if err == nil {
			_ = cs.CoreV1().Namespaces().Delete(context.Background(), desired.Namespace(target.Environment.String()), metav1.DeleteOptions{})
		}
	})

	apply := func(rev int64, image string, appPort int) {
		t.Helper()
		err := k.Apply(ctx, target, release.DesiredApp{
			Name: name, Revision: rev, Image: image, Port: appPort, HealthPath: "/health", Replicas: 1,
			Env:    map[string]string{"WHOAMI_PORT_NUMBER": "8080", "WHOAMI_NAME": "rev" + strconv.FormatInt(rev, 10)},
			Labels: map[string]string{"odyssey.dev/deployment": "d" + strconv.FormatInt(rev, 10)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// wait polls Status like the deploy workflow: until revision rev is
	// ready or failed (as reported by the agent, through the tunnel).
	wait := func(rev int64) release.AppStatus {
		t.Helper()
		var s release.AppStatus
		for {
			var err error
			if s, err = k.Status(ctx, target, name); err != nil {
				t.Logf("status (retrying): %v", err)
			} else if s.ObservedRevision == rev && s.Phase != release.PhaseProgressing {
				t.Logf("revision %d: %s %s %s", rev, s.Phase, s.Reason, s.Message)
				return s
			}
			select {
			case <-ctx.Done():
				t.Fatalf("revision %d never settled; last status %+v", rev, s)
			case <-time.After(time.Second):
			}
		}
	}

	apply(1, whoami, 8080)
	s := wait(1)
	if s.Phase != release.PhaseReady || s.ReadyReplicas != 1 || s.URL != "http://"+name+".apps.odyssey.localhost:"+strconv.Itoa(port) {
		t.Fatalf("revision 1: %+v", s)
	}
	expectServing(t, ctx, s.URL, "Name: rev1")

	// Listening on another port than declared: the health check never
	// passes. The version that serves keeps serving.
	apply(2, whoami, 9090)
	if s := wait(2); s.Phase != release.PhaseFailed || s.Reason != release.FailureUnhealthy {
		t.Fatalf("revision 2: %+v", s)
	}
	expectServing(t, ctx, s.URL, "Name: rev1")

	// An image that does not exist fails fast.
	apply(3, "docker.io/traefik/whoami@sha256:"+strings.Repeat("0", 64), 8080)
	if s := wait(3); s.Phase != release.PhaseFailed || s.Reason != release.FailureImagePull {
		t.Fatalf("revision 3: %+v", s)
	}
	expectServing(t, ctx, s.URL, "Name: rev1")

	// What the auto-rollback does: the serving release again, as a new revision.
	apply(4, whoami, 8080)
	if s := wait(4); s.Phase != release.PhaseReady {
		t.Fatalf("revision 4: %+v", s)
	}
	expectServing(t, ctx, s.URL, "Name: rev4")

	// A superseded deployment arriving late changes nothing.
	apply(2, whoami, 9090)
	time.Sleep(2 * time.Second)
	if s, _ := k.Status(ctx, target, name); s.ObservedRevision != 4 || s.Phase != release.PhaseReady {
		t.Fatalf("late revision 2: %+v", s)
	}
}

// clusterFor returns the kubeconfig path and the host port of the ingress
// of the cluster under test, with the agent's CRD installed.
func clusterFor(t *testing.T, ctx context.Context) (string, int) {
	t.Helper()
	if kubeconfig := os.Getenv("ODYSSEY_TEST_KUBECONFIG"); kubeconfig != "" {
		port, _ := strconv.Atoi(os.Getenv("ODYSSEY_TEST_APPS_PORT"))
		if port == 0 {
			port = 8088
		}
		return kubeconfig, port
	}
	if os.Getenv("ODYSSEY_TEST_KUBE") == "" || testing.Short() {
		t.Skip("set ODYSSEY_TEST_KUBE=1 (make test-kube) or ODYSSEY_TEST_KUBECONFIG")
	}
	image := os.Getenv("ODYSSEY_TEST_K3S_IMAGE")
	if image == "" {
		image = "rancher/k3s:v1.35.5-k3s1"
	}
	// k3s names a manifest's Addon after its file name up to the first dot,
	// so the agent's CRDs (odyssey.dev_*.yaml all become "odyssey") go in
	// one file: two would race for the same Addon and one would be lost.
	var crds []byte
	for _, name := range []string{"odyssey.dev_odysseyapps.yaml", "odyssey.dev_odysseyresources.yaml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "agent", "install", "crd", name))
		if err != nil {
			t.Fatal(err)
		}
		crds = append(append(crds, "---\n"...), b...)
	}
	crd := filepath.Join(t.TempDir(), "odyssey-crds.yaml")
	if err := os.WriteFile(crd, crds, 0o600); err != nil {
		t.Fatal(err)
	}
	// The module disables k3s' Traefik; keep it: the test goes through the
	// ingress like a user would. k3s applies the CRDs at startup.
	k, err := k3s.Run(ctx, image,
		k3s.WithManifest(crd),
		testcontainers.WithCmd("server", "--tls-san=localhost", "--tls-san=127.0.0.1"),
		testcontainers.WithExposedPorts("80/tcp"),
	)
	testcontainers.CleanupContainer(t, k)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := k.GetKubeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	mapped, err := k.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	waitForCRD(t, ctx, path)
	waitForIngress(t, ctx, int(mapped.Num()))
	return path, int(mapped.Num())
}

// waitForCRD waits until the cluster serves OdysseyApps and
// OdysseyResources (k3s applies each manifest on its own: the group can be
// served before both kinds are).
func waitForCRD(t *testing.T, ctx context.Context, kubeconfig string) {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for {
		list, err := cs.Discovery().ServerResourcesForGroupVersion("odyssey.dev/v1alpha1")
		if err == nil {
			served := map[string]bool{}
			for _, r := range list.APIResources {
				served[r.Name] = true
			}
			if served["odysseyapps"] && served["odysseyresources"] {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the agent's CRDs never became available: %v", err)
		case <-time.After(time.Second):
		}
	}
}

// waitForIngress waits until k3s' Traefik answers on port (a 404 for an
// unknown host): it is installed by a Helm job after the API is up.
func waitForIngress(t *testing.T, ctx context.Context, port int) {
	t.Helper()
	hc := &http.Client{Timeout: 3 * time.Second}
	for {
		res, err := hc.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err == nil {
			res.Body.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the ingress controller never answered: %v", err)
		case <-time.After(2 * time.Second):
		}
	}
}

// expectServing GETs u through the ingress until it answers with want (the
// ingress picks up new endpoints within a few seconds). *.localhost may not
// resolve on every host, so it dials 127.0.0.1 with the app's Host.
func expectServing(t *testing.T, ctx context.Context, u, want string) {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+parsed.Port())
		},
	}}
	var last string
	for i := 0; i < 90; i++ { // a fresh cluster's ingress needs a while
		res, err := hc.Get(u)
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			last = res.Status + " " + string(body)
			if res.StatusCode == 200 && strings.Contains(string(body), want) {
				return
			}
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatalf("%s did not answer %q; last: %s", u, want, last)
}
