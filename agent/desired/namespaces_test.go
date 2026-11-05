package desired

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var agentSA = types.NamespacedName{Namespace: "odyssey-system", Name: "odyssey-agent"}

func namespaces(c client.Client, can func(context.Context, string) (bool, error)) *Namespaces {
	return &Namespaces{Client: c, Agent: agentSA, CanUseRole: can}
}

func binding(t *testing.T, c client.Client, ns string) *rbacv1.RoleBinding {
	t.Helper()
	var rb rbacv1.RoleBinding
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: EnvironmentRole}, &rb); err != nil {
		return nil
	}
	return &rb
}

// The agent may keep Secrets and erase data only in the namespaces it
// creates: it binds the environment role there (never cluster-wide) and waits
// until it applies.
func TestEnsureBindsTheEnvironmentRoleInTheEnvironmentNamespace(t *testing.T) {
	ctx := context.Background()
	_, c := newSyncer(t, interceptor.Funcs{})
	checks := 0
	n := namespaces(c, func(_ context.Context, ns string) (bool, error) {
		if ns != Namespace(env) {
			t.Errorf("checked access in %s", ns)
		}
		checks++
		return checks >= 3, nil // the binding takes a moment to apply
	})
	if err := n.Ensure(ctx, org, proj, env); err != nil {
		t.Fatal(err)
	}
	rb := binding(t, c, Namespace(env))
	if rb == nil {
		t.Fatal("no RoleBinding for the environment role")
	}
	if rb.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: EnvironmentRole}) ||
		len(rb.Subjects) != 1 || rb.Subjects[0] != (rbacv1.Subject{Kind: "ServiceAccount", Name: agentSA.Name, Namespace: agentSA.Namespace}) {
		t.Fatalf("binding %+v", rb)
	}
	if checks != 3 {
		t.Errorf("waited for %d checks, want 3", checks)
	}
	// Again (as a concurrent Ensure would): the binding is there, but this
	// caller too waits until it applies before using Secrets.
	checks = 2
	if err := n.Ensure(ctx, org, proj, env); err != nil || checks != 3 {
		t.Fatalf("second ensure: %v (checks %d, want one more)", err, checks)
	}
	// An upgrade binds every namespace without waiting on each.
	checks = 0
	if err := n.BindAll(ctx); err != nil || checks != 0 {
		t.Fatalf("bind all: %v (checks %d)", err, checks)
	}
}

func TestEnsureFailsWhenTheBindingNeverApplies(t *testing.T) {
	defer func(b wait.Backoff) { bindWait = b }(bindWait)
	bindWait.Steps, bindWait.Duration = 2, 1
	_, c := newSyncer(t, interceptor.Funcs{})
	n := namespaces(c, func(context.Context, string) (bool, error) { return false, errors.New("forbidden") })
	err := n.Ensure(context.Background(), org, proj, env)
	if err == nil || !strings.Contains(err.Error(), "did not apply") {
		t.Fatalf("err %v", err)
	}
}

// A namespace of that name the agent did not make is someone else's: no app,
// no Secret and no binding go there.
func TestEnsureRefusesANamespaceItDoesNotManage(t *testing.T) {
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(env)}}
	_, c := newSyncer(t, interceptor.Funcs{}, foreign)
	n := namespaces(c, func(context.Context, string) (bool, error) { return true, nil })
	err := n.Ensure(context.Background(), org, proj, env)
	if !IsRejected(err) || !strings.Contains(err.Error(), "not managed by Odyssey") {
		t.Fatalf("err %v", err)
	}
	if binding(t, c, Namespace(env)) != nil {
		t.Fatal("bound the environment role in a namespace the agent does not manage")
	}
}

// Without a service account (an agent run outside the cluster) nothing is
// bound: its own credentials decide.
func TestEnsureWithoutServiceAccountBindsNothing(t *testing.T) {
	_, c := newSyncer(t, interceptor.Funcs{})
	n := &Namespaces{Client: c}
	if err := n.Ensure(context.Background(), org, proj, env); err != nil {
		t.Fatal(err)
	}
	if binding(t, c, Namespace(env)) != nil {
		t.Fatal("bound without a service account")
	}
}

