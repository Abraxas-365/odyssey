package desired

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	return &Namespaces{Client: c, Agent: agentSA, CanUseSecrets: can}
}

func binding(t *testing.T, c client.Client, ns string) *rbacv1.RoleBinding {
	t.Helper()
	var rb rbacv1.RoleBinding
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: SecretsRole}, &rb); err != nil {
		return nil
	}
	return &rb
}

// The agent may keep Secrets only in the namespaces it creates: it binds the
// Secrets role there (never cluster-wide) and waits until it applies.
func TestEnsureBindsTheSecretsRoleInTheEnvironmentNamespace(t *testing.T) {
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
		t.Fatal("no RoleBinding for the Secrets role")
	}
	if rb.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: SecretsRole}) ||
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
		t.Fatal("bound the Secrets role in a namespace the agent does not manage")
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

// After an upgrade (the role used to be cluster-wide) every environment
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
