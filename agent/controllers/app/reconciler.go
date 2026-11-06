package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

// protectedResync is how often a settled protected app re-checks its
// Middlewares (a safety net next to the watch).
const protectedResync = 10 * time.Minute

// legacyDrain is how long an app moving to services keeps its old
// Deployment after the Service stopped selecting it: the ingress follows
// the Service's endpoints a moment later.
const legacyDrain = 10 * time.Second

// annotationLegacyDrain records when that wait started.
const annotationLegacyDrain = "odicea.dev/legacy-drain"

// The agent's permissions (make gen → agent/install/rbac/role.yaml). Owner
// references garbage-collect what it creates, so it deletes only the
// ingress of an app that lost its host. Phase 1a-a3 narrows the cluster-wide
// grants to the odyssey-* namespaces (the agent creates them itself then).
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps,verbs=get;list;watch
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyresources,verbs=get;list;watch
// Secrets and ExternalSecrets: see secrets.go and agent/secretstore.
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
	// Exposure is the cluster's ingress variant: it removes routes' prefixes
	// and puts the sign-in gate in front of apps whose access is not public
	// (nil: neither is possible, so no such app is exposed).
	Exposure Exposure
	// Owned are more kinds the app owns and reacts to (the Exposure's
	// Middlewares, when the cluster has them at startup).
	Owned []client.Object
	// ExternalSecrets tells whether the ExternalSecret kind exists, i.e. the
	// External Secrets Operator is installed (nil: Client's mapper).
	ExternalSecrets meta.RESTMapper
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
	b := ctrl.NewControllerManagedBy(mgr).
		// Spec changes only: the agent's own status writes need no new pass.
		For(&v1alpha1.OdysseyApp{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{})
	for _, o := range r.Owned {
		b = b.Owns(o) // a deleted or edited Middleware is put back
	}
	return b.
		// A resource that becomes ready (or goes away) moves the apps that use it.
		Watches(&v1alpha1.OdysseyResource{}, handler.EnqueueRequestsFromMapFunc(r.appsUsing)).
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

// appsUsing are the apps of o's namespace that use the resource o.
func (r *Reconciler) appsUsing(ctx context.Context, o client.Object) []reconcile.Request {
	var apps v1alpha1.OdysseyAppList
	if err := r.Client.List(ctx, &apps, client.InNamespace(o.GetNamespace())); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range apps.Items {
		for _, ref := range apps.Items[i].Spec.Resources {
			if ref.Name == o.GetName() {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&apps.Items[i])})
				break
			}
		}
	}
	return out
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
		if err := r.unexposeAll(ctx, &a); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.report(ctx, &a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: fmt.Sprintf("host %s is already used by %s", r.Config.host(&a), taken)})
	}

	// Fail closed: an app that asks for sign-in is never exposed where
	// nothing can enforce it, nor while its protection is being applied.
	protected := accessMode(&a) != v1alpha1.AccessPublic
	unexpose := func() error { return r.unexposeAll(ctx, &a) }
	if protected && (r.Exposure == nil || !r.Exposure.CanProtect(ctx)) {
		if err := unexpose(); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.report(ctx, &a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig, Message: UnavailableMessage})
	}
	if protected {
		if err := r.Exposure.Protect(ctx, &a, policyOf(&a, r.Config)); err != nil {
			return ctrl.Result{}, failClosed(err, unexpose())
		}
	}
	all := workloads(&a)
	if r.Exposure != nil {
		// Before any Ingress references them.
		want := map[string]string{}
		for _, w := range all {
			if w.public() && w.StripPrefix {
				want[w.Deployment] = w.Route
			}
		}
		if len(want) > 0 {
			if !r.Exposure.Available(ctx) {
				return ctrl.Result{}, r.report(ctx, &a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
					Message: "this cluster cannot remove a route's prefix (it needs Traefik)"})
			}
			if err := r.Exposure.Prefixes(ctx, &a, want); err != nil {
				return ctrl.Result{}, failClosed(err, nil)
			}
		}
	} else if slices.ContainsFunc(all, func(w workload) bool { return w.public() && w.StripPrefix }) {
		return ctrl.Result{}, r.report(ctx, &a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: "this cluster cannot remove a route's prefix (it needs Traefik)"})
	}

	x := &rollout{r: r, a: &a, all: all, protected: protected, unexpose: unexpose, seen: map[string]Observation{}}
	// 1. The Ingresses that exist first: the gate (or a route change) is on
	// before anything else is written (an app that was public is exposed
	// until it is).
	for _, w := range all {
		if w.public() {
			if done, res, err := x.ensureIngress(ctx, w, w.root() && protected); done {
				return res, err
			}
		}
	}
	// Before any Deployment changes: the running version keeps serving
	// while a secret or a resource of the new one is missing.
	if done, res, err := r.resourcesStep(ctx, &a); done {
		return res, err
	}
	if done, res, err := r.secretsStep(ctx, &a); done {
		return res, err
	}

	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods, client.InNamespace(a.Namespace), client.MatchingLabels{LabelApp: a.Name}); err != nil {
		return ctrl.Result{}, err
	}
	x.pods = pods.Items
	old, err := r.legacy(ctx, &a)
	if err != nil {
		return ctrl.Result{}, err
	}
	root := rootOf(all)
	migrating := old != nil && root != nil
	if migrating {
		// 2'. An app from before services: its "/" service takes over first
		// (the rest must not run behind a Service that still selects by the
		// app's label).
		if done, res, err := x.run(ctx, *root, true); done {
			return res, err
		}
		if x.seen[root.Name].Phase != v1alpha1.PhaseReady {
			return r.finish(ctx, x, logger)
		}
		// Ready, and the Service <app> now selects it alone. The ingress
		// learns that a moment later: the old Deployment goes once it has
		// stopped sending requests there (no request lands on a pod that
		// is shutting down).
		since, err := r.waitingSinceAt(ctx, &a, annotationLegacyDrain)
		if err != nil {
			return ctrl.Result{}, err
		}
		if r.Now().Sub(since) < legacyDrain {
			o := x.seen[root.Name]
			o.Phase, o.Message = v1alpha1.PhaseProgressing, "moving to services: draining the previous layout"
			x.seen[root.Name] = o
			return r.finish(ctx, x, logger)
		}
		if err := r.Client.Delete(ctx, old); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		if err := r.clearWaiting(ctx, &a, annotationLegacyDrain); err != nil {
			return ctrl.Result{}, err
		}
	}
	// 2. Internal services and workers, 3. public ones once those are ready.
	inner := true
	for _, w := range all {
		if !w.public() {
			if done, res, err := x.run(ctx, w, false); done {
				return res, err
			}
			inner = inner && x.seen[w.Name].Phase == v1alpha1.PhaseReady
		}
	}
	if inner {
		for _, w := range all {
			if w.public() && (!migrating || !w.root()) {
				if done, res, err := x.run(ctx, w, false); done {
					return res, err
				}
			}
		}
	}
	return r.finish(ctx, x, logger)
}

