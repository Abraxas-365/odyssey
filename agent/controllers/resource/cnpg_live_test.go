package resource

import (
	"context"
	"os"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestRecipeSettingsOnRealCloudNativePG runs the reconciler against a real
// CloudNativePG (ODYSSEY_TEST_CNPG_KUBECONFIG: a throwaway cluster with the
// agent's CRDs and CNPG installed; it creates and deletes one namespace).
// What a fake client cannot tell: that CNPG accepts the Cluster as the agent
// writes it (pinned image by digest, instances, resources), runs that many
// pods of that major, and takes a later change of instances.
func TestRecipeSettingsOnRealCloudNativePG(t *testing.T) {
	path := os.Getenv("ODYSSEY_TEST_CNPG_KUBECONFIG")
	if path == "" {
		t.Skip("set ODYSSEY_TEST_CNPG_KUBECONFIG to a throwaway cluster with CloudNativePG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, API: c}

	space := "odyssey-cnpg-" + time.Now().Format("150405")
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: space}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		var o v1alpha1.OdysseyResource
		if c.Get(bg, client.ObjectKey{Namespace: space, Name: "main"}, &o) == nil {
			o.Finalizers = nil
			_ = c.Update(bg, &o)
		}
		_ = c.Delete(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: space}})
	})

	o := &v1alpha1.OdysseyResource{
		ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: space, Labels: map[string]string{"odyssey.dev/environment": "env1"}},
		Spec: v1alpha1.OdysseyResourceSpec{Kind: v1alpha1.ResourcePostgres, Generation: 1, Storage: "1Gi", Recipe: "postgres-produccion@1",
			Postgres: &v1alpha1.PostgresSettings{Instances: 2, Major: 17, CPU: "250m", Memory: "512Mi"}},
	}
	if err := c.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	// until reports the resource's phase after a pass, until want (or the deadline).
	until := func(want v1alpha1.ResourcePhase, why string) *v1alpha1.OdysseyResource {
		t.Helper()
		for {
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)}); err != nil && !apierrors.IsConflict(err) {
				t.Fatal(err)
			}
			var got v1alpha1.OdysseyResource
			if err := c.Get(ctx, client.ObjectKeyFromObject(o), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase == want && got.Status.ObservedGeneration == got.Spec.Generation {
				return &got
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: still %+v", why, got.Status)
			case <-time.After(5 * time.Second):
			}
		}
	}
	cnpg := func() *unstructured.Unstructured {
		t.Helper()
		cl := NewCluster()
		if err := c.Get(ctx, client.ObjectKey{Namespace: space, Name: "main"}, cl); err != nil {
			t.Fatal(err)
		}
		return cl
	}
	readyPods := func(want int) {
		t.Helper()
		for {
			cl := cnpg()
			n, _, _ := unstructured.NestedInt64(cl.Object, "status", "readyInstances")
			if int(n) == want {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("ready instances %d, want %d: %v", n, want, cl.Object["status"])
			case <-time.After(5 * time.Second):
			}
		}
	}

	got := until(v1alpha1.ResourceReady, "the database comes up")
	readyPods(2)
	cl := cnpg()
	image, _, _ := unstructured.NestedString(cl.Object, "status", "image")
	if image != PostgresImages[17] {
		t.Errorf("running image %q, want the pinned %q", image, PostgresImages[17])
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(space), client.MatchingLabels{"cnpg.io/cluster": "main"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		for _, k := range p.Spec.Containers {
			if k.Name != "postgres" {
				continue
			}
			if k.Resources.Limits.Cpu().String() != "250m" || k.Resources.Requests.Memory().String() != "512Mi" {
				t.Errorf("pod %s resources %+v", p.Name, k.Resources)
			}
		}
		if p.Labels[LabelResource] != "main" {
			t.Errorf("pod %s lacks the resource label: %v", p.Name, p.Labels)
		}
	}

	// A change of instances goes through; asking for another major does not.
	got.Spec.Generation, got.Spec.Postgres.Instances = 2, 1
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got = until(v1alpha1.ResourceReady, "fewer instances")
	readyPods(1)
	got.Spec.Generation, got.Spec.Postgres.Major = 3, 18
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got = until(v1alpha1.ResourceFailed, "another major is refused")
	if image, _, _ := unstructured.NestedString(cnpg().Object, "spec", "imageName"); image != PostgresImages[17] {
		t.Errorf("the major changed: %q", image)
	}

	// Deleting removes the Cluster.
	got.Spec.Generation, got.Spec.Deleted = 4, true
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)}); err != nil && !apierrors.IsConflict(err) {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: space, Name: "main"}, NewCluster()); apierrors.IsNotFound(err) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the Cluster was not deleted")
		case <-time.After(3 * time.Second):
		}
	}
}
