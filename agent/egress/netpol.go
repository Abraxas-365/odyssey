package egress

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The NetworkPolicies the agent keeps in every environment namespace.
const (
	// AppsPolicy fences the pods of apps; ResourcesPolicy those of resources;
	// DefaultPolicy every other pod; AIPolicy opens the AI gateway to the apps
	// that use AI (only while one does).
	AppsPolicy      = "odicea-apps"
	ResourcesPolicy = "odicea-resources"
	DefaultPolicy   = "odicea-default"
	AIPolicy        = "odicea-ai"

	// LabelManagedBy marks the policies the agent owns (the same label and
	// value as the rest of the agent's objects; agent/controllers/app).
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "odyssey-agent"
	// LabelApp is on the pods of an app (agent/controllers/app.LabelApp):
	// the apps policy selects them. LabelResource is on the pods of a resource
	// (agent/controllers/resource.LabelResource; a CloudNativePG database gets
	// it on its pods through spec.inheritedMetadata): the resources policy
	// selects them, and the default policy every pod that has neither.
	LabelApp      = "odyssey.dev/app"
	LabelResource = "odyssey.dev/resource"
	// LabelAI is on the pods of an app that uses AI (agent/controllers/app.LabelAI):
	// the AI policy selects them.
	LabelAI = "odyssey.dev/ai"
	// LabelCNPGCluster is put by CloudNativePG on every pod of a database. The
	// default policy never selects such a pod, even before the Cluster's
	// inheritedMetadata gave it LabelResource (a database made before the
	// label existed): CloudNativePG pods are never closed by mistake, and the
	// resources policy takes them once they carry LabelResource.
	LabelCNPGCluster = "cnpg.io/cluster"
	// NamespaceManagedBy is the value of the managed-by label on the
	// namespaces the agent creates (agent/desired).
	NamespaceManagedBy = "odyssey"
	// NamespacePrefix starts the name of every environment namespace.
	NamespacePrefix = "odyssey-"

	// GatePort is where the sign-in gate serves its keys to the apps.
	GatePort = 8099
)

// Config says where the cluster's shared pieces live. The defaults are k3s's
// (Traefik and CoreDNS in kube-system) and the stock installs of the gate
// and CloudNativePG; another cluster sets its own (ODYSSEY_INGRESS_NAMESPACE,
// ODYSSEY_INGRESS_POD_LABEL, ODYSSEY_OPERATOR_NAMESPACE, ODYSSEY_DNS_NAMESPACE,
// ODYSSEY_DNS_POD_LABEL; see agent/agent.go).
type Config struct {
	// SystemNamespace is where the agent and the gate run.
	SystemNamespace string
	// IngressNamespace and IngressPodLabels select the ingress controller's
	// pods: the only ones that may reach an app from outside its namespace.
	IngressNamespace string
	IngressPodLabels map[string]string
	// DNSNamespace and DNSPodLabels select the cluster's DNS pods.
	DNSNamespace string
	DNSPodLabels map[string]string
	// OperatorNamespace is where CloudNativePG's operator runs: it manages
	// the databases and so may reach their pods.
	OperatorNamespace string
}

// WithDefaults fills what is empty.
func (c Config) WithDefaults() Config {
	if c.SystemNamespace == "" {
		c.SystemNamespace = "odyssey-system"
	}
	if c.IngressNamespace == "" {
		c.IngressNamespace = "kube-system"
	}
	if len(c.IngressPodLabels) == 0 {
		c.IngressPodLabels = map[string]string{"app.kubernetes.io/name": "traefik"}
	}
	if c.DNSNamespace == "" {
		c.DNSNamespace = "kube-system"
	}
	if len(c.DNSPodLabels) == 0 {
		c.DNSPodLabels = map[string]string{"k8s-app": "kube-dns"}
	}
	if c.OperatorNamespace == "" {
		c.OperatorNamespace = "cnpg-system"
	}
	return c
}

// reservedV4 and reservedV6 are what the internet mode never opens: private,
// CGNAT, link-local (cloud metadata, 169.254.169.254) and loopback ranges, and
// the ones that are never a public destination (this-network, IETF protocol
// assignments, benchmarking, multicast, reserved/broadcast; NAT64, which
// reaches IPv4 hosts, and IPv6 multicast). An address in them is reachable
// only when the allow list names it. The addresses of the cluster's nodes are
// added to these (nodeExcept): the kubelet, the API server and the NodePorts
// listen on them.
var (
	reservedV4 = []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8",
		"0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
	}
	reservedV6 = []string{"fc00::/7", "fe80::/10", "::1/128", "64:ff9b::/96", "ff00::/8"}
)

