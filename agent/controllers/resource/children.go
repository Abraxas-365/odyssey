package resource

import (
	"context"
	"errors"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// errCacheBehind: the object exists but the agent's cache has not seen it
// yet; the next pass finds it.
var errCacheBehind = errors.New("the object exists but is not in the cache yet")

// ensure creates or updates one of the objects o makes. An existing object
// o does not control is never touched (*notOurs). mutate sets the wanted
// state; for an existing object (ResourceVersion set) it must change only
// what may change.
func (r *Reconciler) ensure(ctx context.Context, o *v1alpha1.OdysseyResource, obj client.Object, kind string, mutate func()) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if obj.GetResourceVersion() != "" && !metav1.IsControlledBy(obj, o) {
			return &notOurs{kind, obj.GetName()}
		}
		mutate()
		return controllerutil.SetControllerReference(o, obj, r.Client.Scheme())
	})
	if apierrors.IsAlreadyExists(err) {
		live := obj.DeepCopyObject().(client.Object)
		if gerr := r.API.Get(ctx, client.ObjectKeyFromObject(obj), live); gerr == nil && !metav1.IsControlledBy(live, o) {
			return &notOurs{kind, obj.GetName()}
		}
		return errCacheBehind
	}
	return err
}

// storageClass is the class named (nil: the cluster's default), or nil when
// there is none.
func (r *Reconciler) storageClass(ctx context.Context, name *string) (*storagev1.StorageClass, error) {
	var list storagev1.StorageClassList
	if err := r.API.List(ctx, &list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		c := &list.Items[i]
		switch {
		case name != nil && *name != "" && c.Name == *name:
			return c, nil
		case (name == nil || *name == "") && c.Annotations["storageclass.kubernetes.io/is-default-class"] == "true":
			return c, nil
		}
	}
	return nil, nil
}

// canGrow says the class (nil name: the default) lets volumes grow.
func (r *Reconciler) canGrow(ctx context.Context, class *string) (bool, error) {
	sc, err := r.storageClass(ctx, class)
	if err != nil || sc == nil {
		return false, err
	}
	return sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion, nil
}

const noGrowthMessage = "the storage class does not allow growing volumes"

// wantStorage is the size the spec asks for.
func wantStorage(o *v1alpha1.OdysseyResource) (resource.Quantity, error) {
	return resource.ParseQuantity(o.Spec.Storage)
}

// claim keeps the PersistentVolumeClaim name for o: created with the spec's
// size, grown (never shrunk) when the spec asks for more. The result is the
// claim as the API holds it, nil with a failure when it cannot grow.
func (r *Reconciler) claim(ctx context.Context, o *v1alpha1.OdysseyResource, name string) (*corev1.PersistentVolumeClaim, *observation, error) {
	want, err := wantStorage(o)
	if err != nil {
		f := failed("the storage size is not valid: " + o.Spec.Storage)
		return nil, &f, nil
	}
	var live corev1.PersistentVolumeClaim
	switch err := r.API.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: name}, &live); {
	case err == nil:
		if metav1.IsControlledBy(&live, o) {
			if cur := live.Spec.Resources.Requests[corev1.ResourceStorage]; want.Cmp(cur) > 0 {
				ok, err := r.canGrow(ctx, live.Spec.StorageClassName)
				if err != nil {
					return nil, nil, err
				}
				if !ok {
					f := failed(noGrowthMessage)
					return &live, &f, nil
				}
			}
		}
	case !apierrors.IsNotFound(err):
		return nil, nil, err
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: o.Namespace}}
	err = r.ensure(ctx, o, pvc, "PersistentVolumeClaim", func() {
		pvc.Labels = labels(o)
		if pvc.ResourceVersion == "" {
			pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			pvc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: want}
			return
		}
		// A claim's spec is immutable but for its size, and that only grows.
		if cur := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; want.Cmp(cur) > 0 {
			pvc.Spec.Resources.Requests[corev1.ResourceStorage] = want
		}
	})
	return pvc, nil, err
}
