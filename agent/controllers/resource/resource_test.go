package resource

import (
	"context"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const ns = "odyssey-env1"

func newRec(t *testing.T, cnpg bool, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(ClusterGVK, &unstructured.Unstructured{})
	mapper := meta.NewDefaultRESTMapper(nil)
	if cnpg {
		mapper.Add(ClusterGVK, meta.RESTScopeNamespace)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&v1alpha1.OdysseyResource{}, &appsv1.Deployment{})
	c := b.Build()
	return &Reconciler{Client: c, API: c, Mapper: mapper}, c
}

func res(name string, kind v1alpha1.ResourceKind, gen int64, storage string) *v1alpha1.OdysseyResource {
	return &v1alpha1.OdysseyResource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "uid-" + "x", Labels: map[string]string{"odyssey.dev/environment": "env1"}},
		Spec:       v1alpha1.OdysseyResourceSpec{Kind: kind, Generation: gen, Storage: storage},
	}
}

func defaultClass(expand bool, mode storagev1.VolumeBindingMode) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: "std", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}},
		AllowVolumeExpansion: &expand, VolumeBindingMode: &mode,
	}
}

func pass(t *testing.T, r *Reconciler, c client.Client, o *v1alpha1.OdysseyResource) (*v1alpha1.OdysseyResource, ctrl.Result) {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)})
	if err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.OdysseyResource
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(o), &got); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, res
		}
		t.Fatal(err)
	}
	return &got, res
}

func TestFilesClaimIsReadyWhenBoundOrWaitingForFirstConsumer(t *testing.T) {
	ctx := context.Background()
	o := res("docs", v1alpha1.ResourceFiles, 1, "2Gi")
	r, c := newRec(t, false, o, defaultClass(false, storagev1.VolumeBindingWaitForFirstConsumer))
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceReady || got.Status.ObservedGeneration != 1 {
		t.Fatalf("pending under WaitForFirstConsumer is ready: %+v", got.Status)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != Finalizer {
		t.Errorf("finalizer: %v", got.Finalizers)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "docs-files"}, &pvc); err != nil {
		t.Fatal(err)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "2Gi" || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce || !metav1.IsControlledBy(&pvc, got) {
		t.Errorf("pvc: %+v", pvc.Spec)
	}

	// Immediate binding: pending means provisioning, bound means ready.
	o2 := res("docs", v1alpha1.ResourceFiles, 1, "2Gi")
	r2, c2 := newRec(t, false, o2, defaultClass(false, storagev1.VolumeBindingImmediate))
	if got, _ := pass(t, r2, c2, o2); got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("pending: %+v", got.Status)
	}
	if err := c2.Get(ctx, client.ObjectKey{Namespace: ns, Name: "docs-files"}, &pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if err := c2.Status().Update(ctx, &pvc); err != nil {
		t.Fatal(err)
	}
	if got, _ := pass(t, r2, c2, o2); got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("bound: %+v", got.Status)
	}
}

func TestStorageOnlyGrows(t *testing.T) {
	ctx := context.Background()
	o := res("docs", v1alpha1.ResourceFiles, 1, "2Gi")
	r, c := newRec(t, false, o, defaultClass(true, storagev1.VolumeBindingWaitForFirstConsumer))
	pass(t, r, c, o)
	size := func() string {
		var pvc corev1.PersistentVolumeClaim
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "docs-files"}, &pvc); err != nil {
			t.Fatal(err)
		}
		q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		return q.String()
	}
	set := func(s string, gen int64) {
		var cur v1alpha1.OdysseyResource
		if err := c.Get(ctx, client.ObjectKeyFromObject(o), &cur); err != nil {
			t.Fatal(err)
		}
		cur.Spec.Storage, cur.Spec.Generation = s, gen
		if err := c.Update(ctx, &cur); err != nil {
			t.Fatal(err)
		}
	}
	set("5Gi", 2)
	if got, _ := pass(t, r, c, o); size() != "5Gi" || got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("grown: %s %+v", size(), got.Status)
	}
	set("1Gi", 3)
	if got, _ := pass(t, r, c, o); size() != "5Gi" || got.Status.Phase != v1alpha1.ResourceReady || got.Status.ObservedGeneration != 3 {
		t.Fatalf("shrinking is ignored: %s %+v", size(), got.Status)
	}

	// A class that cannot expand fails the resource instead of the API call.
	o2 := res("docs", v1alpha1.ResourceFiles, 1, "2Gi")
	r2, c2 := newRec(t, false, o2, defaultClass(false, storagev1.VolumeBindingWaitForFirstConsumer))
	pass(t, r2, c2, o2)
	var cur v1alpha1.OdysseyResource
	_ = c2.Get(ctx, client.ObjectKeyFromObject(o2), &cur)
	cur.Spec.Storage, cur.Spec.Generation = "5Gi", 2
	_ = c2.Update(ctx, &cur)
	if got, _ := pass(t, r2, c2, o2); got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "growing") {
		t.Fatalf("no expansion: %+v", got.Status)
	}
}

