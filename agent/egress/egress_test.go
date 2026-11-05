package egress

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func policy(gen int64, mode string, allow ...string) *fleetv1.ClusterPolicy {
	return &fleetv1.ClusterPolicy{Generation: gen, Egress: &fleetv1.EgressPolicy{Mode: mode, Allow: allow}}
}

func TestParse(t *testing.T) {
	p, err := Parse(policy(3, "", "10.20.0.7/16", "203.0.113.9", "10.20.0.0/16", "2001:db8::1/64", "::ffff:198.51.100.0/120"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.20.0.0/16", "198.51.100.0/24", "203.0.113.9/32", "2001:db8::/64"}
	if p.Mode != ModeInternet || p.Generation != 3 || !slices.Equal(p.Strings(), want) {
		t.Fatalf("parsed %+v %v, want %v", p, p.Strings(), want)
	}
	for name, msg := range map[string]*fleetv1.ClusterPolicy{
		"unknown mode": policy(1, "open"),
		"not a cidr":   policy(1, "restricted", "example.com"),
		"open v4":      policy(1, "restricted", "0.0.0.0/0"),
		"open v6":      policy(1, "internet", "::/0"),
		"mapped open":  policy(1, "internet", "::ffff:0.0.0.0/96"),
		"too many":     policy(1, "internet", make([]string, maxAllow+1)...),
	} {
		if _, err := Parse(msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if p, err := Parse(&fleetv1.ClusterPolicy{Generation: 2}); err != nil || p.Mode != ModeInternet || len(p.Allow) != 0 {
		t.Errorf("a policy without egress is the default: %+v %v", p, err)
	}
}

func cidrs(rule networkingv1.NetworkPolicyEgressRule) (blocks []string, except []string) {
	for _, to := range rule.To {
		if to.IPBlock != nil {
			blocks = append(blocks, to.IPBlock.CIDR)
			except = append(except, to.IPBlock.Except...)
		}
	}
	return
}

// the apps policy: always DNS, the namespace, the gate; the internet only by
// mode and never the private ranges; the allow list in both.
func TestAppsPolicy(t *testing.T) {
	cfg := Config{}
	internet := cfg.AppsNetworkPolicy("odyssey-e", Default(), Environment{})
	if internet.Name != AppsPolicy || internet.Namespace != "odyssey-e" || internet.Labels[LabelManagedBy] != ManagedBy {
		t.Fatalf("meta %+v", internet.ObjectMeta)
	}
	sel := internet.Spec.PodSelector.MatchExpressions
	if len(sel) != 1 || sel[0].Key != LabelApp || sel[0].Operator != metav1.LabelSelectorOpExists {
		t.Fatalf("selects %+v: the pods of apps", internet.Spec.PodSelector)
	}
	if len(internet.Spec.Ingress) != 2 || internet.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "kube-system" ||
		internet.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "traefik" {
		t.Fatalf("ingress %+v", internet.Spec.Ingress)
	}
	e := internet.Spec.Egress
	if len(e) != 5 {
		t.Fatalf("internet mode has %d egress rules, want dns, namespace, gate, v4, v6: %+v", len(e), e)
	}
	if e[0].Ports[0].Port.IntValue() != 53 || e[2].Ports[0].Port.IntValue() != GatePort {
		t.Errorf("dns/gate ports %+v %+v", e[0].Ports, e[2].Ports)
	}
	v4, ex4 := cidrs(e[3])
	v6, ex6 := cidrs(e[4])
	if !slices.Equal(v4, []string{"0.0.0.0/0"}) || !slices.Equal(v6, []string{"::/0"}) {
		t.Fatalf("internet blocks %v %v", v4, v6)
	}
	for _, must := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4"} {
		if !slices.Contains(ex4, must) {
			t.Errorf("internet mode does not except %s", must)
		}
	}
	for _, must := range []string{"fc00::/7", "fe80::/10", "::1/128", "64:ff9b::/96", "ff00::/8"} {
		if !slices.Contains(ex6, must) {
			t.Errorf("internet mode does not except %s", must)
		}
	}

	allow, _ := Parse(policy(1, "restricted", "10.20.0.0/16", "203.0.113.9"))
	restricted := cfg.AppsNetworkPolicy("odyssey-e", allow, Environment{}).Spec.Egress
	if len(restricted) != 4 {
		t.Fatalf("restricted has %d egress rules, want dns, namespace, gate, allow: %+v", len(restricted), restricted)
	}
	if got, ex := cidrs(restricted[3]); !slices.Equal(got, []string{"10.20.0.0/16", "203.0.113.9/32"}) || len(ex) != 0 {
		t.Errorf("allow rule %v except %v", got, ex)
	}
	for _, r := range restricted {
		if b, _ := cidrs(r); slices.Contains(b, "0.0.0.0/0") {
			t.Error("restricted mode opens the internet")
		}
	}

	both, _ := Parse(policy(1, "internet", "10.20.0.0/16"))
	if got := cfg.AppsNetworkPolicy("odyssey-e", both, Environment{}).Spec.Egress; len(got) != 6 {
		t.Errorf("internet plus allow: %d rules", len(got))
	}
}

func TestConfigurablePlaces(t *testing.T) {
	c := Config{IngressNamespace: "ingress", IngressPodLabels: map[string]string{"app": "nginx"}, DNSNamespace: "dns", DNSPodLabels: map[string]string{"app": "coredns"}, OperatorNamespace: "db", SystemNamespace: "sys"}
	apps := c.AppsNetworkPolicy("odyssey-e", Default(), Environment{})
	in := apps.Spec.Ingress[0].From[0]
	if in.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "ingress" || in.PodSelector.MatchLabels["app"] != "nginx" {
		t.Errorf("ingress %+v", in)
	}
	dns := apps.Spec.Egress[0].To[0]
	if dns.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "dns" || dns.PodSelector.MatchLabels["app"] != "coredns" {
		t.Errorf("dns %+v", dns)
	}
	if gate := apps.Spec.Egress[2].To[0]; gate.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "sys" {
		t.Errorf("gate %+v", gate)
	}
	res := c.ResourcesNetworkPolicy("odyssey-e")
	if op := res.Spec.Ingress[1].From[0]; op.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "db" {
		t.Errorf("operator %+v", op)
	}
}

func TestResourcesPolicy(t *testing.T) {
	p := Config{}.ResourcesNetworkPolicy("odyssey-e")
	sel := p.Spec.PodSelector.MatchExpressions
	if p.Name != ResourcesPolicy || len(sel) != 1 || sel[0].Key != LabelResource || sel[0].Operator != metav1.LabelSelectorOpExists {
		t.Fatalf("selects %+v: only the pods that carry the resource label", p.Spec.PodSelector)
	}
	if len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress || p.Spec.Egress != nil {
		t.Errorf("resources must not restrict egress: %+v", p.Spec)
	}
	if len(p.Spec.Ingress) != 2 || p.Spec.Ingress[1].From[0].NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "cnpg-system" {
		t.Errorf("ingress %+v", p.Spec.Ingress)
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func envNamespace(name string, managed bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if managed {
		ns.Labels = map[string]string{LabelManagedBy: NamespaceManagedBy}
	}
	return ns
}

func newEnforcer(t *testing.T, f interceptor.Funcs, objs ...client.Object) (*Enforcer, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).WithInterceptorFuncs(f).Build()
	return &Enforcer{Client: c}, c
}

func policyIn(t *testing.T, c client.Client, ns, name string) *networkingv1.NetworkPolicy {
	t.Helper()
	var p networkingv1.NetworkPolicy
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &p); err != nil {
		return nil
	}
	return &p
}

// without a policy received, every environment namespace is fenced with the
// default (internet); other namespaces are left alone.
func TestStartFencesEveryEnvironmentNamespaceWithTheDefault(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), envNamespace("odyssey-b", true),
		envNamespace("odyssey-builds", false), envNamespace("odyssey-system", false), envNamespace("kube-system", false),
		envNamespace("odyssey-gone", true))
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"odyssey-a", "odyssey-b", "odyssey-gone"} {
		for _, name := range []string{AppsPolicy, ResourcesPolicy, DefaultPolicy} {
			if policyIn(t, c, ns, name) == nil {
				t.Errorf("%s has no %s", ns, name)
			}
		}
	}
	for _, ns := range []string{"odyssey-builds", "odyssey-system", "kube-system"} {
		if policyIn(t, c, ns, AppsPolicy) != nil || policyIn(t, c, ns, ResourcesPolicy) != nil {
			t.Errorf("%s was fenced: it is not an environment", ns)
		}
	}
	if cur, err := e.Current(ctx); err != nil || cur.Mode != ModeInternet || cur.Generation != 0 {
		t.Errorf("current %+v %v", cur, err)
	}
}

