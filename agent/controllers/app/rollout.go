package app

import (
	"context"
	"fmt"
	"maps"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The rollout of a revision (MS-2), in this order:
//
//  1. The Ingresses that exist are brought up to date first: a gate turned
//     on, or a route that changed, never waits behind a slow rollout.
//  2. Services nobody outside the app reaches (internal web services and
//     workers) roll out first; the public ones wait until those are ready,
//     so a new public version never talks to an old internal one.
//  3. A public service's Ingress appears once the service is ready (the
//     route it takes from another service, e.g. /api from "/", never
//     answers 503 meanwhile). The service at "/" is the exception: its
//     Ingress is the app's address and exists from the start.
//  4. A Service switches its selector to its workload only once the
//     workload is ready: until then it keeps sending traffic to whatever it
//     selected (the previous service at the same address).
//
// The app is ready when every service is; it failed when one did (the
// control plane then restores the previous version as a whole). Once ready,
// the objects of services the revision no longer has are deleted.
//
// An app from before services ran as one Deployment named after the app,
// selected by the app's label (immutable). It is migrated in place the
// first time it is seen: the "/" service's workload starts next to it, the
// Service <app> keeps selecting by the app's label (old and new pods both
// carry it) until the new one is ready, then narrows to it, and only then
// the old Deployment goes. The app's other services wait for that: while the
// Service selects by the app's label it must not reach them.

// rollout is one pass over a's workloads.
type rollout struct {
	r         *Reconciler
	a         *v1alpha1.OdysseyApp
	all       []workload
	protected bool
	unexpose  func() error
	pods      []corev1.Pod
	seen      map[string]Observation // by service name
}

// legacy is the Deployment of an app from before services, if it still
// runs (nil otherwise).
func (r *Reconciler) legacy(ctx context.Context, a *v1alpha1.OdysseyApp) (*appsv1.Deployment, error) {
	var d appsv1.Deployment
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: a.Name}, &d); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(&d, a) {
		return nil, nil
	}
	return &d, nil
}

// rootOf is the service at "/" (nil when no service answers the whole address).
func rootOf(ws []workload) *workload {
	for i := range ws {
		if ws[i].root() {
			return &ws[i]
		}
	}
	return nil
}

// ingressOf names w's Ingress.
func (x *rollout) ingress(w workload) *networkingv1.Ingress {
	return &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: w.Object, Namespace: x.a.Namespace}}
}

// callback: the sign-in callback rides on w's Ingress (protected, no service
// at "/", w the first public one).
func (x *rollout) callback(w workload) bool {
	if !x.protected || rootOf(x.all) != nil {
		return false
	}
	for _, s := range x.all {
		if s.public() {
			return s.Name == w.Name
		}
	}
	return false
}

// ensureIngress creates or updates w's Ingress; create=false only updates
// one that exists.
func (x *rollout) ensureIngress(ctx context.Context, w workload, create bool) (bool, ctrl.Result, error) {
	ing := x.ingress(w)
	if !create {
		if err := x.r.Client.Get(ctx, client.ObjectKeyFromObject(ing), ing); err != nil {
			return false, ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	var onInvalid func() error
	if x.protected {
		onInvalid = x.unexpose // an invalid update must not leave an old, open Ingress live
	}
	done, res, err := x.r.ensure(ctx, x.a, ing, func() {
		mutateRoute(ing, x.a, w, x.callback(w), x.r.Config)
		applyExposure(ing, x.r.Exposure, x.a, w, x.protected)
	}, onInvalid)
	if x.protected && err != nil {
		err = failClosed(err, x.unexpose())
	}
	return done, res, err
}

// run rolls w out: its Deployment, then (web) its Service, then (public, when
// allowed) its Ingress. legacy: w is the "/" service of a migrating app.
func (x *rollout) run(ctx context.Context, w workload, legacy bool) (bool, ctrl.Result, error) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: w.Deployment, Namespace: x.a.Namespace}}
	if done, res, err := x.r.ensure(ctx, x.a, d, func() { mutateWorkload(d, x.a, w, x.all, x.r.Config) }, nil); done {
		return done, res, err
	}
	// Against the revision its pods run: a revision that left them as they
	// were (mutateWorkload) is judged by the Deployment's availability alone
	// (restarts the kept pods piled up over weeks are not this revision's
	// doing, and must not fail a change of another service; the control
	// plane's timeout bounds a service that is down).
	rev, pods := x.a.Spec.Revision, podsOf(x.pods, w.Deployment)
	kept := revisionOf(d) > 0 && revisionOf(d) < rev
	if kept {
		rev, pods = revisionOf(d), nil
	}
	o := Observe(d, pods, rev, x.r.Now(), x.r.HealthGrace)
	if kept {
		o.ReadyReplicas = d.Status.ReadyReplicas
	}
	x.seen[w.Name] = o
	ready := o.Phase == v1alpha1.PhaseReady
	if w.web() {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: w.Object, Namespace: x.a.Namespace}}
		if done, res, err := x.r.ensure(ctx, x.a, svc, func() {
			keep := !ready && len(svc.Spec.Selector) > 0 && svc.Spec.Selector[LabelWorkload] != w.Deployment
			if keep && !legacy {
				// Not ready yet: the address keeps answering with what it
				// answered with (the previous service at it).
				prev := svc.Spec.Selector
				mutateService(svc, x.a, w, false)
				svc.Spec.Selector = prev
				return
			}
			mutateService(svc, x.a, w, legacy && !ready)
		}, nil); done {
			return done, res, err
		}
	}
	if w.public() {
		// The app's address exists from the start; any other route only
		// once its service answers it.
		if done, res, err := x.ensureIngress(ctx, w, w.root() || ready); done {
			return done, res, err
		}
	}
	return false, ctrl.Result{}, nil
}