func TestRedisObjects(t *testing.T) {
	ctx := context.Background()
	o := res("cache", v1alpha1.ResourceRedis, 1, "1Gi")
	r, c := newRec(t, false, o, defaultClass(false, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("not ready until the Deployment is: %+v", got.Status)
	}
	key := client.ObjectKey{Namespace: ns, Name: "cache-redis"}
	var sec corev1.Secret
	if err := c.Get(ctx, key, &sec); err != nil {
		t.Fatal(err)
	}
	pw := string(sec.Data["password"])
	if len(pw) < 32 || string(sec.Data["url"]) != "redis://:"+pw+"@cache-redis."+ns+".svc:6379" {
		t.Fatalf("secret: %v", sec.Data)
	}
	var d appsv1.Deployment
	if err := c.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	pod := d.Spec.Template.Spec
	ct := pod.Containers[0]
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || ct.Image != RedisImage || !strings.Contains(RedisImage, "@sha256:") {
		t.Errorf("deployment: %+v", d.Spec.Strategy)
	}
	if *pod.SecurityContext.RunAsUser != 999 || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		!*ct.SecurityContext.ReadOnlyRootFilesystem || *ct.SecurityContext.AllowPrivilegeEscalation || ct.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("security: %+v %+v", pod.SecurityContext, ct.SecurityContext)
	}
	args := strings.Join(ct.Args, " ")
	if !strings.Contains(args, "--requirepass $(REDIS_PASSWORD)") || !strings.Contains(args, "--appendonly yes") || strings.Contains(args, pw) {
		t.Errorf("args: %s", args)
	}
	if ref := ct.Env[0].ValueFrom.SecretKeyRef; ref.Name != "cache-redis" || ref.Key != "password" || ct.ReadinessProbe == nil {
		t.Errorf("env: %+v", ct.Env)
	}
	var svc corev1.Service
	if err := c.Get(ctx, key, &svc); err != nil || svc.Spec.Ports[0].Port != 6379 {
		t.Errorf("service: %v %+v", err, svc.Spec)
	}
	if err := c.Get(ctx, key, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Errorf("pvc: %v", err)
	}

	// Ready once the Deployment has an available replica; the password is kept.
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, AvailableReplicas: 1}
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if got, _ := pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("ready: %+v", got.Status)
	}
	_ = c.Get(ctx, key, &sec)
	if string(sec.Data["password"]) != pw {
		t.Error("the password is made once")
	}
}

func clusterObj(status map[string]any) *unstructured.Unstructured {
	c := NewCluster()
	c.Object["status"] = status
	return c
}

func TestClusterState(t *testing.T) {
	for name, tc := range map[string]struct {
		status map[string]any
		phase  v1alpha1.ResourcePhase
	}{
		"condition":      {map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}, v1alpha1.ResourceReady},
		"ready instance": {map[string]any{"readyInstances": int64(1)}, v1alpha1.ResourceReady},
		"not yet":        {map[string]any{"phase": "Setting up primary", "readyInstances": int64(0)}, v1alpha1.ResourceProvisioning},
		"false":          {map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}}, v1alpha1.ResourceProvisioning},
	} {
		if got := clusterState(clusterObj(tc.status)); got.phase != tc.phase {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestPostgresCluster(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "3Gi")
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("created: %+v", got.Status)
	}
	cl := NewCluster()
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "main"}, cl); err != nil {
		t.Fatal(err)
	}
	size, _, _ := unstructured.NestedString(cl.Object, "spec", "storage", "size")
	inst, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	db, _, _ := unstructured.NestedString(cl.Object, "spec", "bootstrap", "initdb", "database")
	owner, _, _ := unstructured.NestedString(cl.Object, "spec", "bootstrap", "initdb", "owner")
	if size != "3Gi" || inst != 1 || db != "app" || owner != "app" || !metav1.IsControlledBy(cl, got) {
		t.Fatalf("cluster: %v", cl.Object)
	}

	// Growth patches storage.size; ready follows the Cluster.
	got.Spec.Storage, got.Spec.Generation = "5Gi", 2
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(cl.Object, int64(1), "status", "readyInstances")
	if err := c.Update(ctx, cl); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, o)
	_ = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "main"}, cl)
	size, _, _ = unstructured.NestedString(cl.Object, "spec", "storage", "size")
	if size != "5Gi" || got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("grown: %s %+v", size, got.Status)
	}
	got.Spec.Storage, got.Spec.Generation = "1Gi", 3
	_ = c.Update(ctx, got)
	pass(t, r, c, o)
	_ = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "main"}, cl)
	if size, _, _ = unstructured.NestedString(cl.Object, "spec", "storage", "size"); size != "5Gi" {
		t.Errorf("shrinking is ignored: %s", size)
	}
}