// nodeExcept is the except list of one family's internet rule: the reserved
// ranges plus each node address that is not inside one already.
func nodeExcept(reserved []string, nodes []netip.Addr, v6 bool) []string {
	out := slices.Clone(reserved)
	covered := make([]netip.Prefix, 0, len(reserved))
	for _, r := range reserved {
		covered = append(covered, netip.MustParsePrefix(r))
	}
	for _, n := range nodes {
		n = n.Unmap()
		if n.Is6() != v6 || slices.ContainsFunc(covered, func(p netip.Prefix) bool { return p.Contains(n) }) {
			continue
		}
		out = append(out, netip.PrefixFrom(n, n.BitLen()).String())
	}
	return out
}

func namespaceIs(name string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: name}}
}

func port(n int, proto corev1.Protocol) networkingv1.NetworkPolicyPort {
	p, v := proto, intstr.FromInt32(int32(n))
	return networkingv1.NetworkPolicyPort{Protocol: &p, Port: &v}
}

func sameNamespace() networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
}

func objectMeta(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{LabelManagedBy: ManagedBy}}
}

// Environment is what the policies of a namespace depend on besides the
// cluster policy.
type Environment struct {
	// Nodes are the addresses of the cluster's nodes (InternalIP and
	// ExternalIP): never reachable through the internet mode.
	Nodes []netip.Addr
	// AI are the gateways the namespace's apps with AI reach (none: the
	// namespace has no app with AI and no AIPolicy).
	AI []AIGateway
}

// AIGateway is where an app's AI base URL points, as the agent resolved it.
type AIGateway struct {
	// Service is the namespace of an in-cluster Service (name.namespace.svc):
	// the gateway is reached through it. Else Addrs are its addresses.
	Service string
	Addrs   []netip.Addr
	Port    int32
}

// AppsNetworkPolicy fences the app pods of a namespace.
//
// Ingress: the ingress controller and the namespace's own pods. Egress: DNS,
// the namespace's own pods (its resources), the gate's keys, and by mode the
// public internet (every address but the reserved ranges and the nodes') or
// nothing more; plus the allow list, internal addresses included.
func (c Config) AppsNetworkPolicy(namespace string, p Policy, env Environment) *networkingv1.NetworkPolicy {
	c = c.WithDefaults()
	tcp := corev1.ProtocolTCP
	egress := []networkingv1.NetworkPolicyEgressRule{
		c.dnsRule(),
		{To: []networkingv1.NetworkPolicyPeer{sameNamespace()}},
		{
			To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: namespaceIs(c.SystemNamespace),
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "odyssey-access"}},
			}},
			Ports: []networkingv1.NetworkPolicyPort{port(GatePort, tcp)},
		},
	}
	if p.Mode != ModeRestricted {
		egress = append(egress,
			networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: nodeExcept(reservedV4, env.Nodes, false)}}}},
			networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: nodeExcept(reservedV6, env.Nodes, true)}}}},
		)
	}
	if len(p.Allow) > 0 {
		var to []networkingv1.NetworkPolicyPeer
		for _, a := range p.Allow {
			to = append(to, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: a.String()}})
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{To: to})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(namespace, AppsPolicy),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: LabelApp, Operator: metav1.LabelSelectorOpExists}}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: namespaceIs(c.IngressNamespace),
					PodSelector:       &metav1.LabelSelector{MatchLabels: c.IngressPodLabels},
				}}},
				{From: []networkingv1.NetworkPolicyPeer{sameNamespace()}},
			},
			Egress: egress,
		},
	}
}

func (c Config) dnsRule() networkingv1.NetworkPolicyEgressRule {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: namespaceIs(c.DNSNamespace),
			PodSelector:       &metav1.LabelSelector{MatchLabels: c.DNSPodLabels},
		}},
		Ports: []networkingv1.NetworkPolicyPort{port(53, udp), port(53, tcp)},
	}
}

