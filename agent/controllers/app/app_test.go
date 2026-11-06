package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cfg = Config{Domain: "apps.test", URLScheme: "http", URLPort: 8088, ReadyDeadline: 180}
)

func newApp(rev int64) *v1alpha1.OdysseyApp {
	return &v1alpha1.OdysseyApp{
		ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: "odyssey-env1"},
		Spec: v1alpha1.OdysseyAppSpec{
			Revision: rev, Image: "r.test/portal@sha256:" + strings.Repeat("a", 64), Port: 8080, HealthPath: "/healthz", Replicas: 2,
			Env:    map[string]string{"B": "2", "A": "1"},
			Labels: map[string]string{"odyssey.dev/deployment": "d1"},
		},
	}
}

func deployment(a *v1alpha1.OdysseyApp, status appsv1.DeploymentStatus) *appsv1.Deployment {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 2}}
	mutateDeployment(d, a, cfg)
	status.ObservedGeneration = 2
	d.Status = status
	return d
}

func pod(rev string, statuses ...corev1.ContainerStatus) corev1.Pod {
	ready := corev1.ConditionTrue
	for _, c := range statuses {
		if !c.Ready {
			ready = corev1.ConditionFalse
		}
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelApp: "portal", LabelWorkload: "portal-web", LabelRevision: rev}},
		Status:     corev1.PodStatus{ContainerStatuses: statuses, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}},
	}
}

// workloadKey is the Deployment of a's only service (an app without services).
func workloadKey(a *v1alpha1.OdysseyApp) client.ObjectKey {
	return client.ObjectKey{Namespace: a.Namespace, Name: a.Name + "-web"}
}

func running(since time.Duration, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-since))}}}
}

func waiting(reason, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}}
}

func TestObjects(t *testing.T) {
	a := newApp(7)
	d := &appsv1.Deployment{}
	mutateDeployment(d, a, cfg)
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != a.Spec.Image || *d.Spec.Replicas != 2 || c.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Fatalf("deployment: %+v", d.Spec)
	}
	if ru := d.Spec.Strategy.RollingUpdate; ru.MaxUnavailable.IntValue() != 0 || ru.MaxSurge.IntValue() != 1 {
		t.Errorf("the previous version must keep serving during a rollout: %+v", ru)
	}
	if d.Spec.Template.Labels[LabelRevision] != "7" || d.Spec.Template.Labels["odyssey.dev/deployment"] != "d1" {
		t.Errorf("pod labels: %v", d.Spec.Template.Labels)
	}
	a.Spec.Labels[LabelRevision], a.Spec.Labels[LabelApp] = "99", "other"
	mutateDeployment(d, a, cfg)
	if l := d.Spec.Template.Labels; l[LabelRevision] != "7" || l[LabelApp] != "portal" || l[LabelManagedBy] != ManagedBy {
		t.Errorf("spec labels must not replace the agent's own: %v", l)
	}
	delete(a.Spec.Labels, LabelRevision)
	delete(a.Spec.Labels, LabelApp)
	if len(d.Spec.Selector.MatchLabels) != 1 || d.Spec.Selector.MatchLabels[LabelWorkload] != "portal-web" {
		t.Errorf("the selector is immutable, so it holds only the workload: %v", d.Spec.Selector.MatchLabels)
	}
	var env []string
	for _, e := range c.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	if strings.Join(env, ",") != "A=1,B=2,PORT=8080" {
		t.Errorf("env: %v", env)
	}
	if *c.SecurityContext.AllowPrivilegeEscalation || c.SecurityContext.Capabilities.Drop[0] != "ALL" || *d.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Errorf("security: %+v", c.SecurityContext)
	}
	if *d.Spec.ProgressDeadlineSeconds != 180 {
		t.Errorf("deadline: %d", *d.Spec.ProgressDeadlineSeconds)
	}

	a.Spec.Env["PORT"] = "9000"
	mutateDeployment(d, a, cfg)
	if got := d.Spec.Template.Spec.Containers[0].Env; got[len(got)-1].Value != "9000" || len(got) != 3 {
		t.Errorf("an explicit PORT wins: %v", got)
	}

	ing := &networkingv1.Ingress{}
	mutateIngress(ing, a, cfg)
	if ing.Spec.Rules[0].Host != "portal.apps.test" || cfg.URL(a) != "http://portal.apps.test:8088" {
		t.Errorf("host %q url %q", ing.Spec.Rules[0].Host, cfg.URL(a))
	}
}

