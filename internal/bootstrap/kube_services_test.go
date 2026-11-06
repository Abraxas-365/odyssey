package bootstrap_test

// End-to-end of an app of several services (MS-2) on the long-lived k3d
// cluster (`make test-kube-k3d`): the gateway and a real agent (in-process),
// a real sign-in gate (in-process too, reached by the cluster's Traefik at
// host.k3d.internal) and the cluster's ingress.
//
//  1. An app as an agent from before services left it (one Deployment
//     named after the app, selected by the app's label) moves to services
//     when the new agent starts, without one failed request.
//  2. A version of web ("/"), api ("/api", prefix removed) and a worker,
//     protected: /api goes to the api with its prefix removed after the
//     gate saw it, /apix does not, and the sign-in returns to the URL the
//     person opened.
//  3. The next version drops the api and the worker: their objects go.
//
// It needs the cluster to reach the host (the build e2e's setup); it is
// skipped on a throwaway k3s.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent"
	gate "github.com/Abraxas-365/odyssey/agent/access"
	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/app"
	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefleet"
	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const servicesTestIssuer = "https://odicea.test"

func TestKubeServices(t *testing.T) {
	kubeconfig := os.Getenv("ODYSSEY_TEST_KUBECONFIG")
	if kubeconfig == "" || testing.Short() {
		t.Skip("set ODYSSEY_TEST_KUBECONFIG (make test-kube-k3d): the cluster must reach the host's gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	scheme, err := agent.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	kc, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopClusterAgent(t, ctx, cs))

	// The sign-in gate, on the host; tickets are signed with our own key.
	g := startServicesGate(t)

	h := startFleet(t)
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	target := release.Target{Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}
	name := "ms-" + target.Environment.String()[:8]
	ns := desired.Namespace(target.Environment.String())
	host := name + ".apps.odyssey.localhost"
	base := "http://" + host + ":" + strconv.Itoa(port)
	t.Cleanup(func() { _ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}) })

	// 1. What an agent from before services left running.
	legacyApp(t, ctx, kc, target, name, ns)
	web := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(port))
		},
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	expectServing(t, ctx, base+"/", "Name: legacy")

	// Requests all along the move: not one may fail.
	var failures atomic.Int64
	var failMu sync.Mutex
	var failLog []string
	probeCtx, stopProbe := context.WithCancel(ctx)
	var probing sync.WaitGroup
	probing.Go(func() {
		for probeCtx.Err() == nil {
			at := time.Now()
			res, err := web.Get(base + "/")
			if err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					err = errors.New(res.Status)
				}
			}
			if err != nil && probeCtx.Err() == nil {
				failures.Add(1)
				failMu.Lock()
				failLog = append(failLog, at.UTC().Format("15:04:05.000")+" "+err.Error())
				failMu.Unlock()
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	created, err := h.platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "k3d-services", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: created.Token.Gateway, CAPin: created.Token.CAPin})
	mgr, err := agent.NewManager(cfg, agent.Options{
		App:         app.Config{Domain: "apps.odyssey.localhost", URLScheme: "http", URLPort: port, ReadyDeadline: 90},
		HealthGrace: 30 * time.Second, HealthAddr: "0", MetricsAddr: "0",
		Tunnel: tunnel.Options{Store: store, MinBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		Access: agent.AccessOptions{Enabled: true, VerifyURL: "http://" + buildTestHostName + ":" + strconv.Itoa(g.port) + "/verify",
			KeyNamespace: "odyssey-system", KeyName: "odyssey-access"},
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

	waitFor(t, ctx, "the app to move to services", 3*time.Minute, func() bool {
		var a v1alpha1.OdysseyApp
		if kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a) != nil || a.Status.Phase != v1alpha1.PhaseReady {
			return false
		}
		err := kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &appsv1.Deployment{})
		return apierrors.IsNotFound(err)
	})
	expectServing(t, ctx, base+"/", "Name: rev1")
	time.Sleep(3 * time.Second) // the last requests, after the old pods went
	stopProbe()
	probing.Wait()
	if n := failures.Load(); n != 0 {
		var pols networkingv1.NetworkPolicyList
		_ = kc.List(ctx, &pols, client.InNamespace(ns))
		var made []string
		for _, p := range pols.Items {
			made = append(made, p.Name+"@"+p.CreationTimestamp.UTC().Format("15:04:05"))
		}
		var web appsv1.Deployment
		_ = kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: name + "-web"}, &web)
		t.Fatalf("%d requests failed while the app moved to services:\n%s\nfence: %v; new workload made %s",
			n, strings.Join(failLog, "\n"), made, web.CreationTimestamp.UTC().Format("15:04:05"))
	}
	var svc corev1.Service
	if err := kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Spec.Selector) != 1 || svc.Spec.Selector[app.LabelWorkload] != name+"-web" {
		t.Fatalf("the Service selects %v", svc.Spec.Selector)
	}

	eventually(t, "the agent to connect", func() bool {
		c, err := h.platform.Fleet.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected && c.Agent.Capabilities.CanRunServices() && c.Agent.Capabilities.CanProtect()
	})

	// The CRD's own rules, judged by the real API server (CEL).
	for _, bad := range []v1alpha1.ServiceSpec{
		{Name: "worker", Kind: v1alpha1.ServiceWorker, Route: "/jobs", Image: whoami, Replicas: 1},
		{Name: "web", Kind: v1alpha1.ServiceWeb, Route: "/", StripPrefix: true, Image: whoami, Port: 8080, Replicas: 1},
		{Name: "web", Kind: v1alpha1.ServiceWeb, Route: "/", Image: whoami, Replicas: 1},
	} {
		a := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			desired.LabelOrganization: org.String(), desired.LabelProject: target.Project.String(), desired.LabelEnvironment: target.Environment.String()}},
			Spec: v1alpha1.OdysseyAppSpec{Revision: 9, Services: []v1alpha1.ServiceSpec{bad}}}
		if err := kc.Create(ctx, a, client.DryRunAll); !apierrors.IsInvalid(err) {
			t.Errorf("the cluster accepted %+v (%v)", bad, err)
		}
	}

	// 2. web + api + worker, behind the sign-in.
	k := releasefleet.New(h.platform.Fleet.Dispatch)
	deploy := func(rev int64, services ...release.Service) release.AppStatus {
		t.Helper()
		d := release.DesiredApp{
			Name: name, Revision: rev, Access: string(release.AccessTeam), Services: services,
			Env:    map[string]string{"WHOAMI_PORT_NUMBER": "8080", "WHOAMI_NAME": "rev" + strconv.FormatInt(rev, 10)},
			Labels: map[string]string{"odyssey.dev/deployment": "d" + strconv.FormatInt(rev, 10)},
		}
		if s := services[0]; len(services) == 1 && s.Route == "/" && !s.StripPrefix {
			// One web service at "/": the top-level fields repeat it (Desired).
			d.Image, d.Port, d.HealthPath, d.Replicas = s.Image, s.Port, s.HealthPath, s.Replicas
		}
		err := k.Apply(ctx, target, d)
		if err != nil {
			t.Fatal(err)
		}
		var s release.AppStatus
		waitFor(t, ctx, "revision "+strconv.FormatInt(rev, 10)+" to settle", 4*time.Minute, func() bool {
			s, err = k.Status(ctx, target, name)
			return err == nil && s.ObservedRevision == rev && s.Phase != release.PhaseProgressing
		})
		if s.Phase != release.PhaseReady {
			t.Fatalf("revision %d: %+v", rev, s)
		}
		return s
	}
	webSvc := release.Service{Name: "web", Kind: project.ServiceWeb, Route: "/", Image: whoami, Port: 8080, HealthPath: "/health", Replicas: 1}
	s := deploy(2, webSvc,
		release.Service{Name: "api", Kind: project.ServiceWeb, Route: "/api", StripPrefix: true, Image: whoami, Port: 8080, HealthPath: "/health", Replicas: 1},
		release.Service{Name: "worker", Kind: project.ServiceWorker, Image: whoami, Replicas: 1})
	if len(s.Services) != 3 {
		t.Fatalf("per-service status: %+v", s.Services)
	}
	for _, sv := range s.Services {
		if sv.Phase != release.PhaseReady || sv.ReadyReplicas != 1 {
			t.Errorf("service %+v", sv)
		}
	}
	var webDeploy appsv1.Deployment
	if err := kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: name + "-web"}, &webDeploy); err != nil {
		t.Fatal(err)
	}
	if !hasEnv(webDeploy, "ODICEA_SERVICE_API_URL", "http://"+name+"-api") || hasEnv(webDeploy, "ODICEA_SERVICE_WORKER_URL", "") {
		t.Errorf("web's env: %v", webDeploy.Spec.Template.Spec.Containers[0].Env)
	}

	// Signed out: the sign-in comes back to exactly what was opened.
	opened := base + "/api/orders?page=2"
	var location string
	waitFor(t, ctx, "the gate to guard /api", time.Minute, func() bool {
		res, err := web.Get(opened)
		if err != nil {
			return false
		}
		res.Body.Close()
		location = res.Header.Get("Location")
		return res.StatusCode == http.StatusFound && strings.HasPrefix(location, servicesTestIssuer)
	})
	back, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Query().Get("return"); got != opened {
		t.Fatalf("the sign-in returns to %q, not %q", got, opened)
	}

	// Signing in: the gate takes the ticket and sends the person back.
	ticket := g.ticket(t, host+":"+strconv.Itoa(port), target.Environment.String(), org.String())
	res, err := web.Get(base + gate.CallbackPath + "?" + url.Values{"ticket": {ticket}, "return": {"/api/orders?page=2"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != opened {
		t.Fatalf("callback: %s → %q", res.Status, res.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == gate.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("the callback set no session")
	}
	get := func(path string) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		req.AddCookie(cookie)
		res, err := web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s %s", path, res.Status, body)
		}
		return string(body)
	}
	// whoami answers with its pod's name and the request line it received.
	if b := get("/api/orders?page=2"); !strings.Contains(b, "Hostname: "+name+"-api-") || !strings.Contains(b, "GET /orders?page=2 ") ||
		!strings.Contains(b, "X-Odicea-User: u1") {
		t.Errorf("/api/orders reaches the api without its prefix, signed in:\n%s", b)
	}
	if b := get("/api"); !strings.Contains(b, "Hostname: "+name+"-api-") || !strings.Contains(b, "GET / ") {
		t.Errorf("/api reaches the api's root:\n%s", b)
	}
	if b := get("/apix"); !strings.Contains(b, "Hostname: "+name+"-web-") || !strings.Contains(b, "GET /apix ") {
		t.Errorf("/apix is not the api's:\n%s", b)
	}
	if b := get("/"); !strings.Contains(b, "Hostname: "+name+"-web-") {
		t.Errorf("/ is the web's:\n%s", b)
	}

	// 3. The api and the worker go away.
	if s := deploy(3, webSvc); len(s.Services) != 1 {
		t.Fatalf("per-service status: %+v", s.Services)
	}
	for _, o := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name + "-api"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name + "-worker"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name + "-api"}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name + "-api"}},
		traefikMiddleware(name + "-api-odicea-prefix"),
	} {
		err := kc.Get(ctx, client.ObjectKey{Namespace: ns, Name: o.GetName()}, o)
		if !apierrors.IsNotFound(err) {
			t.Errorf("%T %s is still there (%v)", o, o.GetName(), err)
		}
	}
	waitFor(t, ctx, "/api to be the web's", time.Minute, func() bool {
		return strings.Contains(get("/api/orders"), "Hostname: "+name+"-web-")
	})
}

