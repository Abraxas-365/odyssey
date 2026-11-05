package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func queuedScan(name string, age time.Duration, suspended bool, conds ...batchv1.JobCondition) *batchv1.Job {
	b := succeeded()
	b.Name = name
	j := ScanJob(b, scanCfg())
	j.Name = name
	j.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
	j.Spec.Suspend = &suspended
	j.Status.Conditions = conds
	return j
}

func suspended(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	var j batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: name}, &j); err != nil {
		t.Fatal(err)
	}
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

// Scans run one at a time, oldest first: nothing starts while one runs, and a
// finished one frees the slot.
func TestScanQueueRunsOneAtATimeOldestFirst(t *testing.T) {
	ctx := context.Background()
	done := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(
		queuedScan("scan-old", 3*time.Minute, true),
		queuedScan("scan-mid", 2*time.Minute, true),
		queuedScan("scan-new", time.Minute, true),
		queuedScan("scan-ended", 10*time.Minute, false, done),
	).Build()
	q := &ScanQueue{Client: c, API: c, Config: scanCfg(), warmedAt: time.Now()}

	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if suspended(t, c, "scan-old") || !suspended(t, c, "scan-mid") || !suspended(t, c, "scan-new") {
		t.Fatal("the first tick starts the oldest and only it")
	}
	// it runs: nothing else starts, however many ticks
	for range 3 {
		if err := q.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !suspended(t, c, "scan-mid") || !suspended(t, c, "scan-new") {
		t.Fatal("a second scan started while one runs")
	}
	// it ends: the next one goes
	var old batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "scan-old"}, &old); err != nil {
		t.Fatal(err)
	}
	old.Status.Conditions = []batchv1.JobCondition{done}
	if err := c.Status().Update(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if suspended(t, c, "scan-mid") || !suspended(t, c, "scan-new") {
		t.Fatal("the next oldest starts when the running one ends")
	}
}

func TestScanQueueFailedJobFreesTheSlotAndOffDoesNothing(t *testing.T) {
	ctx := context.Background()
	failed := batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(
		queuedScan("scan-failed", 3*time.Minute, false, failed),
		queuedScan("scan-next", time.Minute, true),
	).Build()
	off := &ScanQueue{Client: c, API: c, Config: cfg}
	if err := off.Tick(ctx); err != nil || !suspended(t, c, "scan-next") {
		t.Fatalf("scans off: %v", err)
	}
	q := &ScanQueue{Client: c, API: c, Config: scanCfg(), warmedAt: time.Now()}
	if err := q.Tick(ctx); err != nil || suspended(t, c, "scan-next") {
		t.Fatalf("a failed scan holds the queue: %v", err)
	}
}

// The warm-up only saves time: when it cannot be made, the queue still starts
// scans. And a Job stuck long past its time limit (pod on a lost node) does
// not hold the queue forever.
func TestScanQueueGoesOnWithoutTheWarmupAndPastAStuckJob(t *testing.T) {
	ctx := context.Background()
	stuck := queuedScan("scan-stuck", time.Hour, false)
	stuck.Status.StartTime = &metav1.Time{Time: time.Now().Add(-time.Hour)}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(stuck, queuedScan("scan-next", time.Minute, true)).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return apierrors.NewForbidden(batchv1.Resource("jobs"), obj.GetName(), errors.New("quota"))
		}}).Build()
	q := &ScanQueue{Client: c, API: c, Config: scanCfg()}
	if err := q.Tick(ctx); err != nil {
		t.Fatalf("a failed warm-up stops the queue: %v", err)
	}
	if suspended(t, c, "scan-next") {
		t.Fatal("the next scan waits behind a Job stuck past its time limit")
	}
	// one that is merely running (within its limit) still holds it
	running := queuedScan("scan-running", time.Minute, false)
	running.Status.StartTime = &metav1.Time{Time: time.Now().Add(-time.Minute)}
	c2 := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(running, queuedScan("scan-waits", 30*time.Second, true)).Build()
	q2 := &ScanQueue{Client: c2, API: c2, Config: scanCfg(), warmedAt: time.Now()}
	if err := q2.Tick(ctx); err != nil || !suspended(t, c2, "scan-waits") {
		t.Fatalf("a running scan must hold the queue: %v", err)
	}
}

