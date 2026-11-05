package egress

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type fakeResolver struct {
	hosts map[string][]netip.Addr
	calls int
	err   error
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hosts[host], nil
}

func node(name, internal, external string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if internal != "" {
		n.Status.Addresses = append(n.Status.Addresses, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: internal})
	}
	if external != "" {
		n.Status.Addresses = append(n.Status.Addresses, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: external})
	}
	n.Status.Addresses = append(n.Status.Addresses, corev1.NodeAddress{Type: corev1.NodeHostName, Address: name})
	return n
}

func aiApp(ns, name, baseURL string) *v1alpha1.OdysseyApp {
	a := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if baseURL != "" {
		a.Spec.AI = &v1alpha1.AISpec{BaseURL: baseURL, KeySecret: "K"}
	}
	return a
}

func TestGatewayOf(t *testing.T) {
	for _, tc := range []struct {
		url  string
		host string
		port int32
		bad  bool
	}{
		{url: "http://host.k3d.internal:3000", host: "host.k3d.internal", port: 3000},
		{url: "https://Gateway.Example.com", host: "gateway.example.com", port: 443},
		{url: "http://gw", host: "gw", port: 80},
		{url: "http://[2001:db8::1]:8080/x", host: "2001:db8::1", port: 8080},
		{url: "ftp://gw", bad: true},
		{url: "http://gw:0", bad: true},
		{url: "http://gw:99999", bad: true},
		{url: "gw", bad: true},
		{url: "", bad: true},
	} {
		host, port, err := gatewayOf(tc.url)
		if tc.bad != (err != nil) || host != tc.host || port != tc.port {
			t.Errorf("%q: %q %d %v", tc.url, host, port, err)
		}
	}
}

func TestServiceNamespace(t *testing.T) {
	for host, want := range map[string]string{
		"freerouter.ai.svc": "ai", "freerouter.ai.svc.cluster.local": "ai", "freerouter.ai.svc.cluster.local.": "ai",
		"freerouter.svc": "", "svc": "", "a.b.c.svc": "", "gateway.example.com": "", "host.k3d.internal": "", "x.svc.example.com": "",
	} {
		if got := serviceNamespace(host); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
}

// the internet mode keeps every node address closed (the kubelet, the API on
// the node's IP, the NodePorts), and the new reserved ranges.
func TestNodesAreExceptedFromTheInternet(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true),
		node("n1", "10.1.1.4", "203.0.113.7"), node("n2", "2001:db8::4", ""), node("n3", "192.168.65.3", ""))
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	rules := policyIn(t, c, "odyssey-a", AppsPolicy).Spec.Egress
	v4, ex4 := cidrs(rules[3])
	v6, ex6 := cidrs(rules[4])
	if !slices.Equal(v4, []string{"0.0.0.0/0"}) || !slices.Equal(v6, []string{"::/0"}) {
		t.Fatalf("%v %v", v4, v6)
	}
	if !slices.Contains(ex4, "203.0.113.7/32") {
		t.Errorf("a public node address is reachable: %v", ex4)
	}
	if slices.Contains(ex4, "10.1.1.4/32") || slices.Contains(ex4, "192.168.65.3/32") {
		t.Errorf("an address inside a reserved range is listed twice: %v", ex4)
	}
	if !slices.Contains(ex6, "2001:db8::4/128") {
		t.Errorf("a v6 node address is reachable: %v", ex6)
	}
	// a node that joins is excepted at the next check
	if err := c.Create(ctx, node("n4", "198.51.100.9", "")); err != nil {
		t.Fatal(err)
	}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	_, ex4 = cidrs(policyIn(t, c, "odyssey-a", AppsPolicy).Spec.Egress[3])
	if !slices.Contains(ex4, "198.51.100.9/32") {
		t.Errorf("the new node is not excepted: %v", ex4)
	}
}

