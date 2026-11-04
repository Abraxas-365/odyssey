package tunnel

import (
	"context"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Resources is what the tunnel does with the environments' resources (2b:
// databases, caches, files; agent/desired). Optional: without it the agent
// ignores the resources the control plane sends (and does not list resource
// kinds among its capabilities).
type Resources interface {
	// ApplyResource writes the desired resource. An error IsRejected says is
	// final (reported failed); any other is retried.
	ApplyResource(ctx context.Context, r *fleetv1.Resource) error
	// ResourceStatus is one resource's observed status (nil: none yet).
	ResourceStatus(ctx context.Context, environment, name string) (*fleetv1.ResourceStatus, error)
	// ResourceStatuses are every resource's.
	ResourceStatuses(ctx context.Context) ([]*fleetv1.ResourceStatus, error)
	// ResourceReported is told what was sent, once it was: a deleted
	// resource's object is only removed after that.
	ResourceReported(ctx context.Context, s *fleetv1.ResourceStatus) error
}

// RejectedResource is the status reported for a resource the cluster refused.
type RejectedResource func(*fleetv1.Resource, error) *fleetv1.ResourceStatus

// maxReportResources matches the gateway's limit per report.
const maxReportResources = 1000

// receiveResource queues one desired resource (the newest generation wins).
// It reports whether m was a resource message.
func (c *Client) receiveResource(m *fleetv1.ControlMessage) bool {
	r := m.GetResource()
	if r == nil {
		return false
	}
	if c.o.Resources == nil {
		return true // this agent does not take resources (it did not say it could)
	}
	k := key{r.GetEnvironmentId(), r.GetName()}
	c.mu.Lock()
	if cur := c.resources[k]; cur == nil || cur.GetGeneration() <= r.GetGeneration() {
		c.resources[k] = r
	}
	c.mu.Unlock()
	c.resourceQueue.Add(k)
	return true
}

// workResources applies queued resources: a refused one is reported failed,
// a transient error is retried with backoff.
func (c *Client) workResources(ctx context.Context) {
	for {
		k, shutdown := c.resourceQueue.Get()
		if shutdown {
			return
		}
		c.mu.Lock()
		r := c.resources[k]
		c.mu.Unlock()
		if r != nil {
			err := c.o.Resources.ApplyResource(ctx, r)
			switch {
			case err == nil:
				c.resourceApplied(k, r, nil)
				c.resourceQueue.Forget(k)
			case c.o.IsRejected(err):
				c.o.Log.Error(err, "resource refused", "environment", k.environment, "resource", k.name, "generation", r.GetGeneration())
				c.resourceApplied(k, r, err)
				c.resourceQueue.Forget(k)
			case ctx.Err() == nil:
				c.o.Log.Info("apply resource failed, retrying", "environment", k.environment, "resource", k.name, "err", err)
				c.resourceQueue.AddRateLimited(k)
			}
		}
		c.resourceQueue.Done(k)
	}
}

// resourceApplied clears the resource unless a newer generation arrived
// meanwhile, and keeps (or forgets) its refusal.
func (c *Client) resourceApplied(k key, r *fleetv1.Resource, refused error) {
	c.mu.Lock()
	if c.resources[k] == r {
		delete(c.resources, k)
	}
	if refused != nil && c.o.RejectedResource != nil {
		c.rejectedResources[k] = c.o.RejectedResource(r, refused)
	} else {
		delete(c.rejectedResources, k)
	}
	c.mu.Unlock()
	c.ResourceChanged(k.environment, k.name)
}

// ResourceChanged marks a resource whose status may have changed (the
// agent's watch on OdysseyResources calls it); it is reported on the open
// tunnel, coalesced.
func (c *Client) ResourceChanged(environment, name string) {
	c.mu.Lock()
	c.resourcesChanged[key{environment, name}] = true
	c.mu.Unlock()
	c.poke()
}

// reportResourcesChanged sends the statuses that changed (a delta).
func (c *Client) reportResourcesChanged(ctx context.Context, send func(*fleetv1.AgentMessage) error) error {
	if c.o.Resources == nil {
		return nil
	}
	c.mu.Lock()
	keys := c.resourcesChanged
	c.resourcesChanged = map[key]bool{}
	c.mu.Unlock()
	var out []*fleetv1.ResourceStatus
	for k := range keys {
		s, err := c.o.Resources.ResourceStatus(ctx, k.environment, k.name)
		if err != nil {
			c.ResourceChanged(k.environment, k.name) // try again with the next change
			c.o.Log.Info("read resource status", "err", err)
			continue
		}
		if s = c.withRejectedResource(k, s); s != nil {
			out = append(out, s)
		}
	}
	return c.sendResources(ctx, send, false, out)
}

// reportResourcesAll sends every status (a full report).
func (c *Client) reportResourcesAll(ctx context.Context, send func(*fleetv1.AgentMessage) error) error {
	if c.o.Resources == nil {
		return nil
	}
	out, err := c.o.Resources.ResourceStatuses(ctx)
	if err != nil {
		c.o.Log.Info("read resource statuses", "err", err)
		out = nil
	}
	seen := map[key]bool{}
	for i, s := range out {
		k := key{s.GetEnvironmentId(), s.GetName()}
		seen[k] = true
		out[i] = c.withRejectedResource(k, s)
	}
	c.mu.Lock()
	for k, s := range c.rejectedResources {
		if !seen[k] {
			out = append(out, s)
		}
	}
	c.mu.Unlock()
	return c.sendResources(ctx, send, err == nil, out)
}

// withRejectedResource prefers a refusal of a newer generation over what the
// cluster has.
func (c *Client) withRejectedResource(k key, s *fleetv1.ResourceStatus) *fleetv1.ResourceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.rejectedResources[k]; r != nil && (s == nil || r.GetObservedGeneration() > s.GetObservedGeneration()) {
		return r
	}
	return s
}