// podsOf are the pods of workload.
func podsOf(pods []corev1.Pod, workload string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if p.Labels[LabelWorkload] == workload {
			out = append(out, p)
		}
	}
	return out
}

// observation is the app's: failed when a service failed, ready when every
// one is, progressing otherwise. Waiting services (not reached this pass)
// count as progressing.
func (x *rollout) observation() Observation {
	out := Observation{Phase: v1alpha1.PhaseReady}
	several := len(x.all) > 1
	var progressing string
	for _, w := range x.all {
		o, ok := x.seen[w.Name]
		if !ok {
			o = Observation{Phase: v1alpha1.PhaseProgressing, Message: "waiting for the app's other services"}
		}
		out.ReadyReplicas += o.ReadyReplicas
		msg := o.Message
		if several && msg != "" {
			msg = w.Name + ": " + msg
		}
		if len(x.a.Spec.Services) > 0 {
			out.Services = append(out.Services, v1alpha1.ServiceStatus{Name: w.Name, Phase: o.Phase, Reason: o.Reason,
				Message: o.Message, ReadyReplicas: o.ReadyReplicas})
		}
		switch o.Phase {
		case v1alpha1.PhaseFailed:
			if out.Phase != v1alpha1.PhaseFailed {
				out.Phase, out.Reason, out.Message = v1alpha1.PhaseFailed, o.Reason, msg
			}
		case v1alpha1.PhaseProgressing:
			if progressing == "" {
				progressing = msg
			}
		}
	}
	if out.Phase == v1alpha1.PhaseReady && progressing != "" {
		out.Phase, out.Message = v1alpha1.PhaseProgressing, progressing
	}
	for _, w := range x.all {
		if _, ok := x.seen[w.Name]; !ok && out.Phase == v1alpha1.PhaseReady {
			out.Phase = v1alpha1.PhaseProgressing
		}
	}
	return out
}

// collect deletes what a's revision no longer has: the Deployments,
// Services and Ingresses of services it dropped (and a "/" service's old
// Deployment when the app has none at "/"), then the route removers no
// Ingress references.
func (r *Reconciler) collect(ctx context.Context, a *v1alpha1.OdysseyApp, all []workload) error {
	deployments, services, ingresses, prefixes := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, w := range all {
		deployments[w.Deployment] = true
		if w.web() {
			services[w.Object] = true
		}
		if w.public() {
			ingresses[w.Object] = true
			if w.StripPrefix {
				prefixes[w.Deployment] = w.Route
			}
		}
	}
	mine := client.MatchingLabels{LabelManagedBy: ManagedBy, LabelApp: a.Name}
	for _, kind := range []struct {
		list client.ObjectList
		keep map[string]bool
	}{
		{&networkingv1.IngressList{}, ingresses}, // routes first: nothing points at what goes next
		{&corev1.ServiceList{}, services},
		{&appsv1.DeploymentList{}, deployments},
	} {
		if err := r.Client.List(ctx, kind.list, client.InNamespace(a.Namespace), mine); err != nil {
			return err
		}
		items, err := metaItems(kind.list)
		if err != nil {
			return err
		}
		for _, o := range items {
			if kind.keep[o.GetName()] || !metav1.IsControlledBy(o, a) {
				continue
			}
			if err := r.Client.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("delete %s: %w", o.GetName(), err)
			}
		}
	}
	if r.Exposure != nil {
		return r.Exposure.DropPrefixes(ctx, a, maps.Clone(prefixes))
	}
	return nil
}

// metaItems are the objects of a typed list.
func metaItems(list client.ObjectList) ([]client.Object, error) {
	var out []client.Object
	switch l := list.(type) {
	case *networkingv1.IngressList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *corev1.ServiceList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *appsv1.DeploymentList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	default:
		return nil, fmt.Errorf("unexpected list %T", list)
	}
	return out, nil
}

// unexposeAll deletes every Ingress of a (protection cannot be applied, or
// another app has the host).
func (r *Reconciler) unexposeAll(ctx context.Context, a *v1alpha1.OdysseyApp) error {
	var list networkingv1.IngressList
	if err := r.Client.List(ctx, &list, client.InNamespace(a.Namespace), client.MatchingLabels{LabelManagedBy: ManagedBy, LabelApp: a.Name}); err != nil {
		return err
	}
	// The app's own name too: an Ingress the cache has not seen yet.
	names := map[string]bool{a.Name: true}
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], a) {
			names[list.Items[i].Name] = true
		}
	}
	for name := range names {
		ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace}}
		if err := r.Client.Delete(ctx, ing); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
