// Package build is the agent's controller for OdysseyBuild: it runs each
// build as a Job in odyssey-builds (job.go), with a one-time token its pod
// presents to fetch the source from the agent, and records what the Job
// shows (observe.go) in the build's status. A final status never changes.
package build

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups=odyssey.dev,namespace=odyssey-builds,resources=odysseybuilds,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=odyssey.dev,namespace=odyssey-builds,resources=odysseybuilds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odyssey.dev,namespace=odyssey-builds,resources=odysseybuilds/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,namespace=odyssey-builds,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",namespace=odyssey-builds,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=odyssey-builds,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",namespace=odyssey-builds,resources=secrets,verbs=get;create;delete

// Reconciler runs OdysseyBuilds.
type Reconciler struct {
	Client client.Client
	// API reads what the agent does not cache (the token Secrets).
	API    client.Reader
	Config Config
	// Now is the clock (tests); time.Now by default.
	Now func() time.Time
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.API == nil {
		r.API = mgr.GetAPIReader()
	}
	r.Config = r.Config.withDefaults()
	inBuilds := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetNamespace() == Namespace })
	return ctrl.NewControllerManagedBy(mgr).
		Named("odysseybuild").
		For(&v1alpha1.OdysseyBuild{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}, inBuilds)).
		Owns(&batchv1.Job{}).
		// The Job owns the pod; map it to its build by label.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			name := o.GetLabels()[LabelBuild]
			if name == "" || o.GetNamespace() != Namespace {
				return nil
			}
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: Namespace, Name: name}}}
		})).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b v1alpha1.OdysseyBuild
	if err := r.Client.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err) // cancelled: its Job and Secret go with it
	}
	if !b.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if b.Status.Phase.Final() {
		return r.finished(ctx, &b)
	}
	job, err := r.ensure(ctx, &b)
	if err != nil {
		return ctrl.Result{}, err
	}
	var next v1alpha1.OdysseyBuildStatus
	if job == nil {
		// its Job is gone before the build ended (deleted, or kept past its
		// TTL while the agent was down): running it again could push a
		// second image, so it failed
		next = failed(v1alpha1.BuildReasonFailed, "the build's Job disappeared before it finished")
	} else {
		pod, err := r.pod(ctx, &b)
		if err != nil {
			return ctrl.Result{}, err
		}
		next = Observe(job, pod)
		if next.Phase == v1alpha1.BuildPending && b.Status.Phase == v1alpha1.BuildRunning {
			next = b.Status // the pod left the cache for a moment: it does not go back
		}
	}
	if next.Phase.Final() {
		next.FinishedAt = &metav1.Time{Time: r.now()}
	}
	if next.Phase == b.Status.Phase && next.ImageDigest == b.Status.ImageDigest && next.Reason == b.Status.Reason && next.Message == b.Status.Message {
		return ctrl.Result{}, nil
	}
	b.Status = next
	if err := r.Client.Status().Update(ctx, &b); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if next.Phase.Final() {
		return r.finished(ctx, &b)
	}
	return ctrl.Result{}, nil
}

// finished cleans up after a build: its token (nothing may fetch its source
// any more) at once, the build itself once Retention has passed (the tunnel
// has reported it by then; its Job goes with it).
func (r *Reconciler) finished(ctx context.Context, b *v1alpha1.OdysseyBuild) (ctrl.Result, error) {
	// (its Job is left to its TTL: its pod's log may still be being read.
	// Nothing that could still push is final: Observe waits on pulls.)
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: JobName(b), Namespace: Namespace}}
	if err := r.Client.Delete(ctx, sec); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	at := b.CreationTimestamp.Time
	if b.Status.FinishedAt != nil {
		at = b.Status.FinishedAt.Time
	}
	if left := at.Add(r.Config.withDefaults().Retention).Sub(r.now()); left > 0 {
		return ctrl.Result{RequeueAfter: left}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, b, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ensure creates the build's token Secret and Job (both owned by the
// build, so cancelling it deletes them) and returns the Job; nil when the
// Job was created before and is gone.
func (r *Reconciler) ensure(ctx context.Context, b *v1alpha1.OdysseyBuild) (*batchv1.Job, error) {
	var job batchv1.Job
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: JobName(b)}, &job)
	if err == nil {
		return &job, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if b.Status.Phase != "" {
		// a status is recorded only once the Job exists (Observe never
		// returns an empty phase)
		return nil, nil
	}
	var sec corev1.Secret
	switch err := r.API.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: JobName(b)}, &sec); {
	case apierrors.IsNotFound(err):
		token, err := newToken()
		if err != nil {
			return nil, err
		}
		s := TokenSecret(b, token)
		if err := controllerutil.SetControllerReference(b, s, r.Client.Scheme()); err != nil {
			return nil, err
		}
		if err := r.Client.Create(ctx, s); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	j := Job(b, r.Config)
	if err := controllerutil.SetControllerReference(b, j, r.Client.Scheme()); err != nil {
		return nil, err
	}
	if err := r.Client.Create(ctx, j); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return j, nil
}

// pod is the build's pod (the newest, should the Job have made more).
func (r *Reconciler) pod(ctx context.Context, b *v1alpha1.OdysseyBuild) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(Namespace), client.MatchingLabels{LabelBuild: b.Name}); err != nil {
		return nil, err
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if newest == nil || newer(p.CreationTimestamp, newest.CreationTimestamp) {
			newest = p
		}
	}
	return newest, nil
}

func newer(a, b metav1.Time) bool { return b.Before(&a) }

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