func TestReceivePolicyKeepsItAndRewritesEveryNamespace(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), envNamespace("odyssey-b", true))
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.ReceivePolicy(ctx, policy(4, "restricted", "10.20.0.0/16")); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"odyssey-a", "odyssey-b"} {
		p := policyIn(t, c, ns, AppsPolicy)
		if p == nil || len(p.Spec.Egress) != 4 {
			t.Fatalf("%s: apps policy %+v", ns, p)
		}
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "odyssey-system", Name: PolicyConfigMap}, &cm); err != nil ||
		cm.Data["mode"] != "restricted" || cm.Data["generation"] != "4" || cm.Data["allow"] != "10.20.0.0/16" {
		t.Fatalf("kept %+v %v", cm.Data, err)
	}
	// a restart enforces what was kept, before the control plane answers
	again := &Enforcer{Client: c}
	if cur, err := again.Current(ctx); err != nil || cur.Mode != ModeRestricted || cur.Generation != 4 || !slices.Equal(cur.Strings(), []string{"10.20.0.0/16"}) {
		t.Fatalf("after a restart %+v %v", cur, err)
	}
	// a namespace made now is fenced with it before its first pod
	if err := c.Create(ctx, envNamespace("odyssey-new", true)); err != nil {
		t.Fatal(err)
	}
	if err := again.Fence(ctx, "odyssey-new"); err != nil {
		t.Fatal(err)
	}
	if p := policyIn(t, c, "odyssey-new", AppsPolicy); p == nil || len(p.Spec.Egress) != 4 {
		t.Errorf("new namespace %+v", p)
	}
	// back to internet
	if err := again.ReceivePolicy(ctx, policy(5, "internet")); err != nil {
		t.Fatal(err)
	}
	if p := policyIn(t, c, "odyssey-a", AppsPolicy); len(p.Spec.Egress) != 5 {
		t.Errorf("internet again: %d rules", len(p.Spec.Egress))
	}
}