// without the nodes' addresses the internet rule would leave them open: the
// policies are not rewritten and the status says why.
func TestNodesUnreadableKeepsThePoliciesAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	fail := false
	e, c := newEnforcer(t, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if _, ok := l.(*corev1.NodeList); ok && fail {
			return errors.New("forbidden")
		}
		return cl.List(ctx, l, o...)
	}}, envNamespace("odyssey-a", true), node("n1", "203.0.113.7", ""))
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := e.Resync(ctx); err == nil || !strings.Contains(err.Error(), "nodes") {
		t.Fatalf("error %v", err)
	}
	if _, failure, _ := e.Status(); !strings.Contains(failure, "nodes") {
		t.Errorf("failure %q", failure)
	}
	if err := e.Fence(ctx, "odyssey-a"); err == nil {
		t.Error("fenced without the nodes")
	}
	_, ex := cidrs(policyIn(t, c, "odyssey-a", AppsPolicy).Spec.Egress[3])
	if !slices.Contains(ex, "203.0.113.7/32") {
		t.Errorf("the policy in force lost its node: %v", ex)
	}
	fail = false
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, failure, _ := e.Status(); failure != "" {
		t.Errorf("failure %q after it recovered", failure)
	}
}

// restricted mode: an allow entry that includes a node is kept and warned.
func TestAllowIncludingANodeIsKeptAndWarned(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), node("n1", "10.1.1.4", ""), node("n2", "172.20.0.2", ""))
	if err := e.ReceivePolicy(ctx, policy(3, "restricted", "10.1.0.0/16", "198.51.100.0/24")); err != nil {
		t.Fatal(err)
	}
	gen, failure, warnings := e.Status()
	if gen != 3 || failure != "" || len(warnings) != 1 || !strings.Contains(warnings[0], "10.1.0.0/16") || !strings.Contains(warnings[0], "10.1.1.4") {
		t.Fatalf("status %d %q %v", gen, failure, warnings)
	}
	if got, _ := cidrs(policyIn(t, c, "odyssey-a", AppsPolicy).Spec.Egress[3]); !slices.Equal(got, []string{"10.1.0.0/16", "198.51.100.0/24"}) {
		t.Errorf("the entry was dropped: %v", got)
	}
	// no entry includes a node: no warning
	if err := e.ReceivePolicy(ctx, policy(4, "restricted", "198.51.100.0/24")); err != nil {
		t.Fatal(err)
	}
	if _, _, w := e.Status(); len(w) != 0 {
		t.Errorf("warnings %v", w)
	}
}

func TestWarningsAreBounded(t *testing.T) {
	var allow []string
	var nodes []netip.Addr
	for i := range 30 {
		allow = append(allow, netip.AddrFrom4([4]byte{10, byte(i), 0, 1}).String())
		nodes = append(nodes, netip.AddrFrom4([4]byte{10, byte(i), 0, 1}))
	}
	p, err := Parse(policy(1, "restricted", allow...))
	if err != nil {
		t.Fatal(err)
	}
	if got := Warnings(p, nodes); len(got) != maxWarnings {
		t.Errorf("%d warnings", len(got))
	}
}

// odicea-ai: the pods labelled for AI reach the gateway the apps name, and
// nothing else; it goes away when no app uses AI.
func TestAIPolicyOpensTheGatewayOfTheAppsWithAI(t *testing.T) {
	ctx := context.Background()
	r := &fakeResolver{hosts: map[string][]netip.Addr{
		"host.k3d.internal": {netip.MustParseAddr("192.168.65.254")},
		"gw.example.com":    {netip.MustParseAddr("203.0.113.20"), netip.MustParseAddr("2001:db8::20"), netip.MustParseAddr("203.0.113.20")},
	}}
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), envNamespace("odyssey-b", true),
		aiApp("odyssey-a", "web", "http://host.k3d.internal:3000"), aiApp("odyssey-a", "worker", "http://host.k3d.internal:3000"),
		aiApp("odyssey-a", "api", "https://gw.example.com"), aiApp("odyssey-a", "plain", ""),
		aiApp("odyssey-b", "plain", ""))
	e.Resolver = r
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	p := policyIn(t, c, "odyssey-a", AIPolicy)
	if p == nil {
		t.Fatal("no odicea-ai in the namespace with AI apps")
	}
	if p.Spec.PodSelector.MatchLabels[LabelAI] != "true" || len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("selector %+v types %v", p.Spec.PodSelector, p.Spec.PolicyTypes)
	}
	type rule struct {
		cidrs []string
		port  int
	}
	var got []rule
	for _, er := range p.Spec.Egress {
		cs, _ := cidrs(er)
		got = append(got, rule{cs, er.Ports[0].Port.IntValue()})
	}
	want := []rule{{[]string{"192.168.65.254/32"}, 3000}, {[]string{"203.0.113.20/32", "2001:db8::20/128"}, 443}}
	if len(got) != 2 || !slices.Equal(got[0].cidrs, want[0].cidrs) || got[0].port != 3000 || !slices.Equal(got[1].cidrs, want[1].cidrs) || got[1].port != 443 {
		t.Errorf("rules %+v, want %+v", got, want)
	}
	if policyIn(t, c, "odyssey-b", AIPolicy) != nil {
		t.Error("a namespace without AI has odicea-ai")
	}

	// the app drops AI: the policy goes away at the next check
	var a v1alpha1.OdysseyApp
	for _, n := range []string{"web", "worker", "api"} {
		if err := c.Get(ctx, client.ObjectKey{Namespace: "odyssey-a", Name: n}, &a); err != nil {
			t.Fatal(err)
		}
		a.Spec.AI = nil
		if err := c.Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if policyIn(t, c, "odyssey-a", AIPolicy) != nil {
		t.Error("odicea-ai stayed with no app using AI")
	}
}