// ResourcesNetworkPolicy fences the pods of resources (databases, caches):
// the pods that carry the resource label. Only the namespace's own pods (its
// apps) and the database operator reach them; what they reach is not
// restricted (a database talks to the cluster's API server and needs its
// backups' storage, for one).
func (c Config) ResourcesNetworkPolicy(namespace string) *networkingv1.NetworkPolicy {
	c = c.WithDefaults()
	return &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(namespace, ResourcesPolicy),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: LabelResource, Operator: metav1.LabelSelectorOpExists}}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{From: []networkingv1.NetworkPolicyPeer{sameNamespace()}},
				{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: namespaceIs(c.OperatorNamespace)}}},
			},
		},
	}
}

// DefaultNetworkPolicy fences every other pod of the namespace (one that is
// neither an app's nor a resource's: nothing the agent runs, but whatever else
// lands there): it talks to the namespace and DNS and is reached by the
// namespace only. A pod the agent does not know gets no more than that. Pods
// of CloudNativePG (label cnpg.io/cluster) are excluded: a database made
// before its pods carried odyssey.dev/resource must not be closed by this
// policy while the agent labels it (it would cut the operator and the apps).
func (c Config) DefaultNetworkPolicy(namespace string) *networkingv1.NetworkPolicy {
	c = c.WithDefaults()
	return &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(namespace, DefaultPolicy),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: LabelApp, Operator: metav1.LabelSelectorOpDoesNotExist},
				{Key: LabelResource, Operator: metav1.LabelSelectorOpDoesNotExist},
				{Key: LabelCNPGCluster, Operator: metav1.LabelSelectorOpDoesNotExist},
			}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{sameNamespace()}}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{c.dnsRule(), {To: []networkingv1.NetworkPolicyPeer{sameNamespace()}}},
		},
	}
}

// AINetworkPolicy lets the namespace's apps that use AI (the pods labelled
// LabelAI) reach their gateway whatever the cluster's mode: an internal
// gateway is inside the ranges the internet mode closes, and the restricted
// mode closes everything. The rule is the gateway's addresses (as the agent
// resolved them) on its port, or the gateway's namespace for an in-cluster
// Service. Policies add up: this one only opens what odicea-apps does not.
// nil when no app of the namespace uses AI.
func (c Config) AINetworkPolicy(namespace string, gateways []AIGateway) *networkingv1.NetworkPolicy {
	if len(gateways) == 0 {
		return nil
	}
	tcp := corev1.ProtocolTCP
	var rules []networkingv1.NetworkPolicyEgressRule
	for _, g := range gateways {
		if g.Service != "" {
			// An in-cluster Service: its port is not the pod's (targetPort),
			// so the namespace is opened on any port. A rule with no Ports
			// (never Port 0, which the API server refuses) allows them all.
			rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: namespaceIs(g.Service)}}})
		}
		var to []networkingv1.NetworkPolicyPeer
		for _, a := range g.Addrs {
			a = a.Unmap()
			to = append(to, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(a, a.BitLen()).String()}})
		}
		if len(to) == 0 || g.Port < 1 || g.Port > 65535 {
			continue
		}
		p, v := tcp, intstr.FromInt32(g.Port)
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: to, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &p, Port: &v}}})
	}
	if len(rules) == 0 {
		return nil
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(namespace, AIPolicy),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelAI: "true"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

// Policies is what a namespace should hold under policy p, by name.
func (c Config) Policies(namespace string, p Policy, env Environment) []*networkingv1.NetworkPolicy {
	out := []*networkingv1.NetworkPolicy{c.AppsNetworkPolicy(namespace, p, env), c.ResourcesNetworkPolicy(namespace), c.DefaultNetworkPolicy(namespace)}
	if ai := c.AINetworkPolicy(namespace, env.AI); ai != nil {
		out = append(out, ai)
	}
	slices.SortFunc(out, func(a, b *networkingv1.NetworkPolicy) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Warnings are what TI should know about the policy in this cluster: an allow
// entry that includes a node's address opens the node's kubelet, its ports
// and (on a control-plane node) the Kubernetes API to every app. The entry is
// kept (TI asked for it); the control plane shows the warning.
func Warnings(p Policy, nodes []netip.Addr) []string {
	var out []string
	for _, a := range p.Allow {
		for _, n := range nodes {
			if a.Contains(n.Unmap()) {
				out = append(out, fmt.Sprintf("La dirección %s incluye el nodo %s del clúster: las apps podrán llegar a su kubelet, a sus puertos y, si es un nodo del plano de control, a la API de Kubernetes.", a, n.Unmap()))
				break
			}
		}
		if len(out) == maxWarnings {
			break
		}
	}
	return out
}

// maxWarnings bounds what is reported (the ack stays small).
const maxWarnings = 10
