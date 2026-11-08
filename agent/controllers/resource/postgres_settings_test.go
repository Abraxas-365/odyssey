package resource

import (
	"context"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEveryPinnedMajorHasAnImageByDigest(t *testing.T) {
	if len(PostgresImages) != len(resourcespec.Majors) {
		t.Errorf("images %v for majors %v", PostgresImages, resourcespec.Majors)
	}
	for _, m := range resourcespec.Majors {
		img, ok := PostgresImages[int32(m)]
		if !ok || !strings.Contains(img, "@sha256:") || majorOf(img) != int32(m) {
			t.Errorf("PostgreSQL %d: %q", m, img)
		}
	}
}

func TestMajorOf(t *testing.T) {
	for img, want := range map[string]int32{
		"ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm@sha256:ab": 17,
		"ghcr.io/cloudnative-pg/postgresql:16":                                16,
		"registry:5000/postgresql:18.4":                                       18,
		"registry:5000/postgresql":                                            0,
		"":                                                                    0,
		"postgresql:latest":                                                   0,
	} {
		if got := majorOf(img); got != want {
			t.Errorf("%q: %d, want %d", img, got, want)
		}
	}
}

func produccion() *v1alpha1.PostgresSettings {
	return &v1alpha1.PostgresSettings{Instances: 2, Major: 17, CPU: "1", Memory: "2Gi"}
}

func cluster(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	cl := NewCluster()
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "main"}, cl); err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestPostgresRecipeSettingsMakeTheCluster(t *testing.T) {
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	o.Spec.Recipe, o.Spec.Postgres = "postgres-produccion@1", produccion()
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	if got, _ := pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("created: %+v", got.Status)
	}
	cl := cluster(t, c)
	inst, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	image, _, _ := unstructured.NestedString(cl.Object, "spec", "imageName")
	req, _, _ := unstructured.NestedStringMap(cl.Object, "spec", "resources", "requests")
	lim, _, _ := unstructured.NestedStringMap(cl.Object, "spec", "resources", "limits")
	if inst != 2 || image != PostgresImages[17] || req["cpu"] != "1" || req["memory"] != "2Gi" || lim["cpu"] != "1" || lim["memory"] != "2Gi" {
		t.Fatalf("cluster spec: %v", cl.Object["spec"])
	}
}

func TestPostgresWithoutSettingsKeepsTheBasicCluster(t *testing.T) {
	o := res("main", v1alpha1.ResourcePostgres, 1, "1Gi")
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	pass(t, r, c, o)
	spec, _, _ := unstructured.NestedMap(cluster(t, c).Object, "spec")
	if _, ok := spec["imageName"]; ok {
		t.Errorf("a basic database runs CloudNativePG's image: %v", spec)
	}
	if _, ok := spec["resources"]; ok {
		t.Errorf("a basic database sets no resources: %v", spec)
	}
}

func TestPostgresFollowsInstancesAndResources(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	o.Spec.Postgres = produccion()
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)

	got.Spec.Generation = 2
	got.Spec.Postgres = &v1alpha1.PostgresSettings{Instances: 3, Major: 17, CPU: "2", Memory: "4Gi"}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	pass(t, r, c, o)
	cl := cluster(t, c)
	inst, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	lim, _, _ := unstructured.NestedStringMap(cl.Object, "spec", "resources", "limits")
	image, _, _ := unstructured.NestedString(cl.Object, "spec", "imageName")
	if inst != 3 || lim["cpu"] != "2" || lim["memory"] != "4Gi" || image != PostgresImages[17] {
		t.Fatalf("settled: %v", cl.Object["spec"])
	}
}

func TestPostgresNeverChangesItsMajorVersion(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	o.Spec.Postgres = produccion()
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)

	got.Spec.Generation = 2
	got.Spec.Postgres = &v1alpha1.PostgresSettings{Instances: 4, Major: 18}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "PostgreSQL 17") {
		t.Fatalf("a different major fails the resource: %+v", got.Status)
	}
	cl := cluster(t, c)
	inst, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	image, _, _ := unstructured.NestedString(cl.Object, "spec", "imageName")
	if inst != 2 || image != PostgresImages[17] {
		t.Errorf("nothing changes with a refused major: %v", cl.Object["spec"])
	}
}