// finish reports the pass's aggregate and, once the app is ready, removes
// what the revision no longer has.
func (r *Reconciler) finish(ctx context.Context, x *rollout, logger logr.Logger) (ctrl.Result, error) {
	a := x.a
	o := x.observation()
	if o.Phase == v1alpha1.PhaseReady {
		// The previous version's pods are gone: the services the revision
		// dropped can go (before the report: "ready" means the app is
		// exactly the revision).
		if err := r.collect(ctx, a, x.all); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !x.protected && r.Exposure != nil {
		// After the Ingresses stopped referencing them.
		if err := r.Exposure.Unprotect(ctx, a); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.report(ctx, a, o); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(1).Info("observed", "phase", o.Phase, "reason", o.Reason, "message", o.Message)
	if o.Phase == v1alpha1.PhaseProgressing {
		return ctrl.Result{RequeueAfter: progressEvery}, nil
	}
	if o.Phase == v1alpha1.PhaseReady {
		// What the spec no longer lists can go. A value kept only for its
		// grace is looked at again when the grace ends.
		wait, err := r.pruneSecrets(ctx, a)
		if err != nil {
			return ctrl.Result{}, err
		}
		if wait > 0 {
			if x.protected && protectedResync < wait {
				wait = protectedResync
			}
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	if x.protected {
		// Middlewares may not be watched (Traefik installed after the agent
		// started): look again from time to time.
		return ctrl.Result{RequeueAfter: protectedResync}, nil
	}
	return ctrl.Result{}, nil
}

// failClosed is err, after removing the Ingress (unexposeErr is that attempt's
// result): when protection cannot be applied the app must not stay open.
func failClosed(err, unexposeErr error) error {
	if unexposeErr != nil {
		return errors.Join(err, unexposeErr)
	}
	return err
}

// ensure creates or updates one of a's objects. done reports that the pass
// must stop there (with res, err).
func (r *Reconciler) ensure(ctx context.Context, a *v1alpha1.OdysseyApp, obj client.Object, mutate func(), onInvalid func() error) (done bool, res ctrl.Result, err error) {
	kind := strings.TrimPrefix(fmt.Sprintf("%T", obj), "*v1.")
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if c := metav1.GetControllerOf(obj); obj.GetUID() != "" && c != nil && c.UID != a.UID {
			// Another object's (a resource's, say) has the name: never take
			// it over.
			return errForeign
		}
		mutate()
		return controllerutil.SetControllerReference(a, obj, r.Client.Scheme())
	})
	switch {
	case err == nil:
		return false, ctrl.Result{}, nil
	case errors.Is(err, errForeign):
		return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: fmt.Sprintf("a %s named %s already exists in %s and belongs to something else", kind, obj.GetName(), a.Namespace)})
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
		return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig,
			Message: fmt.Sprintf("a %s named %s already exists in %s and is not managed by Odyssey", kind, obj.GetName(), a.Namespace)})
	case apierrors.IsInvalid(err):
		if onInvalid != nil {
			if uerr := onInvalid(); uerr != nil {
				return true, ctrl.Result{}, errors.Join(err, uerr)
			}
		}
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
	if s.ObservedRevision == a.Spec.Revision && s.ObservedGeneration == a.Generation && (s.Phase == v1alpha1.PhaseFailed || s.Phase == v1alpha1.PhaseReady) {
		return nil
	}
	next := v1alpha1.OdysseyAppStatus{
		ObservedRevision: a.Spec.Revision, ObservedGeneration: a.Generation, Phase: o.Phase, Reason: o.Reason, Message: o.Message,
		ReadyReplicas: o.ReadyReplicas, URL: r.Config.URL(a), Services: o.Services,
	}
	if equality.Semantic.DeepEqual(next, s) {
		return nil
	}
	a.Status = next
	return r.Client.Status().Update(ctx, a)
}