func TestAIPolicyForAnInClusterService(t *testing.T) {
	ctx := context.Background()
	r := &fakeResolver{}
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), aiApp("odyssey-a", "web", "http://freerouter.gateway.svc.cluster.local:3000"))
	e.Resolver = r
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	p := policyIn(t, c, "odyssey-a", AIPolicy)
	if p == nil || len(p.Spec.Egress) != 1 {
		t.Fatalf("policy %+v", p)
	}
	peer := p.Spec.Egress[0].To[0]
	if peer.IPBlock != nil || peer.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "gateway" {
		t.Errorf("peer %+v", peer)
	}
	// the Service's port is not the pod's: the namespace on any port, and
	// never Port 0 (the API server refuses it)
	if p.Spec.Egress[0].Ports != nil {
		t.Errorf("ports %+v", p.Spec.Egress[0].Ports)
	}
	if r.calls != 0 {
		t.Error("a Service host was resolved")
	}
}

// a gateway that does not resolve leaves odicea-ai as it is (the apps that
// reach it now keep doing so), reports the failure and recovers.
func TestAIGatewayThatDoesNotResolveKeepsThePolicy(t *testing.T) {
	ctx := context.Background()
	r := &fakeResolver{hosts: map[string][]netip.Addr{"gw.example.com": {netip.MustParseAddr("203.0.113.20")}}}
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), aiApp("odyssey-a", "web", "https://gw.example.com"))
	e.Resolver = r
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	r.err = errors.New("no such host")
	if err := e.Resync(ctx); err == nil || !strings.Contains(err.Error(), "gw.example.com") {
		t.Fatalf("error %v", err)
	}
	if _, failure, _ := e.Status(); !strings.Contains(failure, "gw.example.com") {
		t.Errorf("failure %q", failure)
	}
	if p := policyIn(t, c, "odyssey-a", AIPolicy); p == nil {
		t.Fatal("the policy was removed because the name did not resolve")
	}
	// the gateway moves: it is followed at the next check
	r.err = nil
	r.hosts["gw.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.99")}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := cidrs(policyIn(t, c, "odyssey-a", AIPolicy).Spec.Egress[0]); !slices.Equal(got, []string{"203.0.113.99/32"}) {
		t.Errorf("the gateway's new address is not followed: %v", got)
	}
	if _, failure, _ := e.Status(); failure != "" {
		t.Errorf("failure %q after recovering", failure)
	}
}

