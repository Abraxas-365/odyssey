package app

import (
	"context"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func image(c string) string { return "r.test/portal@sha256:" + strings.Repeat(c, 64) }

// threeServices: the web at "/", an api at "/api" (prefix removed) and a
// worker.
func threeServices(rev int64) *v1alpha1.OdysseyApp {
	a := newApp(rev)
	a.UID = types.UID("app-uid")
	a.Labels = map[string]string{LabelEnvironment: "env-1"}
	a.Spec.Image, a.Spec.Port, a.Spec.HealthPath, a.Spec.Replicas = "", 0, "", 0
	a.Spec.Services = []v1alpha1.ServiceSpec{
		{Name: "web", Kind: v1alpha1.ServiceWeb, Route: "/", Image: image("a"), Port: 3000, HealthPath: "/", Replicas: 2},
		{Name: "api", Kind: v1alpha1.ServiceWeb, Route: "/api", StripPrefix: true, Image: image("b"), Port: 8080, HealthPath: "/healthz", Replicas: 1},
		{Name: "worker", Kind: v1alpha1.ServiceWorker, Image: image("c"), Replicas: 1},
	}
	return a
}

func get[T client.Object](t *testing.T, c client.Client, ns, name string, obj T) T {
	t.Helper()
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			var zero T
			return zero
		}
		t.Fatal(err)
	}
	return obj
}

func deploy(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp, name string) *appsv1.Deployment {
	t.Helper()
	return get(t, c, a.Namespace, name, &appsv1.Deployment{})
}

// becomeReady plays the Deployment controller: name rolls out a's revision
// with ready pods.
func becomeReady(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp, name string) {
	t.Helper()
	ctx := context.Background()
	d := deploy(t, c, a, name)
	if d == nil {
		t.Fatalf("no Deployment %s", name)
	}
	n := *d.Spec.Replicas
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: n, UpdatedReplicas: n, AvailableReplicas: n, ReadyReplicas: n}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	for i := range int(n) {
		p := pod(d.Spec.Template.Labels[LabelRevision], running(time.Minute, true))
		p.Name, p.Namespace = name+"-"+d.Spec.Template.Labels[LabelRevision]+"-"+string(rune('a'+i)), a.Namespace
		p.Labels[LabelWorkload] = name
		if err := c.Create(ctx, &p); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
}

