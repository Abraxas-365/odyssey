package resource

import (
	"context"
	"fmt"

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
// Cluster <name> with one instance; CNPG makes the Secret <name>-app
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
		c.Object["spec"] = map[string]any{
			"instances": int64(1),
			"storage":   map[string]any{"size": o.Spec.Storage},
			"bootstrap": map[string]any{"initdb": map[string]any{"database": databaseName, "owner": databaseName}},
		}
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
