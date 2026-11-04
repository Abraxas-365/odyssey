package app

import (
	"context"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func resourceCR(name string, kind v1alpha1.ResourceKind, phase v1alpha1.ResourcePhase, msg string) *v1alpha1.OdysseyResource {
	return &v1alpha1.OdysseyResource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "odyssey-env1"},
		Spec:       v1alpha1.OdysseyResourceSpec{Kind: kind, Generation: 1, Storage: "1Gi"},
		Status:     v1alpha1.OdysseyResourceStatus{ObservedGeneration: 1, Phase: phase, Message: msg},
	}
}

func withResources(rev int64, refs ...v1alpha1.ResourceRefSpec) *v1alpha1.OdysseyApp {
	a := newApp(rev)
	a.Spec.Resources = refs
	return a
}

var allRefs = []v1alpha1.ResourceRefSpec{
	{Name: "main", Kind: v1alpha1.ResourcePostgres},
	{Name: "cache", Kind: v1alpha1.ResourceRedis, Prefix: "CACHE"},
	{Name: "docs", Kind: v1alpha1.ResourceFiles},
}

func TestResourcesGateTheRolloutThenFail(t *testing.T) {
	at := now
	a := withResources(1, allRefs...)
	r, c := secretsRec(t, esoMapper(), &at, a,
		resourceCR("main", v1alpha1.ResourcePostgres, v1alpha1.ResourceProvisioning, "setting up the database"))

	got, res := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseProgressing || res.RequeueAfter != progressEvery || deploymentOf(t, c, a) != nil {
		t.Fatalf("waiting: %+v %+v", got.Status, res)
	}
	// sorted by name: "cache" is missing and is the first to wait for.
	if !strings.Contains(got.Status.Message, "cache") || !strings.Contains(got.Status.Message, "does not exist") {
		t.Errorf("message: %q", got.Status.Message)
	}
	if !strings.HasSuffix(got.Annotations[annotationResourcesWaiting], "/"+at.UTC().Format(time.RFC3339)) {
		t.Errorf("the first moment is recorded: %v", got.Annotations)
	}

	at = at.Add(resourcekind.Wait - time.Second)
	if got, _ = runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("within the window: %+v", got.Status)
	}
	at = at.Add(2 * time.Second)
	got, res = runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonResource || res.RequeueAfter != 0 ||
		got.Status.Message != "resource cache is not ready: it does not exist in this environment" {
		t.Fatalf("after the window: %+v %+v", got.Status, res)
	}
	// A failed revision stays failed even if the resource turns up.
	for _, o := range []*v1alpha1.OdysseyResource{
		resourceCR("cache", v1alpha1.ResourceRedis, v1alpha1.ResourceReady, ""), resourceCR("docs", v1alpha1.ResourceFiles, v1alpha1.ResourceReady, ""),
	} {
		if err := c.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ = runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseFailed || deploymentOf(t, c, a) != nil {
		t.Fatalf("a failed revision stays failed: %+v", got.Status)
	}
}

func TestProvisioningResourceMessageIsReported(t *testing.T) {
	at := now
	a := withResources(1, allRefs[0])
	r, c := secretsRec(t, esoMapper(), &at, a, resourceCR("main", v1alpha1.ResourcePostgres, v1alpha1.ResourceProvisioning, "setting up the database"))
	runWith(t, r, c, a)
	at = at.Add(resourcekind.Wait)
	got, _ := runWith(t, r, c, a)
	if got.Status.Message != "resource main is not ready: setting up the database" {
		t.Fatalf("message: %+v", got.Status)
	}
}