func envOfContainer(d *appsv1.Deployment) map[string]string {
	out := map[string]string{}
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestWorkloadNames(t *testing.T) {
	ws := workloads(threeServices(1))
	got := []string{}
	for _, w := range ws {
		got = append(got, w.Deployment+"/"+w.Object)
	}
	if strings.Join(got, ",") != "portal-web/portal,portal-api/portal-api,portal-worker/portal-worker" {
		t.Errorf("names: %v", got)
	}
	// an app from before services is one web service at "/"
	if ws := workloads(newApp(1)); len(ws) != 1 || ws[0].Name != "web" || !ws[0].root() || ws[0].Image != newApp(1).Spec.Image || ws[0].Object != "portal" {
		t.Errorf("legacy: %+v", ws)
	}
	long := strings.Repeat("a", 60)
	if n := objectName(long, "worker"); len(n) > 63 || !strings.HasSuffix(n, "-worker") || n == objectName(long+"b", "worker") {
		t.Errorf("long name %q", n)
	}
}

func TestServicesRollOutInOrder(t *testing.T) {
	a := threeServices(1)
	r, c := exposed(t, traefikMapper(), a)
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseProgressing || len(got.Status.Services) != 3 {
		t.Fatalf("status: %+v", got.Status)
	}
	// the internal ones (the worker) first; the public api waits for them;
	// the app's address (the web's Ingress) exists from the start
	if deploy(t, c, a, "portal-worker") == nil {
		t.Fatal("the worker must start first")
	}
	if deploy(t, c, a, "portal-api") != nil || deploy(t, c, a, "portal-web") != nil {
		t.Fatal("public services wait for the internal ones")
	}
	if get(t, c, a.Namespace, "portal-worker", &corev1.Service{}) != nil {
		t.Error("a worker has no Service")
	}
	w := deploy(t, c, a, "portal-worker")
	if w.Spec.Template.Spec.Containers[0].ReadinessProbe != nil || len(w.Spec.Template.Spec.Containers[0].Ports) != 0 {
		t.Errorf("a worker has no port nor health check: %+v", w.Spec.Template.Spec.Containers[0])
	}
	if _, ok := envOfContainer(w)["PORT"]; ok {
		t.Error("a worker gets no PORT")
	}

	becomeReady(t, c, a, "portal-worker")
	got, _ = runWith(t, r, c, a)
	web, api := deploy(t, c, a, "portal-web"), deploy(t, c, a, "portal-api")
	if web == nil || api == nil {
		t.Fatalf("public services roll out once the internal ones are ready: %+v", got.Status)
	}
	if l := web.Spec.Template.Spec.Containers[0].Lifecycle; l == nil || l.PreStop == nil || l.PreStop.Sleep == nil || l.PreStop.Sleep.Seconds != drainSeconds {
		t.Errorf("a web pod keeps answering while the ingress lets it go: %+v", l)
	}
	if w.Spec.Template.Spec.Containers[0].Lifecycle != nil {
		t.Error("a worker serves nothing to drain")
	}
	if web.Spec.Selector.MatchLabels[LabelWorkload] != "portal-web" || web.Spec.Template.Labels[LabelApp] != "portal" || web.Spec.Template.Labels[LabelService] != "web" {
		t.Errorf("labels: %v %v", web.Spec.Selector, web.Spec.Template.Labels)
	}
	if e := envOfContainer(web); e["ODICEA_SERVICE_API_URL"] != "http://portal-api" || e["PORT"] != "3000" || e["ODICEA_SERVICE_WORKER_URL"] != "" {
		t.Errorf("web env: %v", e)
	}
	if e := envOfContainer(api); e["ODICEA_SERVICE_WEB_URL"] != "http://portal" || e["PORT"] != "8080" {
		t.Errorf("api env: %v", e)
	}
	if e := envOfContainer(w); e["ODICEA_SERVICE_WEB_URL"] != "http://portal" || e["ODICEA_SERVICE_API_URL"] != "http://portal-api" {
		t.Errorf("worker env: %v", e)
	}
	if get(t, c, a.Namespace, "portal", &networkingv1.Ingress{}) == nil {
		t.Error("the app's address exists from the start")
	}
	if get(t, c, a.Namespace, "portal-api", &networkingv1.Ingress{}) != nil {
		t.Error("/api is routed only once its service is ready")
	}

	becomeReady(t, c, a, "portal-web")
	becomeReady(t, c, a, "portal-api")
	got, _ = runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseReady || got.Status.ReadyReplicas != 4 {
		t.Fatalf("ready: %+v", got.Status)
	}
	for _, s := range got.Status.Services {
		if s.Phase != v1alpha1.PhaseReady {
			t.Errorf("service %+v", s)
		}
	}
	ing := get(t, c, a.Namespace, "portal-api", &networkingv1.Ingress{})
	if ing == nil {
		t.Fatal("/api is routed")
	}
	paths := ing.Spec.Rules[0].HTTP.Paths
	if len(paths) != 2 || paths[0].Path != "/api" || *paths[0].PathType != networkingv1.PathTypeExact ||
		paths[1].Path != "/api/" || *paths[1].PathType != networkingv1.PathTypePrefix || paths[0].Backend.Service.Name != "portal-api" {
		t.Errorf("/api must not catch /apix: %+v", paths)
	}
	if ing.Spec.Rules[0].Host != "portal.apps.test" {
		t.Errorf("same host: %v", ing.Spec.Rules[0].Host)
	}
	if ann := ing.Annotations[annotationMiddlewares]; ann != "odyssey-env1-portal-api-odicea-prefix@kubernetescrd" {
		t.Errorf("public api: only the prefix removal: %q", ann)
	}
	if p := ing.Annotations[annotationPriority]; p != "1004" {
		t.Errorf("the api's route ranks above the root's (Traefik ranks by rule length): %q", p)
	}
	if _, ok := get(t, c, a.Namespace, "portal", &networkingv1.Ingress{}).Annotations[annotationPriority]; ok {
		t.Error("the root keeps Traefik's default rank")
	}
	if m := middleware(t, c, a.Namespace, "portal-api-odicea-prefix"); m == nil {
		t.Error("the prefix remover exists")
	}
	if s := get(t, c, a.Namespace, "portal", &corev1.Service{}); s.Spec.Selector[LabelWorkload] != "portal-web" || len(s.Spec.Selector) != 1 {
		t.Errorf("service portal selects the web: %v", s.Spec.Selector)
	}
}

func TestProtectedServicesChainTheGateBeforeThePrefix(t *testing.T) {
	a := threeServices(1)
	a.Spec.Access = &v1alpha1.AccessSpec{Mode: v1alpha1.AccessTeam, Organization: "org-1"}
	r, c := exposed(t, traefikMapper(), a)
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-worker")
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-web")
	becomeReady(t, c, a, "portal-api")
	runWith(t, r, c, a)
	gate := "odyssey-env1-portal-odicea-strip@kubernetescrd,odyssey-env1-portal-odicea-access@kubernetescrd"
	if ing := get(t, c, a.Namespace, "portal", &networkingv1.Ingress{}); ing.Annotations[annotationMiddlewares] != gate {
		t.Errorf("web: %q", ing.Annotations[annotationMiddlewares])
	}
	// the gate sees, and sends the person back to, /api/…: the prefix goes last
	if ing := get(t, c, a.Namespace, "portal-api", &networkingv1.Ingress{}); ing.Annotations[annotationMiddlewares] != gate+",odyssey-env1-portal-api-odicea-prefix@kubernetescrd" {
		t.Errorf("api: %q", ing.Annotations[annotationMiddlewares])
	}
	if e := envOfContainer(deploy(t, c, a, "portal-api")); e["ODICEA_AUTH_AUDIENCE"] != "portal.apps.test:8088" {
		t.Errorf("every service gets the app's auth: %v", e)
	}
}

// Traefik without the sign-in gate still removes routes' prefixes; it
// protects nothing (a protected app fails closed).
func TestPrefixesWithoutTheGate(t *testing.T) {
	a := threeServices(1)
	r, c := exposed(t, traefikMapper(), a)
	r.Exposure = &Traefik{Client: c, Mapper: traefikMapper()}
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-worker")
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-web")
	becomeReady(t, c, a, "portal-api")
	if got, _ := runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("status: %+v", got.Status)
	}
	if ing := get(t, c, a.Namespace, "portal-api", &networkingv1.Ingress{}); ing.Annotations[annotationMiddlewares] != "odyssey-env1-portal-api-odicea-prefix@kubernetescrd" {
		t.Errorf("api: %q", ing.Annotations[annotationMiddlewares])
	}

	p := protectedApp(v1alpha1.AccessTeam)
	r, c = exposed(t, traefikMapper(), p)
	r.Exposure = &Traefik{Client: c, Mapper: traefikMapper()}
	if got, _ := runWith(t, r, c, p); got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Message != UnavailableMessage {
		t.Errorf("protected without the gate: %+v", got.Status)
	}
	if get(t, c, p.Namespace, p.Name, &networkingv1.Ingress{}) != nil {
		t.Error("a protected app was exposed without the gate")
	}
}