func TestPostgresNeedsCNPG(t *testing.T) {
	o := res("main", v1alpha1.ResourcePostgres, 1, "1Gi")
	r, c := newRec(t, false, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	if got, _ := pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceFailed || got.Status.Message != NoCNPGMessage {
		t.Fatalf("status: %+v", got.Status)
	}
}

func TestAForeignObjectInTheWayFailsTheResource(t *testing.T) {
	o := res("docs", v1alpha1.ResourceFiles, 1, "1Gi")
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "docs-files", Namespace: ns}}
	r, c := newRec(t, false, o, foreign, defaultClass(true, storagev1.VolumeBindingImmediate))
	if got, _ := pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "not managed by Odyssey") {
		t.Fatalf("status: %+v", got.Status)
	}
}

func TestDeleteFlowWaitsForTheReport(t *testing.T) {
	ctx := context.Background()
	o := res("cache", v1alpha1.ResourceRedis, 1, "1Gi")
	r, c := newRec(t, false, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	r.AwaitReport = true
	pass(t, r, c, o)
	key := client.ObjectKey{Namespace: ns, Name: "cache-redis"}
	if err := c.Get(ctx, key, &appsv1.Deployment{}); err != nil {
		t.Fatal(err)
	}

	var cur v1alpha1.OdysseyResource
	_ = c.Get(ctx, client.ObjectKeyFromObject(o), &cur)
	cur.Spec.Deleted, cur.Spec.Generation = true, 2
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	if got, _ := pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceProvisioning || got.Status.Message != "deleting" {
		t.Fatalf("the first pass deletes and waits: %+v", got.Status)
	}
	got, _ := pass(t, r, c, o)
	if got == nil || got.Status.Phase != v1alpha1.ResourceDeleted || got.Status.ObservedGeneration != 2 {
		t.Fatalf("deleted is reported before the object goes: %+v", got)
	}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &corev1.Secret{}, &corev1.PersistentVolumeClaim{}} {
		if err := c.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
			t.Errorf("%T must be deleted: %v", obj, err)
		}
	}
	// Still there: the tunnel has not said it told the control plane.
	if got, _ = pass(t, r, c, o); got == nil || got.Status.Phase != v1alpha1.ResourceDeleted {
		t.Fatalf("held for the report: %+v", got)
	}
	got.Annotations = map[string]string{AnnotationReported: "2"}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, _ = pass(t, r, c, o); got != nil {
		t.Fatalf("object removed after the report: %+v", got)
	}
}

func TestDeleteWithoutATunnelRemovesAtOnce(t *testing.T) {
	o := res("docs", v1alpha1.ResourceFiles, 1, "1Gi")
	r, c := newRec(t, false, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	pass(t, r, c, o)
	var cur v1alpha1.OdysseyResource
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(o), &cur)
	cur.Spec.Deleted, cur.Spec.Generation = true, 2
	_ = c.Update(context.Background(), &cur)
	pass(t, r, c, o)
	if got, _ := pass(t, r, c, o); got != nil {
		t.Fatalf("removed: %+v", got)
	}
}

func TestDeleteLeavesForeignObjectsAlone(t *testing.T) {
	o := res("docs", v1alpha1.ResourceFiles, 1, "1Gi")
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "docs-files", Namespace: ns}}
	r, c := newRec(t, false, o, foreign)
	o.Spec.Deleted = true
	_ = c.Update(context.Background(), o)
	pass(t, r, c, o)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Errorf("a claim Odyssey does not control must stay: %v", err)
	}
}

func TestDeletePostgresWaitsForTheClusterAndItsVolumes(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "1Gi")
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	pass(t, r, c, o)
	orphan := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "main-1", Namespace: ns, Labels: map[string]string{cnpgCluster: "main"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "main", UID: "u"}}}}
	if err := c.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	var cur v1alpha1.OdysseyResource
	_ = c.Get(ctx, client.ObjectKeyFromObject(o), &cur)
	cur.Spec.Deleted, cur.Spec.Generation = true, 2
	_ = c.Update(ctx, &cur)

	got, _ := pass(t, r, c, o) // deletes the Cluster
	if got == nil || got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("data still there: %+v", got)
	}
	got, _ = pass(t, r, c, o) // the Cluster is gone: its volumes go
	if got != nil && got.Status.Phase == v1alpha1.ResourceDeleted {
		t.Fatalf("not yet: the volume was just deleted")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Errorf("the volume must go: %v", err)
	}
	if got, _ = pass(t, r, c, o); got != nil {
		t.Fatalf("all gone: %+v", got)
	}
}

