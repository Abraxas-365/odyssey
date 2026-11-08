package resource

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Databases are CloudNativePG Clusters, managed as unstructured objects (the
// CNPG Go module is not a dependency). A postgres resource <name> is the
// Cluster <name> as spec.postgres says (one instance with CNPG's image and
// no set resources when it says nothing); CNPG makes the Secret <name>-app
// (uri, host, port, dbname, user, password) and the Service <name>-rw.

// ClusterGVK is the CloudNativePG Cluster kind.
var ClusterGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}

// NewCluster is an empty CloudNativePG Cluster (to watch or read).
func NewCluster() *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(ClusterGVK)
	return c
}

// databaseName is the database and the owner role CNPG creates.
const databaseName = "app"

// clusterAvailable says whether the cluster serves the CNPG Cluster kind.
func (r *Reconciler) clusterAvailable() bool {
	m := r.Mapper
	if m == nil {
		m = r.Client.RESTMapper()
	}
	_, err := m.RESTMapping(ClusterGVK.GroupKind(), ClusterGVK.Version)
	return err == nil
}

// NoCNPGMessage is what a postgres resource reports where CloudNativePG is
// not installed.
const NoCNPGMessage = "this cluster has no CloudNativePG operator (postgresql.cnpg.io)"

func (r *Reconciler) postgres(ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error) {
	if !r.clusterAvailable() {
		return failed(NoCNPGMessage), nil
	}
	want, err := wantStorage(o)
	if err != nil {
		return failed("the storage size is not valid: " + o.Spec.Storage), nil
	}
	if msg := checkPostgres(o.Spec.Postgres); msg != "" {
		return failed(msg), nil
	}
	cur := NewCluster()
	err = r.API.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: o.Name}, cur)
	switch {
	case apierrors.IsNotFound(err):
		cur = nil
	case err != nil:
		return observation{}, err
	case !metav1.IsControlledBy(cur, o):
		return observation{}, &notOurs{"Cluster", o.Name}
	}
	if cur != nil {
		size, _, _ := unstructured.NestedString(cur.Object, "spec", "storage", "size")
		have, perr := resource.ParseQuantity(size)
		changed := false
		// Only grows. A size the live object does not state is left alone.
		if perr == nil && want.Cmp(have) > 0 {
			ok, err := r.canGrow(ctx, nil)
			if err != nil {
				return observation{}, err
			}
			if !ok {
				return failed(noGrowthMessage), nil
			}
			if err := unstructured.SetNestedField(cur.Object, o.Spec.Storage, "spec", "storage", "size"); err != nil {
				return observation{}, err
			}
			changed = true
		}
		// A database made before its pods carried the resource label gets it
		// now (the egress fence selects resource pods by it): CloudNativePG
		// puts it on the running pods without restarting them.
		have2, _, _ := unstructured.NestedStringMap(cur.Object, "spec", "inheritedMetadata", "labels")
		if have2[LabelResource] != o.Name {
			merged := map[string]any{}
			for k, v := range have2 {
				merged[k] = v
			}
			for k, v := range inheritedLabels(o) {
				merged[k] = v
			}
			if err := unstructured.SetNestedField(cur.Object, merged, "spec", "inheritedMetadata", "labels"); err != nil {
				return observation{}, err
			}
			changed = true
		}
		settled, msg, err := settlePostgres(cur, o.Spec.Postgres)
		if err != nil {
			return observation{}, err
		}
		if msg != "" {
			return failed(msg), nil
		}
		changed = changed || settled
		if changed {
			cur.SetLabels(mergeLabels(cur.GetLabels(), labels(o)))
			if err := r.Client.Update(ctx, cur); err != nil {
				return observation{}, err
			}
		}
	} else {
		c := NewCluster()
		c.SetName(o.Name)
		c.SetNamespace(o.Namespace)
		c.SetLabels(labels(o))
		spec := map[string]any{
			"instances": int64(1),
			// The pods carry the resource label too (the cluster's labels
			// above stay on the Cluster object only): the egress fence
			// selects resource pods by it (agent/egress odicea-resources).
			"inheritedMetadata": map[string]any{"labels": inheritedLabels(o)},
			"storage":           map[string]any{"size": o.Spec.Storage},
			"bootstrap":         map[string]any{"initdb": map[string]any{"database": databaseName, "owner": databaseName}},
		}
		if p := o.Spec.Postgres; p != nil {
			if p.Instances > 0 {
				spec["instances"] = int64(p.Instances)
			}
			if p.Major != 0 {
				spec["imageName"] = PostgresImages[p.Major]
			}
			if res := postgresResources(p); res != nil {
				spec["resources"] = res
			}
		}
		c.Object["spec"] = spec
		if err := controllerutil.SetControllerReference(o, c, r.Client.Scheme()); err != nil {
			return observation{}, err
		}
		if err := r.Client.Create(ctx, c); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return observation{}, errCacheBehind
			}
			return observation{}, err
		}
		return provisioning("creating the database"), nil
	}
	return clusterState(cur), nil
}

func mergeLabels(cur, want map[string]string) map[string]string {
	if cur == nil {
		cur = map[string]string{}
	}
	for k, v := range want {
		cur[k] = v
	}
	return cur
}