func TestCallbackRidesOnTheFirstRouteWithoutRoot(t *testing.T) {
	a := threeServices(1)
	a.Spec.Access = &v1alpha1.AccessSpec{Mode: v1alpha1.AccessTeam, Organization: "org-1"}
	a.Spec.Services = a.Spec.Services[1:] // only /api and the worker
	x := &rollout{a: a, all: workloads(a), protected: true}
	i := &networkingv1.Ingress{}
	mutateRoute(i, a, x.all[0], x.callback(x.all[0]), cfg)
	paths := i.Spec.Rules[0].HTTP.Paths
	if len(paths) != 3 || paths[2].Path != "/_odicea/callback" || *paths[2].PathType != networkingv1.PathTypeExact {
		t.Errorf("the sign-in callback must reach the gate: %+v", paths)
	}
	x.protected = false
	if x.callback(x.all[0]) {
		t.Error("a public app needs no callback")
	}
}

func TestAServiceFailingFailsTheApp(t *testing.T) {
	a := threeServices(1)
	r, c := exposed(t, traefikMapper(), a)
	runWith(t, r, c, a)
	p := pod("1", waiting("CrashLoopBackOff", ""))
	p.Name, p.Namespace = "portal-worker-x", a.Namespace
	p.Labels[LabelWorkload] = "portal-worker"
	if err := c.Create(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonCrash || !strings.HasPrefix(got.Status.Message, "worker: ") {
		t.Fatalf("status: %+v", got.Status)
	}
	byName := map[string]v1alpha1.ServiceStatus{}
	for _, s := range got.Status.Services {
		byName[s.Name] = s
	}
	if byName["worker"].Phase != v1alpha1.PhaseFailed || byName["web"].Phase != v1alpha1.PhaseProgressing {
		t.Errorf("per service: %+v", got.Status.Services)
	}
}

func TestLegacyAppMigratesWithoutDowntime(t *testing.T) {
	ctx := context.Background()
	old := newApp(1)
	old.UID = "app-uid"
	// what an agent before services left: Deployment and Service "portal",
	// both selecting by the app's label
	legacy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: old.Namespace}}
	mutateDeployment(legacy, old, cfg)
	legacy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{LabelApp: "portal"}}
	legacy.OwnerReferences = []metav1.OwnerReference{{APIVersion: "odyssey.dev/v1alpha1", Kind: "OdysseyApp", Name: "portal", UID: "app-uid", Controller: ptr(true)}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: old.Namespace, OwnerReferences: legacy.OwnerReferences}}
	svc.Labels = labels(old)
	svc.Spec.Selector = map[string]string{LabelApp: "portal"}

	a := newApp(2)
	a.UID = "app-uid"
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, legacy, svc).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	got, _ := run(t, c, a, now)
	if got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("status: %+v", got.Status)
	}
	if deploy(t, c, a, "portal-web") == nil || deploy(t, c, a, "portal") == nil {
		t.Fatal("the new workload starts next to the old one")
	}
	if s := get(t, c, a.Namespace, "portal", &corev1.Service{}); s.Spec.Selector[LabelApp] != "portal" || len(s.Spec.Selector) != 1 {
		t.Errorf("until the new one is ready the Service reaches both: %v", s.Spec.Selector)
	}

	becomeReady(t, c, a, "portal-web")
	got, _ = run(t, c, a, now)
	if s := get(t, c, a.Namespace, "portal", &corev1.Service{}); s.Spec.Selector[LabelWorkload] != "portal-web" || len(s.Spec.Selector) != 1 {
		t.Errorf("then only the new one: %v", s.Spec.Selector)
	}
	if deploy(t, c, a, "portal") == nil || got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("the old Deployment stays while the ingress follows the Service: %+v", got.Status)
	}
	got, _ = run(t, c, a, now.Add(legacyDrain))
	if deploy(t, c, a, "portal") != nil {
		t.Error("and then it goes")
	}
	if got.Status.Phase != v1alpha1.PhaseReady {
		t.Errorf("status: %+v", got.Status)
	}
	if _, ok := got.Annotations[annotationLegacyDrain]; ok {
		t.Error("the drain's record is cleared")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); err != nil {
		t.Errorf("the address stays: %v", err)
	}
}

