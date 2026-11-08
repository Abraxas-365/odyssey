package resource

import (
	"context"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// cnpgCluster is the label CloudNativePG puts on a Cluster's pods and volumes.
const cnpgCluster = "cnpg.io/cluster"

// KeptMessage is the failure reported for an OdysseyResource deleted in the
// cluster by hand: its data is kept until Odicea deletes the resource.
const KeptMessage = "the OdysseyResource was deleted in the cluster by hand; its data is kept until it is deleted from Odicea"

// keep handles an OdysseyResource that is being deleted without the control
// plane having asked (kubectl delete, a namespace going away). The agent
// never erases data on its own: the finalizer stays, so the owner (and with
// it the children, whose owner reference points to it) remains, and the
// resource is reported failed. The control plane's deleted=true (ApplyResource
// sets spec.deleted on the terminating object) then runs the normal teardown.
func (r *Reconciler) keep(ctx context.Context, o *v1alpha1.OdysseyResource) (ctrl.Result, error) {
	if err := r.report(ctx, o, failed(KeptMessage)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: failedEvery}, nil
}

// teardown removes what the resource made and everything it kept (the data),
// waits until it is gone, reports phase deleted and, once the control plane
// has been told (the tunnel marks it), removes the finalizer and the object.
// It runs only when the control plane asked (spec.deleted); an object deleted
// by hand is kept (see keep).
func (r *Reconciler) teardown(ctx context.Context, o *v1alpha1.OdysseyResource) (ctrl.Result, error) {
	gone, err := r.deleteChildren(ctx, o)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !gone {
		if err := r.report(ctx, o, provisioning("deleting")); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: workEvery}, nil
	}
	if o.Status.Phase != v1alpha1.ResourceDeleted || o.Status.ObservedGeneration != o.Spec.Generation {
		if err := r.report(ctx, o, observation{phase: v1alpha1.ResourceDeleted}); err != nil {
			return ctrl.Result{}, err
		}
	}
	if r.AwaitReport && o.Annotations[AnnotationReported] != generation(o) {
		// The tunnel marks it after sending (or on a safety resync).
		return ctrl.Result{RequeueAfter: failedEvery}, nil
	}
	return ctrl.Result{}, r.remove(ctx, o)
}

// remove lets go of o and deletes it.
func (r *Reconciler) remove(ctx context.Context, o *v1alpha1.OdysseyResource) error {
	if controllerutil.RemoveFinalizer(o, Finalizer) {
		if err := r.Client.Update(ctx, o); err != nil {
			return client.IgnoreNotFound(err)
		}
	}
	if o.DeletionTimestamp.IsZero() {
		return client.IgnoreNotFound(r.Client.Delete(ctx, o))
	}
	return nil
}

// deleteChildren deletes everything o made (whatever its kind: a spec
// cannot change kind) and says whether nothing is left. Objects o does not
// control are never touched.
func (r *Reconciler) deleteChildren(ctx context.Context, o *v1alpha1.OdysseyResource) (bool, error) {
	gone := true
	del := func(obj client.Object) error {
		err := r.API.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case !metav1.IsControlledBy(obj, o):
			return nil
		}
		gone = false
		if obj.GetDeletionTimestamp() != nil {
			return nil
		}
		return client.IgnoreNotFound(r.Client.Delete(ctx, obj, client.PropagationPolicy(metav1.DeletePropagationBackground)))
	}
	e, ok := engines[o.Spec.Kind]
	if !ok {
		return gone, nil // an unknown kind made nothing
	}
	pending, err := e.remove(r, ctx, o, del)
	if err != nil {
		return false, err
	}
	return gone && !pending, nil
}

// objectKey is the name of a child of o.
func objectKey(o *v1alpha1.OdysseyResource, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: o.Namespace}
}

func (r *Reconciler) removeRedis(_ context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (bool, error) {
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: objectKey(o, RedisName(o.Name))},
		&corev1.Service{ObjectMeta: objectKey(o, RedisName(o.Name))},
		&corev1.Secret{ObjectMeta: objectKey(o, RedisName(o.Name))},
		&corev1.PersistentVolumeClaim{ObjectMeta: objectKey(o, RedisName(o.Name))},
	} {
		if err := del(obj); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *Reconciler) removeFiles(_ context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (bool, error) {
	return false, del(&corev1.PersistentVolumeClaim{ObjectMeta: objectKey(o, FilesClaim(o.Name))})
}

func (r *Reconciler) removePostgres(ctx context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (bool, error) {
	if !r.clusterAvailable() {
		return false, nil // nothing could have been made
	}
	c := NewCluster()
	c.SetName(o.Name)
	c.SetNamespace(o.Namespace)
	if err := del(c); err != nil {
		return false, err
	}
	// CNPG's volumes go with the Cluster by owner reference, a moment
	// later: the data is gone when they are. Once the Cluster is gone,
	// a volume still carrying its label is deleted here, but only one
	// that CloudNativePG made for this Cluster (owned by it): never an
	// arbitrary labelled claim.
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.API.List(ctx, &pvcs, client.InNamespace(o.Namespace), client.MatchingLabels{cnpgCluster: o.Name}); err != nil {
		return false, err
	}
	pending := false
	for i := range pvcs.Items {
		p := &pvcs.Items[i]
		if !ownedByCluster(p, o.Name) {
			continue
		}
		pending = true
		if p.DeletionTimestamp == nil && clusterGone(ctx, r.API, c) {
			if err := r.Client.Delete(ctx, p); client.IgnoreNotFound(err) != nil {
				return false, err
			}
		}
	}
	return pending, nil
}

// ownedByCluster says p is owned by the CloudNativePG Cluster named name.
func ownedByCluster(p *corev1.PersistentVolumeClaim, name string) bool {
	for _, ref := range p.OwnerReferences {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err == nil && gv.Group == ClusterGVK.Group && ref.Kind == ClusterGVK.Kind && ref.Name == name {
			return true
		}
	}
	return false
}

// clusterGone says the Cluster does not exist (a read that fails says no).
func clusterGone(ctx context.Context, api client.Reader, c *unstructured.Unstructured) bool {
	return apierrors.IsNotFound(api.Get(ctx, client.ObjectKeyFromObject(c), c.DeepCopy()))
}