func TestObserve(t *testing.T) {
	a := newApp(3)
	healthy := appsv1.DeploymentStatus{Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2, ReadyReplicas: 2}
	rolling := appsv1.DeploymentStatus{Replicas: 3, UpdatedReplicas: 1, AvailableReplicas: 2, ReadyReplicas: 2}
	oldStillThere := appsv1.DeploymentStatus{Replicas: 3, UpdatedReplicas: 2, AvailableReplicas: 3, ReadyReplicas: 3}
	stalled := rolling
	stalled.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
	unseen := deployment(a, healthy)
	unseen.Status.ObservedGeneration = 1
	crashed := corev1.ContainerStatus{RestartCount: 3, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Message: "panic: no DATABASE_URL\nstack…"}}}

	for _, c := range []struct {
		name   string
		d      *appsv1.Deployment
		pods   []corev1.Pod
		phase  v1alpha1.Phase
		reason v1alpha1.Reason
		msg    string
	}{
		{"ready", deployment(a, healthy), []corev1.Pod{pod("3", running(time.Minute, true))}, v1alpha1.PhaseReady, "", ""},
		{"rolling out", deployment(a, rolling), []corev1.Pod{pod("3", running(5*time.Second, false)), pod("2", running(time.Hour, true))}, v1alpha1.PhaseProgressing, "", "1 of 2 replicas updated"},
		{"template of an older revision", deployment(newApp(2), healthy), nil, v1alpha1.PhaseProgressing, "", "applying"},
		{"image cannot be pulled", deployment(a, rolling), []corev1.Pod{pod("3", waiting("ImagePullBackOff", "Back-off pulling image \"r.test/x\""))}, v1alpha1.PhaseFailed, v1alpha1.ReasonImagePull, "Back-off pulling"},
		{"runtime detail without its gRPC wrapping", deployment(a, rolling), []corev1.Pod{pod("3", waiting("ErrImagePull", "rpc error: code = NotFound desc = failed to pull"))}, v1alpha1.PhaseFailed, v1alpha1.ReasonImagePull, "failed to pull"},
		{"crash loop", deployment(a, rolling), []corev1.Pod{pod("3", waiting("CrashLoopBackOff", ""))}, v1alpha1.PhaseFailed, v1alpha1.ReasonCrash, "restarted"},
		{"restarts with exit code", deployment(a, rolling), []corev1.Pod{pod("3", crashed)}, v1alpha1.PhaseFailed, v1alpha1.ReasonCrash, "exited with code 2: panic: no DATABASE_URL"},
		{"health check past the grace", deployment(a, rolling), []corev1.Pod{pod("3", running(2*time.Minute, false))}, v1alpha1.PhaseFailed, v1alpha1.ReasonUnhealthy, "did not pass within 1m0s"},
		{"rollout stalled", deployment(a, stalled), nil, v1alpha1.PhaseFailed, v1alpha1.ReasonUnhealthy, "never passed"},
		{"a slow image download is not a failed health check", deployment(a, stalled), []corev1.Pod{pod("3", waiting("ContainerCreating", ""))}, v1alpha1.PhaseProgressing, "", "downloading"},
		{"a slow start within the grace", deployment(a, rolling), []corev1.Pod{pod("3", running(50*time.Second, false))}, v1alpha1.PhaseProgressing, "", ""},
		{"not ready while an old replica remains", deployment(a, oldStillThere), []corev1.Pod{pod("3", running(time.Minute, true)), pod("3", running(time.Minute, true)), pod("2", running(time.Hour, true))}, v1alpha1.PhaseProgressing, "", "2 of 2 replicas updated"},
		{"not ready before the Deployment saw the template", unseen, []corev1.Pod{pod("3", running(time.Minute, true))}, v1alpha1.PhaseProgressing, "", ""},
		{"old pods crashing do not fail the new revision", deployment(a, rolling), []corev1.Pod{pod("2", waiting("CrashLoopBackOff", ""))}, v1alpha1.PhaseProgressing, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := Observe(c.d, c.pods, 3, now, time.Minute)
			if o.Phase != c.phase || o.Reason != c.reason || !strings.Contains(o.Message, c.msg) {
				t.Errorf("got %+v", o)
			}
		})
	}
	pods := []corev1.Pod{pod("3", running(time.Minute, true)), pod("3", running(time.Second, false)), pod("2", running(time.Hour, true))}
	if o := Observe(deployment(a, rolling), pods, 3, now, time.Minute); o.ReadyReplicas != 1 {
		t.Errorf("ready replicas count the new revision only: %+v", o)
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func run(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp, at time.Time) (*v1alpha1.OdysseyApp, ctrl.Result) {
	t.Helper()
	return runWith(t, &Reconciler{Client: c, API: c, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return at }}, c, a)
}