func (c *Client) sendResources(ctx context.Context, send func(*fleetv1.AgentMessage) error, full bool, rs []*fleetv1.ResourceStatus) error {
	if len(rs) == 0 && !full {
		return nil
	}
	sent := rs
	// "Full" lets the control plane forget statuses missing from the report,
	// so only a report carrying every status may say so: a set split in
	// chunks goes as partial reports (nothing forgotten, nothing lost).
	if len(rs) > maxReportResources {
		full = false
	}
	for len(rs) > maxReportResources {
		if err := send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_ResourceReport{ResourceReport: &fleetv1.ResourceReport{Resources: rs[:maxReportResources]}}}); err != nil {
			return err
		}
		rs = rs[maxReportResources:]
	}
	if err := send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_ResourceReport{ResourceReport: &fleetv1.ResourceReport{Full: full, Resources: rs}}}); err != nil {
		return err
	}
	// Told: a deleted resource's object can go now.
	for _, s := range sent {
		if err := c.o.Resources.ResourceReported(ctx, s); err != nil {
			c.o.Log.Info("mark resource reported", "resource", s.GetName(), "err", err)
			c.ResourceChanged(s.GetEnvironmentId(), s.GetName())
		}
	}
	return nil
}

// WatchResourceStatuses tells c whenever an applied OdysseyResource's status
// changes (the resource controller wrote it), so the change is reported at
// once.
func (c *Client) WatchResourceStatuses(mgr ctrl.Manager) error {
	applied := func(r *v1alpha1.OdysseyResource) bool { return r.Labels[labelEnvironment] != "" }
	changed := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return applied(e.Object.(*v1alpha1.OdysseyResource)) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld.(*v1alpha1.OdysseyResource), e.ObjectNew.(*v1alpha1.OdysseyResource)
			return applied(n) && (o.Status != n.Status || o.Spec.Generation != n.Spec.Generation)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("odysseyresource-report").
		For(&v1alpha1.OdysseyResource{}, builder.WithPredicates(changed)).
		Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
			if env, ok := strings.CutPrefix(req.Namespace, "odyssey-"); ok {
				c.ResourceChanged(env, req.Name)
			}
			return reconcile.Result{}, nil
		}))
}