// one gateway resolves and another does not: odicea-ai is not written at all
// (a partial list would narrow what the apps reach now).
func TestPartialGatewayResolutionLeavesTheAIPolicyAsItWas(t *testing.T) {
	ctx := context.Background()
	r := &fakeResolver{hosts: map[string][]netip.Addr{
		"a.example.com": {netip.MustParseAddr("203.0.113.1")},
		"b.example.com": {netip.MustParseAddr("203.0.113.2")},
	}}
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true),
		aiApp("odyssey-a", "a", "https://a.example.com"), aiApp("odyssey-a", "b", "https://b.example.com"))
	e.Resolver = r
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if p := policyIn(t, c, "odyssey-a", AIPolicy); p == nil || len(p.Spec.Egress) != 2 {
		t.Fatalf("policy %+v", p)
	}
	// b moves to a host with no addresses; a still resolves
	var b v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKey{Namespace: "odyssey-a", Name: "b"}, &b); err != nil {
		t.Fatal(err)
	}
	b.Spec.AI.BaseURL = "https://c.example.com"
	if err := c.Update(ctx, &b); err != nil {
		t.Fatal(err)
	}
	if err := e.Resync(ctx); err == nil || !strings.Contains(err.Error(), "c.example.com") {
		t.Fatalf("error %v", err)
	}
	p := policyIn(t, c, "odyssey-a", AIPolicy)
	if p == nil || len(p.Spec.Egress) != 2 {
		t.Fatalf("the partial list narrowed the policy: %+v", p)
	}
	if got, _ := cidrs(p.Spec.Egress[1]); !slices.Equal(got, []string{"203.0.113.2/32"}) {
		t.Errorf("rules %v", got)
	}
}

func aiPod(ns, name string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{LabelAI: "true"}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func dropAI(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	var a v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &a); err != nil {
		t.Fatal(err)
	}
	a.Spec.AI = nil
	if err := c.Update(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
}

// the apps drop AI while their old pods (still labelled, still holding the
// key) roll out: odicea-ai stays until none is running, then goes.
func TestAIPolicyStaysWhileAPodWithAIRuns(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), envNamespace("odyssey-b", true),
		aiApp("odyssey-a", "web", "http://10.0.0.5:8080"),
		aiPod("odyssey-a", "old", corev1.PodRunning), aiPod("odyssey-a", "done", corev1.PodSucceeded),
		// another namespace's pod does not hold this one's policy
		aiPod("odyssey-b", "other", corev1.PodRunning))
	e.Resolver = &fakeResolver{}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	dropAI(t, c, "odyssey-a", "web")
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if policyIn(t, c, "odyssey-a", AIPolicy) == nil {
		t.Fatal("odicea-ai was removed while a pod with the key still runs")
	}
	if policyIn(t, c, "odyssey-b", AIPolicy) != nil {
		t.Error("a namespace without AI apps has odicea-ai")
	}
	var old corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "odyssey-a", Name: "old"}, &old); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if policyIn(t, c, "odyssey-a", AIPolicy) != nil {
		t.Error("odicea-ai stayed after the last pod with AI ended")
	}
}

// pods that cannot be listed keep the policy and say why.
func TestAIPolicyStaysWhenThePodsCannotBeListed(t *testing.T) {
	ctx := context.Background()
	fail := false
	e, c := newEnforcer(t, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if _, ok := l.(*corev1.PodList); ok && fail {
			return errors.New("forbidden")
		}
		return cl.List(ctx, l, o...)
	}}, envNamespace("odyssey-a", true), aiApp("odyssey-a", "web", "http://10.0.0.5:8080"))
	e.Resolver = &fakeResolver{}
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	dropAI(t, c, "odyssey-a", "web")
	fail = true
	if err := e.Resync(ctx); err == nil || !strings.Contains(err.Error(), "pods") {
		t.Fatalf("error %v", err)
	}
	if policyIn(t, c, "odyssey-a", AIPolicy) == nil {
		t.Error("odicea-ai removed without knowing whether pods use it")
	}
}