func runWith(t *testing.T, r *Reconciler, c client.Client, a *v1alpha1.OdysseyApp) (*v1alpha1.OdysseyApp, ctrl.Result) {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	if err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &got); err != nil {
		t.Fatal(err)
	}
	return &got, res
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	a := newApp(1)
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()

	got, res := run(t, c, a, now)
	if got.Status.ObservedRevision != 1 || got.Status.Phase != v1alpha1.PhaseProgressing || got.Status.URL != "http://portal.apps.test:8088" || res.RequeueAfter == 0 {
		t.Fatalf("first reconcile: %+v %+v", got.Status, res)
	}
	var d appsv1.Deployment
	if err := c.Get(ctx, workloadKey(a), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.OwnerReferences) != 1 || d.OwnerReferences[0].Name != "portal" {
		t.Errorf("owner: %+v", d.OwnerReferences)
	}
	for _, o := range []client.Object{&corev1.Service{}, &networkingv1.Ingress{}} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(a), o); err != nil {
			t.Errorf("%T: %v", o, err)
		}
	}

	// The rollout finishes.
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2, ReadyReplicas: 2}
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portal-a", "portal-b"} {
		p := pod("1", running(time.Minute, true))
		p.Name, p.Namespace = name, a.Namespace
		if err := c.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	got, res = run(t, c, a, now)
	if got.Status.Phase != v1alpha1.PhaseReady || got.Status.ReadyReplicas != 2 || res.RequeueAfter != 0 {
		t.Fatalf("ready: %+v %+v", got.Status, res)
	}

	// Later trouble of a published revision does not rewrite its verdict:
	// the status describes the rollout the control plane waited for.
	crashing := pod("1", waiting("CrashLoopBackOff", ""))
	crashing.Name, crashing.Namespace = "portal-c", a.Namespace
	if err := c.Create(ctx, &crashing); err != nil {
		t.Fatal(err)
	}
	if got, _ = run(t, c, a, now); got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("a ready revision stays ready: %+v", got.Status)
	}

	// A new revision whose pods never pass the health check fails, and stays
	// failed even if a later look would say otherwise.
	got.Spec.Revision = 2
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	bad := pod("2", corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-2 * time.Minute))}}})
	bad.Name, bad.Namespace = "portal-x", a.Namespace
	if err := c.Create(ctx, &bad); err != nil {
		t.Fatal(err)
	}
	got, _ = run(t, c, a, now)
	if got.Status.ObservedRevision != 2 || got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonUnhealthy {
		t.Fatalf("unhealthy: %+v", got.Status)
	}
	if err := c.Delete(ctx, &bad); err != nil {
		t.Fatal(err)
	}
	if got, _ = run(t, c, a, now); got.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("a failed revision stays failed: %+v", got.Status)
	}
}