// errForeign: an object of the name exists and another owner has it.
var errForeign = errors.New("object belongs to something else")

// hostTaken names another ingress that had a's host first. It reads the
// cluster, not the cache: any ingress counts, managed by Odyssey or not.
// Between two apps created at once, the older ingress (then the lower
// name) keeps the host, so exactly one of them steps aside. The app's own
// Ingresses (one per public service) share the host.
func (r *Reconciler) hostTaken(ctx context.Context, a *v1alpha1.OdysseyApp) (string, error) {
	var list networkingv1.IngressList
	if err := r.API.List(ctx, &list); err != nil {
		return "", err
	}
	host := r.Config.host(a)
	mineOf := func(x *networkingv1.Ingress) bool {
		// The Ingress named after the app counts as its own even without an
		// owner (one it is about to take over or report as in the way). An
		// owner is always in its object's namespace.
		return x.Namespace == a.Namespace && (metav1.IsControlledBy(x, a) || x.Name == a.Name)
	}
	var mine *networkingv1.Ingress // the app's oldest
	for i := range list.Items {
		if x := &list.Items[i]; mineOf(x) && (mine == nil || first(x, mine)) {
			mine = x
		}
	}
	for i := range list.Items {
		other := &list.Items[i]
		if mineOf(other) || !servesHost(other, host) {
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
