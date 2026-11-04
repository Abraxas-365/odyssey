package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resources (phase 2b): the environment's databases, caches and volumes an
// app uses (OdysseyApp.spec.resources). The agent's resource controller
// makes them (agent/controllers/resource) and keeps their connection in
// Secrets; the pod reads those by secretKeyRef, never a value from the spec.
// Files are claims the pod mounts.

const (
	// AnnotationResources is on the pod template: a hash of the app's
	// resource references, so connecting or disconnecting one rolls the pods.
	AnnotationResources = "odicea.dev/resources"
	// annotationResourcesWaiting is on the OdysseyApp: "<revision>/<generation>/<RFC 3339>",
	// when that revision first waited for a resource (it survives restarts).
	annotationResourcesWaiting = "odicea.dev/resources-waiting"

	// filesGroup is the group the pod's volumes are made writable for (a
	// supplementary group of every process, so an app running as any
	// non-root user can write its files).
	filesGroup = int64(65532)
)

// sortedResources are a's resource references by name.
func sortedResources(a *v1alpha1.OdysseyApp) []v1alpha1.ResourceRefSpec {
	out := slices.Clone(a.Spec.Resources)
	slices.SortFunc(out, func(x, y v1alpha1.ResourceRefSpec) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// hasFiles says a uses a files resource (a ReadWriteOnce volume: its pods
// are replaced, never run side by side).
func hasFiles(a *v1alpha1.OdysseyApp) bool {
	return slices.ContainsFunc(a.Spec.Resources, func(r v1alpha1.ResourceRefSpec) bool { return r.Kind == v1alpha1.ResourceFiles })
}

// resourceEnv are the variables the pod reads its resources by, in the
// order of resourcekind.Variables for each resource (by name).
func resourceEnv(a *v1alpha1.OdysseyApp) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, r := range sortedResources(a) {
		k := resourcekind.Kind(r.Kind)
		names := resourcekind.Variables(k, r.Prefix)
		if k == resourcekind.Files {
			out = append(out, corev1.EnvVar{Name: names[0], Value: resourcekind.MountPath(r.Name)})
			continue
		}
		secret, keys := resource.Connection(k, r.Name)
		for i, n := range names {
			out = append(out, corev1.EnvVar{Name: n, ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: keys[i]},
			}})
		}
	}
	return out
}

// volumeName is the pod volume of files resource name.
func volumeName(name string) string { return "res-" + name }

// resourceVolumes are the pod's volumes and the container's mounts for the
// files resources.
func resourceVolumes(a *v1alpha1.OdysseyApp) ([]corev1.Volume, []corev1.VolumeMount) {
	var vols []corev1.Volume
	var mounts []corev1.VolumeMount
	for _, r := range sortedResources(a) {
		if r.Kind != v1alpha1.ResourceFiles {
			continue
		}
		vols = append(vols, corev1.Volume{Name: volumeName(r.Name), VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: resource.FilesClaim(r.Name)},
		}})
		mounts = append(mounts, corev1.VolumeMount{Name: volumeName(r.Name), MountPath: resourcekind.MountPath(r.Name)})
	}
	return vols, mounts
}

// resourcesHash identifies a's resource references (empty when there are
// none, so apps without resources keep their pod template).
func resourcesHash(a *v1alpha1.OdysseyApp) string {
	refs := sortedResources(a)
	if len(refs) == 0 {
		return ""
	}
	h := sha256.New()
	for _, r := range refs {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", r.Name, r.Kind, r.Prefix)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// resourceNames are the variables a's resources define.
func resourceNames(a *v1alpha1.OdysseyApp) map[string]bool {
	out := map[string]bool{}
	for _, e := range resourceEnv(a) {
		out[e.Name] = true
	}
	return out
}

// notReady says why resource ref cannot be used yet ("" when it can).
func (r *Reconciler) notReady(ctx context.Context, a *v1alpha1.OdysseyApp, ref v1alpha1.ResourceRefSpec) (string, error) {
	var o v1alpha1.OdysseyResource
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: ref.Name}, &o); err != nil {
		if apierrors.IsNotFound(err) {
			return "it does not exist in this environment", nil
		}
		return "", err
	}
	switch {
	case o.Spec.Kind != ref.Kind:
		return fmt.Sprintf("it is a %s, not a %s", o.Spec.Kind, ref.Kind), nil
	case o.Spec.Deleted || !o.DeletionTimestamp.IsZero():
		return "it is being deleted", nil
	case o.Status.ObservedGeneration != o.Spec.Generation || o.Status.Phase == "":
		return "it is being prepared", nil
	case o.Status.Phase == v1alpha1.ResourceReady:
		return "", nil
	case o.Status.Message != "":
		return o.Status.Message, nil
	}
	return string(o.Status.Phase), nil
}

// resourcesStep holds a's rollout until every resource it uses is ready (the
// running version keeps serving: the Deployment is not touched). done says
// the pass must stop there (with res, err). It mirrors secretsStep: a
// revision that already has its verdict is not held by a resource that
// goes away later, except one that failed waiting for a resource.
func (r *Reconciler) resourcesStep(ctx context.Context, a *v1alpha1.OdysseyApp) (done bool, res ctrl.Result, err error) {
	if len(a.Spec.Resources) == 0 {
		return false, ctrl.Result{}, r.clearWaiting(ctx, a, annotationResourcesWaiting)
	}
	if settled(a) {
		return a.Status.Phase == v1alpha1.PhaseFailed && r.waitedFor(a, annotationResourcesWaiting), ctrl.Result{}, nil
	}
	for _, ref := range sortedResources(a) {
		why, err := r.notReady(ctx, a, ref)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if why == "" {
			continue
		}
		since, err := r.waitingSinceAt(ctx, a, annotationResourcesWaiting)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if r.Now().Sub(since) >= resourcekind.Wait {
			return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonResource,
				Message: fmt.Sprintf("resource %s is not ready: %s", ref.Name, why)})
		}
		return true, ctrl.Result{RequeueAfter: progressEvery}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseProgressing,
			Message: fmt.Sprintf("Waiting for resource %s: %s", ref.Name, why)})
	}
	return false, ctrl.Result{}, r.clearWaiting(ctx, a, annotationResourcesWaiting)
}

// clearWaiting forgets a recorded wait (annotation key) once nothing waits.
func (r *Reconciler) clearWaiting(ctx context.Context, a *v1alpha1.OdysseyApp, key string) error {
	if _, ok := a.Annotations[key]; !ok {
		return nil
	}
	delete(a.Annotations, key)
	return r.Client.Update(ctx, a)
}