func TestReadyResourcesWireTheDeployment(t *testing.T) {
	at := now
	a := withResources(1, allRefs...)
	a.Spec.Env = map[string]string{"DATABASE_URL": "plain", "A": "1"}
	r, c := secretsRec(t, esoMapper(), &at, a,
		resourceCR("main", v1alpha1.ResourcePostgres, v1alpha1.ResourceReady, ""),
		resourceCR("cache", v1alpha1.ResourceRedis, v1alpha1.ResourceReady, ""),
		resourceCR("docs", v1alpha1.ResourceFiles, v1alpha1.ResourceReady, ""))
	got, _ := runWith(t, r, c, a)
	d := deploymentOf(t, c, a)
	if d == nil {
		t.Fatalf("with its resources ready the Deployment is made: %+v", got.Status)
	}
	if _, ok := got.Annotations[annotationResourcesWaiting]; ok {
		t.Errorf("no wait recorded: %v", got.Annotations)
	}
	env := envOf(d)
	for name, want := range map[string][2]string{
		"DATABASE_URL": {"main-app", "uri"}, "PGHOST": {"main-app", "host"}, "PGPORT": {"main-app", "port"}, "PGDATABASE": {"main-app", "dbname"},
		"PGUSER": {"main-app", "user"}, "PGPASSWORD": {"main-app", "password"}, "CACHE_REDIS_URL": {"cache-redis", "url"},
	} {
		ref := env[name].ValueFrom
		if ref == nil || ref.SecretKeyRef.Name != want[0] || ref.SecretKeyRef.Key != want[1] || env[name].Value != "" {
			t.Errorf("%s: %+v", name, env[name])
		}
	}
	if env["FILES_DIR"].Value != resourcekind.MountPath("docs") || env["A"].Value != "1" {
		t.Errorf("env: %+v", env)
	}
	// Order: resourcekind.Variables for main, cache, ... and the plain env loses to the resource.
	var order []string
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "DATABASE_URL" {
			order = append(order, "once")
		}
	}
	if len(order) != 1 {
		t.Errorf("DATABASE_URL must appear once: %v", d.Spec.Template.Spec.Containers[0].Env)
	}
	vars := resourcekind.Variables(resourcekind.Postgres, "")
	var seen []string
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		for _, v := range vars {
			if e.Name == v {
				seen = append(seen, e.Name)
			}
		}
	}
	if strings.Join(seen, ",") != strings.Join(vars, ",") {
		t.Errorf("postgres variable order: %v vs %v", seen, vars)
	}

	pod := d.Spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].PersistentVolumeClaim.ClaimName != "docs-files" ||
		pod.Containers[0].VolumeMounts[0].MountPath != resourcekind.MountPath("docs") {
		t.Errorf("volume: %+v %+v", pod.Volumes, pod.Containers[0].VolumeMounts)
	}
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || d.Spec.Strategy.RollingUpdate != nil {
		t.Errorf("files means Recreate: %+v", d.Spec.Strategy)
	}
	if pod.SecurityContext.FSGroup == nil {
		t.Error("the volume must be writable by a non-root user")
	}
	if d.Spec.Template.Annotations[AnnotationResources] == "" {
		t.Errorf("resources roll the pods: %v", d.Spec.Template.Annotations)
	}
}

func TestResourceChangesRollThePodsAndNoFilesKeepsRolling(t *testing.T) {
	a := withResources(1, allRefs[1])
	d := &appsv1.Deployment{}
	mutateDeployment(d, a, cfg)
	before := d.Spec.Template.Annotations[AnnotationResources]
	if d.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("without files the rollout is rolling: %+v", d.Spec.Strategy)
	}
	a.Spec.Resources[0].Prefix = "OTHER"
	mutateDeployment(d, a, cfg)
	if after := d.Spec.Template.Annotations[AnnotationResources]; before == "" || after == before {
		t.Errorf("hash: %q -> %q", before, after)
	}
	plain := newApp(1)
	mutateDeployment(d, plain, cfg)
	if _, ok := d.Spec.Template.Annotations[AnnotationResources]; ok || len(d.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("apps without resources keep their template: %v", d.Spec.Template.Annotations)
	}
}

func TestResourceThatIsBeingDeletedOrOfAnotherKindWaits(t *testing.T) {
	at := now
	gone := resourceCR("main", v1alpha1.ResourcePostgres, v1alpha1.ResourceReady, "")
	gone.Spec.Deleted = true
	a := withResources(1, allRefs[0])
	r, c := secretsRec(t, esoMapper(), &at, a, gone)
	if got, _ := runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseProgressing || !strings.Contains(got.Status.Message, "being deleted") {
		t.Fatalf("deleted: %+v", got.Status)
	}
	other := resourceCR("main", v1alpha1.ResourceRedis, v1alpha1.ResourceReady, "")
	a2 := withResources(1, allRefs[0])
	r2, c2 := secretsRec(t, esoMapper(), &at, a2, other)
	if got, _ := runWith(t, r2, c2, a2); !strings.Contains(got.Status.Message, "it is a redis, not a postgres") {
		t.Fatalf("kind: %+v", got.Status)
	}
	// A stale status (the spec moved on) is not ready.
	stale := resourceCR("main", v1alpha1.ResourcePostgres, v1alpha1.ResourceReady, "")
	stale.Spec.Generation = 2
	a3 := withResources(1, allRefs[0])
	r3, c3 := secretsRec(t, esoMapper(), &at, a3, stale)
	if got, _ := runWith(t, r3, c3, a3); got.Status.Phase != v1alpha1.PhaseProgressing || deploymentOf(t, c3, a3) != nil {
		t.Fatalf("stale: %+v", got.Status)
	}
}

func TestAppsWithoutResourcesForgetAWait(t *testing.T) {
	at := now
	a := withResources(1, allRefs[0])
	r, c := secretsRec(t, esoMapper(), &at, a)
	runWith(t, r, c, a)
	var cur v1alpha1.OdysseyApp
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(a), &cur)
	cur.Spec.Resources, cur.Spec.Revision = nil, 2
	if err := c.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	got, _ := runWith(t, r, c, a)
	if _, ok := got.Annotations[annotationResourcesWaiting]; ok || deploymentOf(t, c, a) == nil {
		t.Fatalf("annotation %v, deployment %v", got.Annotations, deploymentOf(t, c, a))
	}
}
