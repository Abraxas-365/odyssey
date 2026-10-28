package tunnel

import (
	"context"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// labelEnvironment is how the agent marks the apps it applied (agent/desired).
const labelEnvironment = "odyssey.dev/environment"

// WatchStatuses tells c whenever an applied OdysseyApp's status changes
// (the app controller wrote it), so the change is reported at once.
func (c *Client) WatchStatuses(mgr ctrl.Manager) error {
	applied := func(a *v1alpha1.OdysseyApp) bool { return a.Labels[labelEnvironment] != "" }
	changed := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return applied(e.Object.(*v1alpha1.OdysseyApp)) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld.(*v1alpha1.OdysseyApp), e.ObjectNew.(*v1alpha1.OdysseyApp)
			return applied(n) && o.Status != n.Status
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("odysseyapp-report").
		For(&v1alpha1.OdysseyApp{}, builder.WithPredicates(changed)).
		Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
			// Apps live in odyssey-<environment> (agent/desired.Namespace).
			if env, ok := strings.CutPrefix(req.Namespace, "odyssey-"); ok {
				c.Changed(env, req.Name)
			}
			return reconcile.Result{}, nil
		}))
}
