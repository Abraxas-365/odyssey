package resource

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// The agent's cluster-wide permissions for resources (make gen →
// agent/install/rbac/role.yaml): what the cache and the watches need. The
// verbs that create, grow or erase data (persistentvolumeclaims create/
// update/patch/delete, CloudNativePG clusters create/update/delete,
// deployments and services delete) and the redis password's Secret are NOT
// here: desired.EnvironmentRole grants them, bound only in the environments'
// namespaces (agent/install/rbac.yaml, kept by hand and tested in
// agent/install_test.go).
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyresources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyresources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyresources/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

const (
	// AnnotationReported is on an OdysseyResource whose deletion was
	// reported to the control plane: the value is the generation reported.
	// The tunnel sets it (desired.Syncer.ResourceReported); only then, when
	// the Reconciler waits for it, is the object removed.
	AnnotationReported = "odyssey.dev/deleted-reported"

	// workEvery is how often a resource that is not ready is looked at
	// again (pods, volumes and Clusters also trigger reconciles).
	workEvery = 5 * time.Second
	// failedEvery is how often a failed resource is retried.
	failedEvery = 30 * time.Second
	// settledEvery is the safety-net resync of a ready resource.
	settledEvery = 5 * time.Minute
)

// Reconciler turns OdysseyResources into databases, caches and volumes.
type Reconciler struct {
	// Client reads from the agent's cache (label-scoped: only objects with
	// the managed-by label) and writes. Secrets are never cached.
	Client client.Client
	// API reads what the cache does not hold: foreign objects in the way,
	// Secrets, storage classes.
	API client.Reader
	// Owned are more kinds a resource owns and reacts to: the CloudNativePG
	// Cluster, when its API exists at startup (an informer on a missing kind
	// would stop the manager). Without it, postgres resources are re-checked
	// periodically.
	Owned []client.Object
	// Mapper tells whether the CloudNativePG Cluster kind is served (nil:
	// Client's mapper, which discovers lazily).
	Mapper meta.RESTMapper
	// AwaitReport holds a deleted resource's object (phase deleted) until
	// the tunnel marks its deletion as reported (AnnotationReported). False
	// (an agent with no tunnel) removes it at once.
	AwaitReport bool
}

// SetupWithManager registers the controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.API == nil {
		r.API = mgr.GetAPIReader()
	}
	// Spec changes, a deletion, and the mark the tunnel puts when it reported
	// one; the agent's own status writes need no new pass.
	relevant := predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		o, n := e.ObjectOld, e.ObjectNew
		return o.GetGeneration() != n.GetGeneration() ||
			o.GetAnnotations()[AnnotationReported] != n.GetAnnotations()[AnnotationReported] ||
			o.GetDeletionTimestamp().IsZero() != n.GetDeletionTimestamp().IsZero()
	}}
	b := ctrl.NewControllerManagedBy(mgr).
		Named("odysseyresource").
		For(&v1alpha1.OdysseyResource{}, builder.WithPredicates(relevant)).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{})
	for _, o := range r.Owned {
		b = b.Owns(o)
	}
	return b.Complete(r)
}

// observation is what the pass found: the phase and, unless ready, why.
type observation struct {
	phase   v1alpha1.ResourcePhase
	message string
}

func ready() observation { return observation{phase: v1alpha1.ResourceReady} }

func provisioning(msg string) observation {
	return observation{phase: v1alpha1.ResourceProvisioning, message: msg}
}

func failed(msg string) observation {
	return observation{phase: v1alpha1.ResourceFailed, message: msg}
}