// terminating deletes o with the finalizer on it: the object stays.
func terminating(t *testing.T, c client.Client, o *v1alpha1.OdysseyResource) {
	t.Helper()
	var cur v1alpha1.OdysseyResource
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(o), &cur); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
}

// Data is erased only when the control plane says so: an OdysseyResource
// someone deleted by hand keeps its finalizer and its children, and says so;
// then spec.deleted (set by ApplyResource on the terminating object) runs the
// normal teardown.
func TestDeletedByHandKeepsTheDataUntilTheControlPlaneDeletes(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []v1alpha1.ResourceKind{v1alpha1.ResourceRedis, v1alpha1.ResourceFiles, v1alpha1.ResourcePostgres} {
		o := res("cache", kind, 1, "1Gi")
		r, c := newRec(t, kind == v1alpha1.ResourcePostgres, o, defaultClass(true, storagev1.VolumeBindingImmediate))
		pass(t, r, c, o)
		var pvc corev1.PersistentVolumeClaim
		claim := client.ObjectKey{Namespace: ns, Name: FilesClaim("cache")}
		if kind == v1alpha1.ResourceRedis {
			claim.Name = RedisName("cache")
		}
		if kind != v1alpha1.ResourcePostgres {
			if err := c.Get(ctx, claim, &pvc); err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
		}

		terminating(t, c, o)
		for i := 0; i < 2; i++ {
			got, res := pass(t, r, c, o)
			if got == nil || len(got.Finalizers) != 1 || got.DeletionTimestamp.IsZero() {
				t.Fatalf("%s: the object stays, terminating, with its finalizer: %+v", kind, got)
			}
			if got.Status.Phase != v1alpha1.ResourceFailed || got.Status.Message != KeptMessage || res.RequeueAfter == 0 {
				t.Fatalf("%s: failed with the kept-data message: %+v %+v", kind, got.Status, res)
			}
			if kind != v1alpha1.ResourcePostgres {
				if err := c.Get(ctx, claim, &corev1.PersistentVolumeClaim{}); err != nil {
					t.Fatalf("%s: the data must be kept: %v", kind, err)
				}
			}
		}
		if kind == v1alpha1.ResourceRedis {
			for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &corev1.Secret{}} {
				if err := c.Get(ctx, claim, obj); err != nil {
					t.Fatalf("%T must be kept: %v", obj, err)
				}
			}
		}

		// The control plane deletes it: updates are allowed while the finalizer holds the object.
		var cur v1alpha1.OdysseyResource
		_ = c.Get(ctx, client.ObjectKeyFromObject(o), &cur)
		cur.Spec.Deleted, cur.Spec.Generation = true, 2
		if err := c.Update(ctx, &cur); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for i := 0; i < 4; i++ {
			pass(t, r, c, o)
		}
		if got, _ := pass(t, r, c, o); got != nil {
			t.Fatalf("%s: erased and removed once the control plane asked: %+v", kind, got)
		}
		if kind != v1alpha1.ResourcePostgres {
			if err := c.Get(ctx, claim, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
				t.Fatalf("%s: the data must go: %v", kind, err)
			}
		}
	}
}

// Only a volume CloudNativePG made for the Cluster goes with it: a claim
// that merely carries the label (and one owned by another Cluster) stays.
func TestDeletePostgresOnlyDeletesVolumesOwnedByItsCluster(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "1Gi")
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	pass(t, r, c, o)
	owner := func(group, kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: group + "/v1", Kind: kind, Name: name, UID: "u"}}
	}
	claim := func(name string, refs []metav1.OwnerReference) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{cnpgCluster: "main"}, OwnerReferences: refs}}
	}
	own := claim("main-1", owner("postgresql.cnpg.io", "Cluster", "main"))
	stay := []*corev1.PersistentVolumeClaim{
		claim("labelled-only", nil),
		claim("other-cluster", owner("postgresql.cnpg.io", "Cluster", "other")),
		claim("other-kind", owner("postgresql.cnpg.io", "Pooler", "main")),
		claim("other-group", owner("example.com", "Cluster", "main")),
	}
	for _, p := range append([]*corev1.PersistentVolumeClaim{own}, stay...) {
		if err := c.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var cur v1alpha1.OdysseyResource
	_ = c.Get(ctx, client.ObjectKeyFromObject(o), &cur)
	cur.Spec.Deleted, cur.Spec.Generation = true, 2
	_ = c.Update(ctx, &cur)
	for i := 0; i < 4; i++ {
		pass(t, r, c, o)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(own), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Errorf("the Cluster's own volume must go: %v", err)
	}
	for _, p := range stay {
		if err := c.Get(ctx, client.ObjectKeyFromObject(p), &corev1.PersistentVolumeClaim{}); err != nil {
			t.Errorf("%s is not the Cluster's and must stay: %v", p.Name, err)
		}
	}
}
