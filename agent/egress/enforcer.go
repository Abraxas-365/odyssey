package egress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The agent keeps the policy in a ConfigMap of its own namespace and reads the
// cluster's nodes (their addresses are never reachable through the internet
// mode). It reads NetworkPolicies with the API, not a cache, so the grants to
// get and write them are the environment role's, bound only in environment
// namespaces (agent/install/rbac.yaml), and none is cluster-wide. The
// ConfigMap grants are written by hand in rbac.yaml (Role odyssey-agent-policy):
// get and update only on that one name (create cannot be restricted by name).
// +kubebuilder:rbac:groups="",resources=nodes,verbs=list
// It also lists pods by label (odyssey.dev/ai), a read the controllers already
// hold cluster-wide.

// PolicyConfigMap is where the agent keeps the policy it last received (in
// the system namespace): a restart enforces it before the control plane
// answers, and a cluster the control plane cannot reach keeps its fence.
const PolicyConfigMap = "odyssey-cluster-policy"

// ResyncEvery is how often the agent re-checks the policies of every
// namespace (someone may have deleted or edited one).
const ResyncEvery = 2 * time.Minute

// Enforcer keeps the cluster's egress policy and writes it as the
// NetworkPolicies of every environment namespace.
type Enforcer struct {
	// Client writes ConfigMaps and NetworkPolicies.
	Client client.Client
	// API reads without caching (nil: Client): the agent's cache holds no
	// ConfigMaps or NetworkPolicies.
	API client.Reader
	// Config places the cluster's shared pieces; zero values are k3s's.
	Config Config
	// Resolver resolves the AI gateways' hosts (nil: the system's).
	Resolver Resolver
	// OnError receives the errors of the periodic check (nil: dropped).
	OnError func(error)

	mu      sync.Mutex
	state   status
	current Policy
	loaded  bool
}

func (e *Enforcer) reader() client.Reader {
	if e.API != nil {
		return e.API
	}
	return e.Client
}

func (e *Enforcer) key() client.ObjectKey {
	return client.ObjectKey{Namespace: e.Config.WithDefaults().SystemNamespace, Name: PolicyConfigMap}
}

// Current is the policy in force: the last received and kept, else the
// default (internet).
func (e *Enforcer) Current(ctx context.Context) (Policy, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.load(ctx)
}

func (e *Enforcer) load(ctx context.Context) (Policy, error) {
	if e.loaded {
		return e.current, nil
	}
	var cm corev1.ConfigMap
	err := e.reader().Get(ctx, e.key(), &cm)
	switch {
	case apierrors.IsNotFound(err):
		e.current = Default()
	case err != nil:
		return Policy{}, fmt.Errorf("read the cluster policy: %w", err)
	default:
		p, err := fromData(cm.Data)
		if err != nil {
			// Not guessing: a policy that cannot be read must not open
			// the cluster. The control plane sends it again at connect.
			return Policy{}, fmt.Errorf("the kept cluster policy is not valid (%w): the control plane's replaces it", err)
		}
		e.current = p
	}
	e.loaded = true
	return e.current, nil
}

func data(p Policy) map[string]string {
	return map[string]string{
		"generation": strconv.FormatInt(p.Generation, 10),
		"mode":       p.Mode,
		"allow":      strings.Join(p.Strings(), "\n"),
	}
}

func fromData(d map[string]string) (Policy, error) {
	gen, err := strconv.ParseInt(d["generation"], 10, 64)
	if err != nil {
		return Policy{}, errors.New("no generation")
	}
	msg := &fleetv1.ClusterPolicy{Generation: gen, Egress: &fleetv1.EgressPolicy{Mode: d["mode"]}}
	if a := d["allow"]; a != "" {
		msg.Egress.Allow = strings.Split(a, "\n")
	}
	return Parse(msg)
}

