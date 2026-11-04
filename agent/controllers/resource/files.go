package resource

import (
	"context"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// files keeps the claim an app mounts. It is ready when the claim is Bound
// or, under a class that binds on first use (WaitForFirstConsumer), Pending:
// the volume appears when the app's pod is scheduled.
func (r *Reconciler) files(ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error) {
	pvc, fail, err := r.claim(ctx, o, FilesClaim(o.Name))
	if err != nil {
		return observation{}, err
	}
	if fail != nil {
		return *fail, nil
	}
	return r.claimState(ctx, pvc)
}

// claimState says whether pvc can be mounted.
func (r *Reconciler) claimState(ctx context.Context, pvc *corev1.PersistentVolumeClaim) (observation, error) {
	switch pvc.Status.Phase {
	case corev1.ClaimBound:
		return ready(), nil
	case corev1.ClaimLost:
		return failed("the volume was lost"), nil
	}
	sc, err := r.storageClass(ctx, pvc.Spec.StorageClassName)
	if err != nil {
		return observation{}, err
	}
	switch {
	case sc == nil:
		return provisioning("waiting for a storage class (the cluster has no default one)"), nil
	case sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer:
		return ready(), nil
	}
	return provisioning("waiting for the volume to be provisioned"), nil
}