func TestRefusedPolicyChangesNothing(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true))
	if err := e.ReceivePolicy(ctx, policy(1, "restricted")); err != nil {
		t.Fatal(err)
	}
	if err := e.ReceivePolicy(ctx, policy(2, "open")); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	if cur, _ := e.Current(ctx); cur.Mode != ModeRestricted || cur.Generation != 1 {
		t.Errorf("current %+v: the refused policy must not replace it", cur)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "odyssey-system", Name: PolicyConfigMap}, &cm); err != nil || cm.Data["generation"] != "1" {
		t.Errorf("kept %+v", cm.Data)
	}
}

// a policy that cannot be written in a namespace is kept (and so enforced in
// the others and in the next check) and the error says where.
func TestPolicyKeptButNotAppliedEverywhere(t *testing.T) {
	ctx := context.Background()
	fail := true
	e, c := newEnforcer(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if np, ok := obj.(*networkingv1.NetworkPolicy); ok && np.Namespace == "odyssey-b" && fail {
			return errors.New("forbidden")
		}
		return cl.Create(ctx, obj, opts...)
	}}, envNamespace("odyssey-a", true), envNamespace("odyssey-b", true))
	err := e.ReceivePolicy(ctx, policy(1, "restricted"))
	if err == nil || !strings.Contains(err.Error(), "odyssey-b") {
		t.Fatalf("error %v, want it to name odyssey-b", err)
	}
	if policyIn(t, c, "odyssey-a", AppsPolicy) == nil {
		t.Error("the namespace that could be fenced was not")
	}
	fail = false
	if err := e.Resync(ctx); err != nil {
		t.Fatalf("the next check: %v", err)
	}
	if policyIn(t, c, "odyssey-b", AppsPolicy) == nil {
		t.Error("the check did not fence the namespace it missed")
	}
}

// a policy someone deleted or edited comes back at the next check.
func TestResyncRestoresAnEditedPolicy(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true))
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	p := policyIn(t, c, "odyssey-a", AppsPolicy)
	p.Spec.Egress = nil
	if err := c.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, policyIn(t, c, "odyssey-a", ResourcesPolicy)); err != nil {
		t.Fatal(err)
	}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := policyIn(t, c, "odyssey-a", AppsPolicy); len(got.Spec.Egress) != 5 {
		t.Errorf("edited policy not restored: %+v", got.Spec.Egress)
	}
	if policyIn(t, c, "odyssey-a", ResourcesPolicy) == nil {
		t.Error("deleted policy not restored")
	}
}

// a kept policy that cannot be read does not open the cluster: Fence refuses
// until the control plane sends a new one.
func TestUnreadableKeptPolicyFailsClosed(t *testing.T) {
	ctx := context.Background()
	bad := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "odyssey-system", Name: PolicyConfigMap}, Data: map[string]string{"mode": "weird"}}
	e, _ := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), bad)
	if err := e.Fence(ctx, "odyssey-a"); err == nil {
		t.Fatal("fenced with a policy it could not read")
	}
	if err := e.ReceivePolicy(ctx, policy(1, "internet")); err != nil {
		t.Fatalf("a new policy replaces the unreadable one: %v", err)
	}
	if err := e.Fence(ctx, "odyssey-a"); err != nil {
		t.Fatal(err)
	}
}