func TestReconcileRefusesATakenHost(t *testing.T) {
	a := newApp(1)
	other := newApp(1)
	other.Namespace = "odyssey-env2"
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, other).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	if got, _ := run(t, c, other, now); got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("first app: %+v", got.Status)
	}
	got, _ := run(t, c, a, now)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || !strings.Contains(got.Status.Message, "odyssey-env2/portal") {
		t.Fatalf("taken host: %+v", got.Status)
	}
	if err := c.Get(context.Background(), workloadKey(a), &appsv1.Deployment{}); err == nil {
		t.Error("nothing is created for an app whose host is taken")
	}
}

func TestReconcileHostRaces(t *testing.T) {
	ctx := context.Background()
	a := newApp(1)
	other := newApp(1)
	other.Namespace = "odyssey-env2"
	// Both apps got their ingress at once: the older keeps the host and the
	// other removes its own ingress.
	older := metav1.NewTime(now.Add(-time.Minute))
	ingress := func(ns string, at metav1.Time) *networkingv1.Ingress {
		i := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: ns, CreationTimestamp: at}}
		mutateIngress(i, a, cfg)
		return i
	}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, other, ingress(other.Namespace, older), ingress(a.Namespace, metav1.NewTime(now))).
		WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	if got, _ := run(t, c, other, now); got.Status.Phase == v1alpha1.PhaseFailed {
		t.Fatalf("the older ingress keeps the host: %+v", got.Status)
	}
	if got, _ := run(t, c, a, now); got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig {
		t.Fatalf("the newer one steps aside: %+v", got.Status)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Errorf("the losing app removes its ingress: %v", err)
	}
}

func TestReconcileSeesUnmanagedObjects(t *testing.T) {
	a := newApp(1)
	// Objects without the managed-by label are invisible to the agent's
	// cache; the API reader still sees them.
	foreign := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}}
	mutateIngress(foreign, a, cfg)
	foreign.Labels = nil
	squatter := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "portal-web", Namespace: a.Namespace}}
	api := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, foreign).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	r := &Reconciler{Client: cacheOf(api), API: api, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now }}
	if got, _ := runWith(t, r, api, a); got.Status.Phase != v1alpha1.PhaseFailed || !strings.Contains(got.Status.Message, "default/legacy") {
		t.Fatalf("an unmanaged ingress holds the host: %+v", got.Status)
	}

	api = fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, squatter).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	r = &Reconciler{Client: cacheOf(api), API: api, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now }}
	if got, _ := runWith(t, r, api, a); got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || !strings.Contains(got.Status.Message, "Deployment named portal-web already exists") {
		t.Fatalf("an unmanaged Deployment in the way: %+v", got.Status)
	}
}

// cacheOf behaves like the agent's cache: it hides objects without the
// managed-by label.
func cacheOf(c client.WithWatch) client.Client {
	return interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			live := obj.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, key, live, opts...); err != nil {
				return err
			}
			if _, app := obj.(*v1alpha1.OdysseyApp); !app && live.GetLabels()[LabelManagedBy] != ManagedBy {
				return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

func TestReconcileRetriesConflictsQuietly(t *testing.T) {
	a := newApp(1)
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a).WithStatusSubresource(&v1alpha1.OdysseyApp{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return apierrors.NewConflict(schema.GroupResource{Group: "odyssey.dev", Resource: "odysseyapps"}, a.Name, errors.New("modified"))
		}}).Build()
	r := &Reconciler{Client: c, API: c, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now }}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("a conflict is looked at again, not reported: %+v %v", res, err)
	}
}