// ReceivePolicy keeps and enforces a policy the control plane sent. A policy
// the agent cannot understand is refused before anything changes (the one in
// force stays). One it cannot write everywhere is kept and still retried by
// the periodic check; the error says where it failed.
func (e *Enforcer) ReceivePolicy(ctx context.Context, msg *fleetv1.ClusterPolicy) error {
	p, err := Parse(msg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.keep(ctx, p); err != nil {
		return err
	}
	e.current, e.loaded = p, true
	return e.applyAll(ctx, p)
}

// keep stores p in the ConfigMap when it differs.
func (e *Enforcer) keep(ctx context.Context, p Policy) error {
	want, key := data(p), e.key()
	var cm corev1.ConfigMap
	err := e.reader().Get(ctx, key, &cm)
	switch {
	case apierrors.IsNotFound(err):
		cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{LabelManagedBy: ManagedBy}}, Data: want}
		if err := e.Client.Create(ctx, &cm); err != nil {
			return fmt.Errorf("keep the cluster policy: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("keep the cluster policy: %w", err)
	}
	if equality.Semantic.DeepEqual(cm.Data, want) {
		return nil
	}
	cm.Data = want
	if err := e.Client.Update(ctx, &cm); err != nil {
		return fmt.Errorf("keep the cluster policy: %w", err)
	}
	return nil
}

// Fence writes the namespace's policies under the policy in force. A
// namespace is fenced before its first pod (desired.Namespaces.Ensure).
func (e *Enforcer) Fence(ctx context.Context, namespace string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.load(ctx)
	if err != nil {
		return err
	}
	nodes, err := e.nodes(ctx)
	if err != nil {
		return err
	}
	return e.fenceWith(ctx, namespace, p, nodes)
}

func (e *Enforcer) write(ctx context.Context, want *networkingv1.NetworkPolicy) error {
	var cur networkingv1.NetworkPolicy
	err := e.reader().Get(ctx, client.ObjectKeyFromObject(want), &cur)
	switch {
	case apierrors.IsNotFound(err):
		return e.Client.Create(ctx, want)
	case err != nil:
		return err
	}
	if equality.Semantic.DeepEqual(cur.Spec, want.Spec) && cur.Labels[LabelManagedBy] == ManagedBy {
		return nil
	}
	cur.Spec = want.Spec
	if cur.Labels == nil {
		cur.Labels = map[string]string{}
	}
	cur.Labels[LabelManagedBy] = ManagedBy
	return e.Client.Update(ctx, &cur)
}

// Namespaces lists the environment namespaces (the ones the agent created
// for environments and that are not going away).
func (e *Enforcer) Namespaces(ctx context.Context) ([]string, error) {
	var list corev1.NamespaceList
	if err := e.reader().List(ctx, &list, client.MatchingLabels{LabelManagedBy: NamespaceManagedBy}); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var out []string
	for _, ns := range list.Items {
		if strings.HasPrefix(ns.Name, NamespacePrefix) && ns.DeletionTimestamp == nil {
			out = append(out, ns.Name)
		}
	}
	return out, nil
}

// status is what the last application of the policy left: what the control
// plane is told (an ack now, and again with every full report).
type status struct {
	// failure is why the policy could not be written everywhere ("": it was).
	failure string
	// warnings are what TI should review (Warnings).
	warnings []string
}

// Status is the generation in force, why it is not (fully) applied (empty:
// it is) and what TI should review. Generation 0: no policy received yet.
func (e *Enforcer) Status() (generation int64, failure string, warnings []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.loaded {
		return 0, "", nil
	}
	return e.current.Generation, e.state.failure, slices.Clone(e.state.warnings)
}

// nodes are the addresses of the cluster's nodes (InternalIP and ExternalIP).
func (e *Enforcer) nodes(ctx context.Context) ([]netip.Addr, error) {
	var list corev1.NodeList
	if err := e.reader().List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list the cluster's nodes: %w", err)
	}
	var out []netip.Addr
	for _, n := range list.Items {
		for _, a := range n.Status.Addresses {
			if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
				continue
			}
			if addr, err := netip.ParseAddr(a.Address); err == nil {
				out = append(out, addr.Unmap())
			}
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

func (e *Enforcer) applyAll(ctx context.Context, p Policy) error {
	names, err := e.Namespaces(ctx)
	if err != nil {
		return err
	}
	nodes, err := e.nodes(ctx)
	if err != nil {
		// Not guessing: without the nodes' addresses the internet rule would
		// leave them reachable. The policies stay as they are; retried.
		e.state.failure = err.Error()
		return fmt.Errorf("the policy is kept but not applied (retried): %w", err)
	}
	e.state.warnings = Warnings(p, nodes)
	var errs []error
	for _, ns := range names {
		// A namespace deleted meanwhile is not a failure.
		if err := e.fenceWith(ctx, ns, p, nodes); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	e.state.failure = ""
	if err := errors.Join(errs...); err != nil {
		e.state.failure = err.Error()
		return fmt.Errorf("the policy is kept but not applied everywhere (retried): %w", err)
	}
	return nil
}

// Resync applies the policy in force to every environment namespace.
func (e *Enforcer) Resync(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.load(ctx)
	if err != nil {
		return err
	}
	return e.applyAll(ctx, p)
}

// Start fences every environment namespace under the policy in force (the
// default, internet, when none was received) and keeps checking: a policy
// someone deleted comes back. It is a manager.Runnable (the leader runs it).
func (e *Enforcer) Start(ctx context.Context) error {
	for {
		wait := ResyncEvery
		if err := e.Resync(ctx); err != nil {
			wait = retryAfter // e.g. the environment role is not bound yet
			if e.OnError != nil {
				e.OnError(err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// retryAfter is the wait after a failed check.
const retryAfter = 15 * time.Second

// fenceWith writes the namespace's policies: the cluster policy's, with the
// nodes it must keep closed and the AI gateways its apps need open.
func (e *Enforcer) fenceWith(ctx context.Context, namespace string, p Policy, nodes []netip.Addr) error {
	env := Environment{Nodes: nodes}
	gateways, gerr := e.gateways(ctx, namespace)
	// A gateway that does not resolve (or an app list that cannot be read)
	// leaves odicea-ai exactly as it is, even for the gateways that did
	// resolve: the apps that reach it now keep doing so and a partial list
	// must not narrow it; the next check looks again.
	if gerr == nil {
		env.AI = gateways
	}
	var errs []error
	for _, want := range e.Config.Policies(namespace, p, env) {
		if err := e.write(ctx, want); err != nil {
			errs = append(errs, fmt.Errorf("fence %s: %w", namespace, err))
		}
	}
	switch {
	case gerr != nil:
		errs = append(errs, fmt.Errorf("fence %s: %w", namespace, gerr))
	case len(gateways) == 0:
		// The apps dropped AI, but their old pods (still labelled, still
		// holding the key) may be mid-rollout: odicea-ai goes only once no
		// pod that carries the label is left; the next check removes it.
		busy, err := e.aiPodsRunning(ctx, namespace)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("fence %s: %w", namespace, err))
		case !busy:
			if err := e.remove(ctx, namespace, AIPolicy); err != nil {
				errs = append(errs, fmt.Errorf("fence %s: %w", namespace, err))
			}
		}
	}
	return errors.Join(errs...)
}

// aiPodsRunning says whether a pod that carries the AI label is still alive
// in the namespace (not finished: Succeeded and Failed pods reach nothing).
// It reads without the cache (the agent's cache sees only the pods it owns).
func (e *Enforcer) aiPodsRunning(ctx context.Context, namespace string) (bool, error) {
	var pods corev1.PodList
	if err := e.reader().List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{LabelAI: "true"}); err != nil {
		return false, fmt.Errorf("list the pods with AI of %s: %w", namespace, err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			return true, nil
		}
	}
	return false, nil
}

// remove deletes a policy the agent made (one it did not make is left alone).
func (e *Enforcer) remove(ctx context.Context, namespace, name string) error {
	var cur networkingv1.NetworkPolicy
	err := e.reader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case cur.Labels[LabelManagedBy] != ManagedBy:
		return nil
	}
	return client.IgnoreNotFound(e.Client.Delete(ctx, &cur))
}