func TestPostgresGrowthWaitsForTheMajorToBeSettled(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	o.Spec.Postgres = produccion()
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)

	// A refused major holds back the whole change, growth included...
	got.Spec.Generation, got.Spec.Storage = 2, "20Gi"
	got.Spec.Postgres = &v1alpha1.PostgresSettings{Instances: 2, Major: 18}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, _ = pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceFailed {
		t.Fatalf("status: %+v", got.Status)
	}
	if size, _, _ := unstructured.NestedString(cluster(t, c).Object, "spec", "storage", "size"); size != "10Gi" {
		t.Fatalf("held back: %s", size)
	}
	// ...and goes through once the spec asks for the running major again.
	got.Spec.Generation, got.Spec.Postgres = 3, produccion()
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, _ = pass(t, r, c, o); got.Status.Phase == v1alpha1.ResourceFailed {
		t.Fatalf("status: %+v", got.Status)
	}
	if size, _, _ := unstructured.NestedString(cluster(t, c).Object, "spec", "storage", "size"); size != "20Gi" {
		t.Errorf("grown: %s", size)
	}
}

func TestPostgresResourcesFollowOnlyWhatTheSettingsSay(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	o.Spec.Postgres = produccion()
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)

	// Something IT set by hand on the Cluster stays.
	cl := cluster(t, c)
	_ = unstructured.SetNestedField(cl.Object, "1Gi", "spec", "resources", "limits", "ephemeral-storage")
	if err := c.Update(ctx, cl); err != nil {
		t.Fatal(err)
	}
	// Settings that name only cpu leave memory as it is.
	got.Spec.Generation = 2
	got.Spec.Postgres = &v1alpha1.PostgresSettings{Instances: 2, Major: 17, CPU: "2"}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	pass(t, r, c, o)
	cl = cluster(t, c)
	req, _, _ := unstructured.NestedStringMap(cl.Object, "spec", "resources", "requests")
	lim, _, _ := unstructured.NestedStringMap(cl.Object, "spec", "resources", "limits")
	if req["cpu"] != "2" || lim["cpu"] != "2" || req["memory"] != "2Gi" || lim["memory"] != "2Gi" || lim["ephemeral-storage"] != "1Gi" {
		t.Errorf("resources: %v / %v", req, lim)
	}
}

func TestPostgresMadeBasicTakesSettingsButNotAnUnknownMajor(t *testing.T) {
	ctx := context.Background()
	o := res("main", v1alpha1.ResourcePostgres, 1, "10Gi")
	r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
	got, _ := pass(t, r, c, o)

	// A basic database runs CloudNativePG's default image, which says no
	// major in the Cluster's spec: the agent does not guess one and leaves
	// the image alone, but instances and resources follow.
	got.Spec.Generation = 2
	got.Spec.Postgres = produccion()
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, o)
	if got.Status.Phase == v1alpha1.ResourceFailed {
		t.Fatalf("status: %+v", got.Status)
	}
	cl := cluster(t, c)
	inst, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	if _, ok, _ := unstructured.NestedString(cl.Object, "spec", "imageName"); ok || inst != 2 {
		t.Errorf("spec: %v", cl.Object["spec"])
	}

	// Once CloudNativePG reports a different major, it is refused.
	_ = unstructured.SetNestedField(cl.Object, "ghcr.io/cloudnative-pg/postgresql:16.4", "status", "image")
	if err := c.Update(ctx, cl); err != nil {
		t.Fatal(err)
	}
	got.Spec.Generation = 3
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, _ = pass(t, r, c, o); got.Status.Phase != v1alpha1.ResourceFailed {
		t.Errorf("16 running, 17 asked: %+v", got.Status)
	}
}

func TestPostgresRefusesSettingsItCannotHonour(t *testing.T) {
	for name, p := range map[string]*v1alpha1.PostgresSettings{
		"major":    {Major: 15},
		"quantity": {CPU: "lots"},
		"replicas": {Instances: 9},
	} {
		o := res("main", v1alpha1.ResourcePostgres, 1, "1Gi")
		o.Spec.Postgres = p
		r, c := newRec(t, true, o, defaultClass(true, storagev1.VolumeBindingImmediate))
		got, _ := pass(t, r, c, o)
		if got.Status.Phase != v1alpha1.ResourceFailed {
			t.Errorf("%s: %+v", name, got.Status)
		}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "main"}, NewCluster()); err == nil {
			t.Errorf("%s: a Cluster was made", name)
		}
	}
}