// clusterState reads how a CNPG Cluster is: ready with the Ready condition
// or one ready instance.
func clusterState(c *unstructured.Unstructured) observation {
	conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
	for _, x := range conds {
		m, _ := x.(map[string]any)
		if m["type"] == "Ready" && m["status"] == "True" {
			return ready()
		}
	}
	if n, found, _ := unstructured.NestedFieldNoCopy(c.Object, "status", "readyInstances"); found {
		switch v := n.(type) {
		case int64:
			if v >= 1 {
				return ready()
			}
		case float64:
			if v >= 1 {
				return ready()
			}
		}
	}
	msg := "setting up the database"
	if phase, _, _ := unstructured.NestedString(c.Object, "status", "phase"); phase != "" {
		msg = fmt.Sprintf("setting up the database: %s", phase)
	}
	return provisioning(msg)
}

// inheritedLabels are the labels CloudNativePG puts on every pod, PVC and
// Service it makes for the Cluster (spec.inheritedMetadata): only the
// resource's name. Not the managed-by mark: that one would put CloudNativePG's
// pods in the agent's cache, and teardown tells what is its own by ownership,
// not by label.
func inheritedLabels(o *v1alpha1.OdysseyResource) map[string]any {
	return map[string]any{LabelResource: o.Name}
}

// checkPostgres says what in the settings this agent cannot honour ("" when
// nothing): the control plane validated them, the CRD bounds them, but an
// object edited by hand reaches here too.
func checkPostgres(p *v1alpha1.PostgresSettings) string {
	if p == nil {
		return ""
	}
	if p.Instances < 0 || p.Instances > 5 {
		return fmt.Sprintf("a database has 1 to 5 instances, not %d", p.Instances)
	}
	if _, ok := PostgresImages[p.Major]; p.Major != 0 && !ok {
		return fmt.Sprintf("this agent has no image for PostgreSQL %d", p.Major)
	}
	for _, q := range []string{p.CPU, p.Memory} {
		if _, err := resource.ParseQuantity(q); q != "" && err != nil {
			return fmt.Sprintf("%q is not a Kubernetes quantity", q)
		}
	}
	return ""
}

// postgresResources are the Cluster's spec.resources for p (each value as
// request and limit), nil when p sets neither.
func postgresResources(p *v1alpha1.PostgresSettings) map[string]any {
	set := map[string]any{}
	if p.CPU != "" {
		set["cpu"] = p.CPU
	}
	if p.Memory != "" {
		set["memory"] = p.Memory
	}
	if len(set) == 0 {
		return nil
	}
	limits := map[string]any{}
	for k, v := range set {
		limits[k] = v
	}
	return map[string]any{"requests": set, "limits": limits}
}

// settlePostgres brings a running Cluster to what p asks: instances and
// resources follow it (CloudNativePG rolls the pods); the major version is
// fixed when the database is made, so a different one fails the resource
// (msg) instead of starting an upgrade nobody planned. A setting p leaves
// out is left as it is. changed says cur was modified.
func settlePostgres(cur *unstructured.Unstructured, p *v1alpha1.PostgresSettings) (changed bool, msg string, err error) {
	if p == nil {
		return false, "", nil
	}
	if p.Major != 0 {
		if have := runningMajor(cur); have != 0 && have != p.Major {
			return false, fmt.Sprintf("the database runs PostgreSQL %d and its recipe asks for %d: a major upgrade is a migration, not a setting", have, p.Major), nil
		}
	}
	if p.Instances > 0 {
		have, _, _ := unstructured.NestedInt64(cur.Object, "spec", "instances")
		if have != int64(p.Instances) {
			if err := unstructured.SetNestedField(cur.Object, int64(p.Instances), "spec", "instances"); err != nil {
				return false, "", err
			}
			changed = true
		}
	}
	// Each of cpu and memory, as request and limit, follows p when p sets it;
	// whatever else spec.resources holds (one p leaves out, a hand-set
	// ephemeral-storage) stays.
	for _, side := range []string{"requests", "limits"} {
		for k, v := range map[string]string{"cpu": p.CPU, "memory": p.Memory} {
			if v == "" {
				continue
			}
			if have, _, _ := unstructured.NestedString(cur.Object, "spec", "resources", side, k); have == v {
				continue
			}
			if err := unstructured.SetNestedField(cur.Object, v, "spec", "resources", side, k); err != nil {
				return false, "", err
			}
			changed = true
		}
	}
	return changed, "", nil
}

// runningMajor is the PostgreSQL major version a Cluster runs, read from
// its image ("…/postgresql:17.10-…"): the one it was made with, else the one
// CloudNativePG reports. 0 when neither says.
func runningMajor(c *unstructured.Unstructured) int32 {
	for _, path := range [][]string{{"spec", "imageName"}, {"status", "image"}} {
		image, _, _ := unstructured.NestedString(c.Object, path...)
		if m := majorOf(image); m != 0 {
			return m
		}
	}
	return 0
}

func majorOf(image string) int32 {
	image, _, _ = strings.Cut(image, "@")
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i:], "/") {
		return 0
	}
	tag := image[i+1:]
	end := strings.IndexFunc(tag, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(tag)
	}
	n, err := strconv.Atoi(tag[:end])
	if err != nil || n <= 0 || n > 100 {
		return 0
	}
	return int32(n)
}