// every port a builder emits is one the API server accepts (1..65535): Port 0
// made it refuse odicea-ai for an in-cluster Service.
func TestEveryEmittedPortIsValid(t *testing.T) {
	nodes := []netip.Addr{netip.MustParseAddr("203.0.113.7")}
	gateways := []AIGateway{
		{Service: "gateway"},
		{Addrs: []netip.Addr{netip.MustParseAddr("192.168.65.254")}, Port: 3000},
		{Addrs: []netip.Addr{netip.MustParseAddr("203.0.113.20")}, Port: 0}, // never emitted
		{Service: "other", Addrs: []netip.Addr{netip.MustParseAddr("10.0.0.5")}, Port: 8080},
	}
	for _, mode := range []string{ModeInternet, ModeRestricted} {
		p, err := Parse(policy(1, mode, "198.51.100.0/24"))
		if err != nil {
			t.Fatal(err)
		}
		pols := Config{}.Policies("odyssey-a", p, Environment{Nodes: nodes, AI: gateways})
		if len(pols) != 4 {
			t.Fatalf("%d policies", len(pols))
		}
		for _, np := range pols {
			for _, r := range np.Spec.Egress {
				for _, pp := range r.Ports {
					if pp.Port == nil || pp.Port.IntValue() < 1 || pp.Port.IntValue() > 65535 {
						t.Errorf("%s/%s: port %+v", mode, np.Name, pp)
					}
				}
			}
			for _, r := range np.Spec.Ingress {
				for _, pp := range r.Ports {
					if pp.Port == nil || pp.Port.IntValue() < 1 || pp.Port.IntValue() > 65535 {
						t.Errorf("%s/%s: port %+v", mode, np.Name, pp)
					}
				}
			}
		}
	}
}

// CloudNativePG pods never fall under odicea-default, labelled or not.
func TestDefaultPolicyNeverSelectsCNPGPods(t *testing.T) {
	sel, err := metav1.LabelSelectorAsSelector(&Config{}.DefaultNetworkPolicy("odyssey-a").Spec.PodSelector)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		labels map[string]string
		want   bool
	}{
		"unknown pod":            {map[string]string{"x": "y"}, true},
		"cnpg pod, not labelled": {map[string]string{LabelCNPGCluster: "main"}, false},
		"cnpg pod, labelled":     {map[string]string{LabelCNPGCluster: "main", LabelResource: "main"}, false},
		"app pod":                {map[string]string{LabelApp: "web"}, false},
	} {
		if got := sel.Matches(labels.Set(tc.labels)); got != tc.want {
			t.Errorf("%s: matches %v, want %v", name, got, tc.want)
		}
	}
}

func TestFenceWritesTheAIPolicyOfAnAppThatJustArrived(t *testing.T) {
	ctx := context.Background()
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), aiApp("odyssey-a", "web", "http://10.0.0.5:8080"))
	e.Resolver = &fakeResolver{}
	if err := e.Fence(ctx, "odyssey-a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := cidrs(policyIn(t, c, "odyssey-a", AIPolicy).Spec.Egress[0]); !slices.Equal(got, []string{"10.0.0.5/32"}) {
		t.Errorf("an IP literal gateway: %v", got)
	}
}

func TestAIPolicyRemovalLeavesAPolicyThatIsNotOurs(t *testing.T) {
	ctx := context.Background()
	theirs := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "odyssey-a", Name: AIPolicy}}
	e, c := newEnforcer(t, interceptor.Funcs{}, envNamespace("odyssey-a", true), theirs)
	if err := e.Resync(ctx); err != nil {
		t.Fatal(err)
	}
	if policyIn(t, c, "odyssey-a", AIPolicy) == nil {
		t.Error("deleted a policy the agent did not make")
	}
}

// the policy of other pods: resources are selected by their label, and every
// pod that is neither an app nor a resource is closed to the namespace.
func TestDefaultPolicyClosesEverythingElse(t *testing.T) {
	p := Config{}.DefaultNetworkPolicy("odyssey-e")
	if p.Name != DefaultPolicy || len(p.Spec.PodSelector.MatchExpressions) != 3 {
		t.Fatalf("selects %+v", p.Spec.PodSelector)
	}
	for _, r := range p.Spec.PodSelector.MatchExpressions {
		if r.Operator != metav1.LabelSelectorOpDoesNotExist || (r.Key != LabelApp && r.Key != LabelResource && r.Key != LabelCNPGCluster) {
			t.Errorf("selector %+v", r)
		}
	}
	if len(p.Spec.PolicyTypes) != 2 || len(p.Spec.Ingress) != 1 || p.Spec.Ingress[0].From[0].PodSelector == nil || len(p.Spec.Egress) != 2 {
		t.Errorf("spec %+v", p.Spec)
	}
	for _, r := range p.Spec.Egress {
		if b, _ := cidrs(r); len(b) != 0 {
			t.Errorf("an IP range is open: %v", b)
		}
	}
}