// Reconcile makes the resource what its spec says and records how it is.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	res, err := r.reconcile(ctx, req)
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}
	return res, err
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var o v1alpha1.OdysseyResource
	if err := r.Client.Get(ctx, req.NamespacedName, &o); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err) // gone: owner references clean up
	}
	if o.Spec.Deleted {
		return r.teardown(ctx, &o)
	}
	if !o.DeletionTimestamp.IsZero() {
		// Someone deleted the object by hand (or its namespace is going):
		// the data is erased only when the control plane says so. The
		// finalizer stays, so the children (owned by it) are kept.
		return r.keep(ctx, &o)
	}
	if controllerutil.AddFinalizer(&o, Finalizer) {
		if err := r.Client.Update(ctx, &o); err != nil {
			return ctrl.Result{}, err
		}
	}
	var (
		obs observation
		err error
	)
	switch o.Spec.Kind {
	case v1alpha1.ResourcePostgres:
		obs, err = r.postgres(ctx, &o)
	case v1alpha1.ResourceRedis:
		obs, err = r.redis(ctx, &o)
	case v1alpha1.ResourceFiles:
		obs, err = r.files(ctx, &o)
	default:
		obs = failed(fmt.Sprintf("unknown resource kind %q", o.Spec.Kind))
	}
	if err != nil {
		if msg, permanent := permanentMessage(err); permanent {
			obs, err = failed(msg), nil
		} else {
			return ctrl.Result{}, err
		}
	}
	if err := r.report(ctx, &o, obs); err != nil {
		return ctrl.Result{}, err
	}
	switch obs.phase {
	case v1alpha1.ResourceReady:
		return ctrl.Result{RequeueAfter: settledEvery}, nil
	case v1alpha1.ResourceFailed:
		return ctrl.Result{RequeueAfter: failedEvery}, nil
	}
	return ctrl.Result{RequeueAfter: workEvery}, nil
}

// report records obs as the status of o's current generation (nothing is
// written when it already says so).
func (r *Reconciler) report(ctx context.Context, o *v1alpha1.OdysseyResource, obs observation) error {
	next := v1alpha1.OdysseyResourceStatus{ObservedGeneration: o.Spec.Generation, Phase: obs.phase, Message: clip(obs.message, 500)}
	if o.Status == next {
		return nil
	}
	o.Status = next
	return r.Client.Status().Update(ctx, o)
}

// permanentMessage says what a refusal of the API means for a person: the
// cluster will not take the object as it is, retrying is useless (it is
// retried slowly, as a failed resource).
func permanentMessage(err error) (string, bool) {
	switch {
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return firstLine(err.Error()), true
	case apierrors.IsForbidden(err):
		return "the agent may not do it: " + firstLine(err.Error()), true
	case meta.IsNoMatchError(err):
		return "this cluster does not serve " + firstLine(err.Error()), true
	case apierrors.IsAlreadyExists(err):
		return "an object with the name this resource needs already exists and is not managed by Odyssey: " + firstLine(err.Error()), true
	}
	var n *notOurs
	if asNotOurs(err, &n) {
		return n.Error(), true
	}
	return "", false
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return clip(s, 300)
}

// clip shortens s to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// notOurs is an object in the way: it has the name of one the resource makes
// but the resource does not control it.
type notOurs struct{ kind, name string }

func (n *notOurs) Error() string {
	return fmt.Sprintf("a %s named %s already exists and is not managed by Odyssey", n.kind, n.name)
}

func asNotOurs(err error, target **notOurs) bool {
	for err != nil {
		if n, ok := err.(*notOurs); ok {
			*target = n
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// labels of what o makes: the agent's marks, the resource's name and the
// organization/project/environment ids o carries.
func labels(o *v1alpha1.OdysseyResource) map[string]string {
	l := map[string]string{LabelManagedBy: ManagedBy, LabelResource: o.Name}
	for _, k := range []string{"odyssey.dev/organization", "odyssey.dev/project", "odyssey.dev/environment"} {
		if v := o.Labels[k]; v != "" {
			l[k] = v
		}
	}
	return l
}

// generation is a short mark of the generation, for annotations.
func generation(o *v1alpha1.OdysseyResource) string { return strconv.FormatInt(o.Spec.Generation, 10) }