// legacyApp creates what an agent from before services left for a running
// app: the OdysseyApp (revision 1, top-level fields), and the Deployment,
// Service and Ingress named after the app, the first two selecting by the
// app's label. Its pods answer "legacy"; the new layout answers "rev1".
func legacyApp(t *testing.T, ctx context.Context, kc client.Client, target release.Target, name, ns string) {
	t.Helper()
	ids := map[string]string{desired.LabelOrganization: target.Organization.String(), desired.LabelProject: target.Project.String(),
		desired.LabelEnvironment: target.Environment.String()}
	nsLabels := map[string]string{desired.LabelManagedBy: desired.ManagedBy, "pod-security.kubernetes.io/enforce": "baseline"}
	for k, v := range ids {
		nsLabels[k] = v
	}
	if err := kc.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: nsLabels}}); err != nil {
		t.Fatal(err)
	}
	a := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: ids}, Spec: v1alpha1.OdysseyAppSpec{
		Revision: 1, Image: whoami, Port: 8080, HealthPath: "/health", Replicas: 1,
		Env: map[string]string{"WHOAMI_PORT_NUMBER": "8080", "WHOAMI_NAME": "rev1"},
	}}
	if err := kc.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	owner := []metav1.OwnerReference{*metav1.NewControllerRef(a, v1alpha1.GroupVersion.WithKind("OdysseyApp"))}
	mine := map[string]string{app.LabelManagedBy: app.ManagedBy, app.LabelApp: name}
	one, zero, surge, no := int32(1), intstr.FromInt32(0), intstr.FromInt32(1), false
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: mine, OwnerReferences: owner}, Spec: appsv1.DeploymentSpec{
		Replicas: &one,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{app.LabelApp: name}},
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &surge}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{app.LabelManagedBy: app.ManagedBy, app.LabelApp: name, app.LabelRevision: "1"}},
			Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, EnableServiceLinks: &no, Containers: []corev1.Container{{
				Name: "app", Image: whoami, ImagePullPolicy: corev1.PullIfNotPresent,
				Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
				Env:   []corev1.EnvVar{{Name: "WHOAMI_PORT_NUMBER", Value: "8080"}, {Name: "WHOAMI_NAME", Value: "legacy"}, {Name: "PORT", Value: "8080"}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromString("http")}},
					PeriodSeconds: 3, TimeoutSeconds: 2, SuccessThreshold: 1, FailureThreshold: 2},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}}},
		},
	}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: mine, OwnerReferences: owner}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{app.LabelApp: name},
		Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
	}}
	prefix := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: mine, OwnerReferences: owner}, Spec: networkingv1.IngressSpec{
		Rules: []networkingv1.IngressRule{{Host: name + ".apps.odyssey.localhost", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &prefix, Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{Name: name, Port: networkingv1.ServiceBackendPort{Name: "http"}}}}},
		}}}},
	}}
	for _, o := range []client.Object{d, svc, ing} {
		if err := kc.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
}

