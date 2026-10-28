package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// progressEvery is how often a rollout in progress is re-observed (pod
// events also trigger reconciles; this catches time-based failures).
const progressEvery = 3 * time.Second

// The agent's permissions (make gen → agent/install/rbac/role.yaml). Owner
// references garbage-collect what it creates, so it deletes only the
// ingress of an app that lost its host. Phase 1a-a3 narrows the cluster-wide
// grants to the odyssey-* namespaces (the agent creates them itself then).
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps,verbs=get;list;watch
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,namespace=odyssey-system,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",namespace=odyssey-system,resources=events,verbs=create;patch

// Reconciler turns OdysseyApps into running apps.
type Reconciler struct {
	// Client reads from the agent's cache, which holds only objects with
	// the managed-by label.
	Client client.Client
	// API reads the cluster directly: objects the cache cannot see (an
	// unmanaged object in the way, another ingress using a host).
	API    client.Reader
	Config Config
	// HealthGrace is how long a running pod of the new revision may fail its
	// health check before the app is reported failed (default 2 min; keep it
	// below Config.ReadyDeadline, both below the control plane's 5 min).
	HealthGrace time.Duration
	// Now is the clock (tests); nil uses time.Now.
	Now func() time.Time
}

// SetupWithManager registers the controller: it reconciles OdysseyApps and
// reacts to their Deployments and pods.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.HealthGrace <= 0 {
		r.HealthGrace = 2 * time.Minute
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.API == nil {
		r.API = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		// Spec changes only: the agent's own status writes need no new pass.
		For(&v1alpha1.OdysseyApp{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		// Pods are owned by ReplicaSets; map them to their app by label.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			name := o.GetLabels()[LabelApp]
			if name == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: o.GetNamespace(), Name: name}}}
		})).
		Named("odysseyapp").
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	res, err := r.reconcile(ctx, req)
	if apierrors.IsConflict(err) {
		// Someone (often our own previous pass, ahead of the cache) changed
		// the object: look again with fresh data.
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}
	return res, err
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var a v1alpha1.OdysseyApp
	if err := r.Client.Get(ctx, req.NamespacedName, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err) // deleted: owner refs clean up
	}
	logger := log.FromContext(ctx).WithValues("revision", a.Spec.Revision)

	if taken, err := r.hostTaken(ctx, &a); err != nil {
		return ctrl.Result{}, err
	} else if taken != "" {
		// The app that had the host first keeps it; this one steps aside.
		ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
		if err := r.Client.Delete(ctx, ing); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.report(ctx, &a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: fmt.Sprintf("host %s is already used by %s", r.Config.host(&a), taken)})
	}

	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
	for _, x := range []struct {
		obj    client.Object
		mutate func()
	}{
		{d, func() { mutateDeployment(d, &a, r.Config) }},
		{svc, func() { mutateService(svc, &a) }},
		{ing, func() { mutateIngress(ing, &a, r.Config) }},
	} {
		if done, res, err := r.ensure(ctx, &a, x.obj, x.mutate); done {
			return res, err
		}
	}

	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(a.Namespace), client.MatchingLabels{LabelApp: a.Name}); err != nil {
		return ctrl.Result{}, err
	}
	o := Observe(d, pods.Items, a.Spec.Revision, r.Now(), r.HealthGrace)
	if err := r.report(ctx, &a, o); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(1).Info("observed", "phase", o.Phase, "reason", o.Reason, "message", o.Message)
	if o.Phase == v1alpha1.PhaseProgressing {
		return ctrl.Result{RequeueAfter: progressEvery}, nil
	}
	return ctrl.Result{}, nil
}

// ensure creates or updates one of a's objects. done reports that the pass
// must stop there (with res, err).
func (r *Reconciler) ensure(ctx context.Context, a *v1alpha1.OdysseyApp, obj client.Object, mutate func()) (done bool, res ctrl.Result, err error) {
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		mutate()
		return controllerutil.SetControllerReference(a, obj, r.Client.Scheme())
	})
	switch {
	case err == nil:
		return false, ctrl.Result{}, nil
	case apierrors.IsAlreadyExists(err):
		// Not in the cache: either ours and the cache is behind, or an
		// object the agent does not manage is in the way.
		live := obj.DeepCopyObject().(client.Object)
		if err := r.API.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			return true, ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if metav1.IsControlledBy(live, a) && live.GetLabels()[LabelManagedBy] == ManagedBy {
			return true, ctrl.Result{RequeueAfter: time.Second}, nil
		}
		kind := strings.TrimPrefix(fmt.Sprintf("%T", obj), "*v1.")
		return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: fmt.Sprintf("a %s named %s already exists in %s and is not managed by Odyssey", kind, a.Name, a.Namespace)})
	case apierrors.IsInvalid(err):
		return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig, Message: firstLine(err.Error(), "")})
	}
	return true, ctrl.Result{}, err
}

// report writes the status of a's spec revision when it changed. A settled
// verdict (ready or failed) is final for its revision: the control plane
// acted on it, and the status describes the rollout, not the app's later
// health. A new revision starts over.
func (r *Reconciler) report(ctx context.Context, a *v1alpha1.OdysseyApp, o Observation) error {
	s := a.Status
	if s.ObservedRevision == a.Spec.Revision && (s.Phase == v1alpha1.PhaseFailed || s.Phase == v1alpha1.PhaseReady) {
		return nil
	}
	next := v1alpha1.OdysseyAppStatus{
		ObservedRevision: a.Spec.Revision, Phase: o.Phase, Reason: o.Reason, Message: o.Message,
		ReadyReplicas: o.ReadyReplicas, URL: r.Config.URL(a),
	}
	if next == s {
		return nil
	}
	a.Status = next
	return r.Client.Status().Update(ctx, a)
}

// hostTaken names another ingress that had a's host first. It reads the
// cluster, not the cache: any ingress counts, managed by Odyssey or not.
// Between two apps created at once, the older ingress (then the lower
// name) keeps the host, so exactly one of them steps aside.
func (r *Reconciler) hostTaken(ctx context.Context, a *v1alpha1.OdysseyApp) (string, error) {
	var list networkingv1.IngressList
	if err := r.API.List(ctx, &list); err != nil {
		return "", err
	}
	host := r.Config.host(a)
	var mine *networkingv1.Ingress
	for i := range list.Items {
		if list.Items[i].Namespace == a.Namespace && list.Items[i].Name == a.Name {
			mine = &list.Items[i]
		}
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other == mine || !servesHost(other, host) {
			continue
		}
		if mine == nil || first(other, mine) {
			return other.Namespace + "/" + other.Name, nil
		}
	}
	return "", nil
}

func servesHost(i *networkingv1.Ingress, host string) bool {
	for _, rule := range i.Spec.Rules {
		if rule.Host == host {
			return true
		}
	}
	return false
}

// first reports whether x claimed its host before y.
func first(x, y *networkingv1.Ingress) bool {
	tx, ty := x.CreationTimestamp.Time, y.CreationTimestamp.Time
	if !tx.Equal(ty) {
		return tx.Before(ty)
	}
	return x.Namespace+"/"+x.Name < y.Namespace+"/"+y.Name
}