// The warm-up Job is created once an hour at most (and its volume) and is
// queued like a scan: while it runs, scans wait.
func TestScanQueueWarmsTheDatabase(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	now := time.Now()
	q := &ScanQueue{Client: c, API: c, Config: scanCfg(), Now: func() time.Time { return now }}
	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: ScanCacheClaim}, &pvc); err != nil {
		t.Fatalf("no cache volume: %v", err)
	}
	// created suspended, and the same tick resumes it: it is the only one
	if suspended(t, c, WarmupJobName) {
		t.Fatal("a lone warm-up should run")
	}
	// ticks within the hour do not make another
	var w batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: WarmupJobName}, &w); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, &w); err != nil {
		t.Fatal(err)
	}
	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: WarmupJobName}, &w); !apierrors.IsNotFound(err) {
		t.Fatalf("a second warm-up within the hour: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: WarmupJobName}, &w); err != nil {
		t.Fatalf("no warm-up after the hour: %v", err)
	}
	// while it runs, a scan waits for it
	if err := c.Create(ctx, queuedScan("scan-x", time.Second, true)); err != nil {
		t.Fatal(err)
	}
	if err := q.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !suspended(t, c, "scan-x") {
		t.Fatal("a scan started while the warm-up holds the cache")
	}
}

func scanCreateFails(t *testing.T, b *v1alpha1.OdysseyBuild) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*batchv1.Job); ok {
				return errors.New("the API server is down")
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
}

// A scan that cannot be queued never blocks the build's cleanup: the token
// Secret goes, Retention applies, and the scan is retried soon (not at the
// build's own, much longer, requeue).
func TestFinishedCleansUpWhenTheScanCannotStart(t *testing.T) {
	ctx := context.Background()
	b := succeeded()
	fin := metav1.NewTime(time.Now())
	b.Status.FinishedAt = &fin
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: JobName(b), Namespace: Namespace}}
	c := scanCreateFails(t, b)
	if err := c.Create(ctx, sec); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, API: c, Config: scanCfg()}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("a scan that cannot start fails the build's reconcile: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(sec), &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the token Secret stays: %v", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > scanStartRetry {
		t.Fatalf("requeue %v: the scan is retried soon", res.RequeueAfter)
	}

	// Past the retention the build is deleted all the same.
	r.Now = func() time.Time { return time.Now().Add(cfg.withDefaults().Retention + time.Minute) }
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &v1alpha1.OdysseyBuild{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the build stays: %v", err)
	}
}

// Past scanStartWindow the scan is given up: its reason is on the build, the
// tunnel reports it (the control plane learns "never" at once) and nothing is
// retried.
func TestScanGivenUpIsRecordedOnTheBuild(t *testing.T) {
	ctx := context.Background()
	b := succeeded()
	fin := metav1.NewTime(time.Now().Add(-scanStartWindow - time.Minute))
	b.Status.FinishedAt = &fin
	c := scanCreateFails(t, b)
	r := &Reconciler{Client: c, API: c, Config: scanCfg()}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.OdysseyBuild
	if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	o, ok := ScanNotStarted(&got)
	if !ok || !o.Done || o.Digest != digest || !strings.Contains(o.Error, "could not be started") || !strings.Contains(o.Error, "API server is down") {
		t.Fatalf("outcome %+v %v", o, ok)
	}
	// and it is not tried again
	creates := 0
	got.ResourceVersion = ""
	c2 := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(&got).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			return fmt.Errorf("unexpected create of %T", obj)
		}}).Build()
	r2 := &Reconciler{Client: c2, API: c2, Config: scanCfg()}
	if _, err := r2.Reconcile(ctx, req); err != nil || creates != 0 {
		t.Fatalf("retried after giving up: %v, %d creates", err, creates)
	}
}