func hasEnv(d appsv1.Deployment, name, value string) bool {
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return value == "" || e.Value == value
		}
	}
	return false
}

func traefikMiddleware(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("traefik.io/v1alpha1")
	u.SetKind("Middleware")
	u.SetName(name)
	return u
}

// servicesGate is the sign-in gate on the host, with the keys of the
// tickets it accepts.
type servicesGate struct {
	port int
	key  ed25519.PrivateKey
	kid  string
}

func startServicesGate(t *testing.T) *servicesGate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g := &servicesGate{key: priv, kid: gate.KeyID(pub)}
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
		"kid": g.kid, "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	keys := listen(t, "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) }))

	cookieKey := make([]byte, 32)
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(cookieKey)
	_, _ = rand.Read(seed)
	codec, err := gate.NewCodec(cookieKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gate.NewSigner(seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw := &gate.Gate{Issuer: servicesTestIssuer, Verifier: &gate.Verifier{Issuer: servicesTestIssuer, JWKSURL: "http://" + keys + "/jwks"},
		Cookies: codec, Signer: signer, Log: slog.New(slog.DiscardHandler)}
	addr := listen(t, "0.0.0.0:0", gw.Handler()) // the cluster's Traefik calls it
	_, p, _ := net.SplitHostPort(addr)
	g.port, _ = strconv.Atoi(p)
	return g
}

// ticket is what the control plane signs for a team member of org (a
// compact EdDSA JWT, by hand: golang-jwt stays in its adapters).
func (g *servicesGate) ticket(t *testing.T, aud, env, org string) string {
	t.Helper()
	now := time.Now()
	part := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signed := part(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": g.kid}) + "." + part(map[string]any{
		"iss": servicesTestIssuer, "aud": aud, "sub": "u1", "email": "ana@x.pe", "name": "Ana", "org": org, "app_org": org,
		"env": env, "mode": gate.ModeTeam, "typ": gate.TicketType, "jti": uuid.NewString(),
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
	})
	return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(g.key, []byte(signed)))
}

// listen serves h on addr until the test ends and returns the address.
func listen(t *testing.T, addr string, h http.Handler) string {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().String()
}