func TestADroppedServiceIsRemovedOnceTheAppIsReady(t *testing.T) {
	ctx := context.Background()
	a := threeServices(1)
	r, c := exposed(t, traefikMapper(), a)
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-worker")
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-web")
	becomeReady(t, c, a, "portal-api")
	runWith(t, r, c, a)

	var cur v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Revision = 2
	cur.Spec.Services = cur.Spec.Services[:1] // only the web
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	// the web's new revision is rolling out
	web := deploy(t, c, a, "portal-web")
	web.Status.UpdatedReplicas = 0
	if err := c.Status().Update(ctx, web); err != nil {
		t.Fatal(err)
	}
	runWith(t, r, c, a)
	if deploy(t, c, a, "portal-api") == nil {
		t.Fatal("nothing goes before the new revision is ready")
	}
	becomeReady(t, c, a, "portal-web")
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("status: %+v", got.Status)
	}
	if deploy(t, c, a, "portal-api") != nil || deploy(t, c, a, "portal-worker") != nil {
		t.Error("the dropped services' Deployments go")
	}
	if get(t, c, a.Namespace, "portal-api", &corev1.Service{}) != nil || get(t, c, a.Namespace, "portal-api", &networkingv1.Ingress{}) != nil {
		t.Error("and their Service and Ingress")
	}
	if middleware(t, c, a.Namespace, "portal-api-odicea-prefix") != nil {
		t.Error("and their prefix remover")
	}
	if get(t, c, a.Namespace, "portal", &networkingv1.Ingress{}) == nil || deploy(t, c, a, "portal-web") == nil {
		t.Error("the web stays")
	}
}

func TestAServiceNeverTakesAnotherOwnersObject(t *testing.T) {
	a := threeServices(1)
	a.Spec.Services = []v1alpha1.ServiceSpec{{Name: "redis", Kind: v1alpha1.ServiceWorker, Image: image("d"), Replicas: 1}}
	// the Deployment of a redis resource named "portal"
	theirs := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "portal-redis", Namespace: a.Namespace,
		Labels:          map[string]string{LabelManagedBy: ManagedBy},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "odyssey.dev/v1alpha1", Kind: "OdysseyResource", Name: "portal", UID: "res-uid", Controller: ptr(true)}}}}
	theirs.UID = "dep-uid"
	r, c := exposed(t, traefikMapper(), a, theirs)
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || !strings.Contains(got.Status.Message, "portal-redis") {
		t.Fatalf("status: %+v", got.Status)
	}
	if d := deploy(t, c, a, "portal-redis"); d.OwnerReferences[0].UID != "res-uid" {
		t.Errorf("taken over: %+v", d.OwnerReferences)
	}
}