// After an upgrade (the role used to be bound under another name) every environment
// namespace the agent made gets its binding; no other namespace does.
func TestBindAllCoversOnlyTheAgentsNamespaces(t *testing.T) {
	managed := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{LabelManagedBy: ManagedBy}}}
	}
	_, c := newSyncer(t, interceptor.Funcs{},
		managed("odyssey-e1"), managed("odyssey-e2"),
		managed("kube-system"), // labelled, but not an environment namespace
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "odyssey-e3"}},
	)
	n := namespaces(c, func(context.Context, string) (bool, error) { return true, nil })
	if err := n.BindAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	for ns, want := range map[string]bool{"odyssey-e1": true, "odyssey-e2": true, "kube-system": false, "odyssey-e3": false} {
		if got := binding(t, c, ns) != nil; got != want {
			t.Errorf("%s bound=%v want %v", ns, got, want)
		}
	}
}

type fakeFence struct {
	got []string
	err error
}

func (f *fakeFence) Fence(_ context.Context, ns string) error {
	f.got = append(f.got, ns)
	return f.err
}

// A new namespace is fenced (egress policy) once the agent may write in it,
// before anything runs there; a fence that fails fails Ensure (retried).
func TestEnsureFencesTheNamespace(t *testing.T) {
	ctx := context.Background()
	_, c := newSyncer(t, interceptor.Funcs{})
	f := &fakeFence{}
	n := namespaces(c, func(context.Context, string) (bool, error) { return true, nil })
	n.Fence = f
	if err := n.Ensure(ctx, org, proj, env); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0] != Namespace(env) {
		t.Fatalf("fenced %v", f.got)
	}
	f.err = errors.New("no policy")
	if err := n.Ensure(ctx, org, proj, env); err == nil || !strings.Contains(err.Error(), "no policy") {
		t.Fatalf("a failed fence must fail Ensure: %v", err)
	}
}

// An app that starts or stops using AI re-fences its namespace (odicea-ai
// opens or closes the gateway before the pod starts); one that never used AI
// does not, and a failed fence fails Apply so it is retried, also when the
// retry finds nothing to write.
func TestApplyFencesTheNamespaceWhenAIChanges(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	// Ensure fences before the app exists (and so before its gateway is
	// known); only a fence after the write can open it: this one sees whether
	// the app is already in the cluster when it runs.
	var seen []bool
	var fail error
	f := fencerFunc(func(ctx context.Context, ns string) error {
		var a v1alpha1.OdysseyApp
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "portal"}, &a)
		seen = append(seen, err == nil && a.Spec.AI != nil)
		return fail
	})
	s.Namespaces = &Namespaces{Client: s.Client, Fence: f}
	count := func(f func()) int { seen = nil; f(); return len(seen) }
	apply := func(a *fleetv1.App) error { return s.Apply(ctx, a) }

	if n := count(func() { _ = apply(app(1)) }); n != 1 {
		t.Fatalf("an app without AI is fenced when its namespace is ensured, once: %d", n)
	}
	withAI := app(2)
	withAI.Ai = &fleetv1.AI{BaseUrl: "http://gw:3000", KeySecret: "K"}
	if n := count(func() { _ = apply(withAI) }); n != 2 || !seen[1] {
		t.Fatalf("an app that got AI re-fences once its spec is written: %v", seen)
	}
	fail = errors.New("no resolve")
	if err := s.Apply(ctx, withAI); err == nil {
		t.Fatal("a failed fence must fail Apply")
	}
	fail = nil
	// the retry finds nothing to write and still fences
	if n := count(func() { _ = apply(withAI) }); n != 2 || !seen[1] {
		t.Fatalf("the retry did not fence again: %v", seen)
	}
	// dropping AI re-fences too (odicea-ai closes)
	if n := count(func() { _ = apply(app(3)) }); n != 2 {
		t.Errorf("an app that dropped AI did not re-fence: %d", n)
	}
	if n := count(func() { _ = apply(app(3)) }); n != 1 {
		t.Errorf("an unchanged app without AI fenced again: %d", n)
	}
}

type fencerFunc func(ctx context.Context, namespace string) error

func (f fencerFunc) Fence(ctx context.Context, namespace string) error { return f(ctx, namespace) }
