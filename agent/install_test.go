package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/desired"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// manifests reads every object of the install (multi-document YAML).
func manifests(t *testing.T) []map[string]any {
	t.Helper()
	files, err := filepath.Glob("install/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob("install/rbac/*.yaml")
	var out []map[string]any
	for _, f := range append(files, more...) {
		if filepath.Base(f) == "kustomization.yaml" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range bytes.Split(raw, []byte("\n---")) {
			var m map[string]any
			if err := yaml.Unmarshal(doc, &m); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func decode[T any](t *testing.T, m map[string]any) T {
	t.Helper()
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestAgentCannotReadTheClustersSecrets: the agent holds no access to
// Secrets cluster-wide. Every ClusterRole bound by a ClusterRoleBinding is
// free of the secrets resource (and of wildcards); the Secrets role
// (desired.EnvironmentRole) exists, is bound by nothing in the install (the agent
// binds it per environment namespace), the agent may bind exactly that role,
// and the admission policy that confines its bindings is installed.
func TestAgentCannotReadTheClustersSecrets(t *testing.T) {
	objs := manifests(t)
	roles := map[string]rbacv1.ClusterRole{}
	var bound, agentSA []string
	var policyText, runsAs string
	policy := false
	for _, m := range objs {
		switch m["kind"] {
		case "ClusterRole":
			r := decode[rbacv1.ClusterRole](t, m)
			roles[r.Name] = r
		case "ClusterRoleBinding":
			b := decode[rbacv1.ClusterRoleBinding](t, m)
			bound = append(bound, b.RoleRef.Name)
		case "RoleBinding":
			if b := decode[rbacv1.RoleBinding](t, m); b.RoleRef.Name == desired.EnvironmentRole {
				t.Errorf("the install binds %s in %s: only the agent binds it, per environment namespace", desired.EnvironmentRole, b.Namespace)
			}
		case "ValidatingAdmissionPolicy":
			p := decode[struct {
				Metadata struct{ Name string } `json:"metadata"`
				Spec     struct {
					Validations []struct{ Expression string } `json:"validations"`
				} `json:"spec"`
			}](t, m)
			if p.Metadata.Name == desired.EnvironmentRole {
				policy = true
				for _, v := range p.Spec.Validations {
					policyText += v.Expression + "\n"
				}
			}
		case "ServiceAccount":
			sa := decode[struct {
				Metadata struct{ Name, Namespace string } `json:"metadata"`
			}](t, m)
			agentSA = append(agentSA, sa.Metadata.Namespace+"/"+sa.Metadata.Name)
		case "Deployment":
			d := decode[struct {
				Metadata struct{ Name, Namespace string } `json:"metadata"`
				Spec     struct {
					Template struct {
						Spec struct {
							ServiceAccountName string `json:"serviceAccountName"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			}](t, m)
			if d.Metadata.Name == "odyssey-agent" {
				runsAs = d.Metadata.Namespace + "/" + d.Spec.Template.Spec.ServiceAccountName
			}
		}
	}
	if !policy {
		t.Errorf("no ValidatingAdmissionPolicy %s confines the Secrets role's bindings", desired.EnvironmentRole)
	}
	// The policy names the agent's identity literally: it must be the one
	// the agent's Deployment runs as (and binds for), or every bind is denied.
	if !slices.Contains(agentSA, runsAs) {
		t.Errorf("the agent runs as %q, not a ServiceAccount of the install %v", runsAs, agentSA)
	}
	if ns, name, _ := strings.Cut(runsAs, "/"); !strings.Contains(policyText, "s.namespace == '"+ns+"'") || !strings.Contains(policyText, "s.name == '"+name+"'") {
		t.Errorf("the policy does not admit the agent's service account %s:\n%s", runsAs, policyText)
	}
	secrets, ok := roles[desired.EnvironmentRole]
	if !ok {
		t.Fatalf("no ClusterRole %s", desired.EnvironmentRole)
	}
	if slices.Contains(bound, desired.EnvironmentRole) {
		t.Errorf("%s is bound cluster-wide", desired.EnvironmentRole)
	}
	grants := false
	for _, r := range secrets.Rules {
		grants = grants || slices.Contains(r.Resources, "secrets")
	}
	if !grants {
		t.Errorf("%s grants no access to secrets", desired.EnvironmentRole)
	}
	canBind := false
	for _, name := range bound {
		r, ok := roles[name]
		if !ok {
			continue // bound to a role the install does not define
		}
		for _, rule := range r.Rules {
			if slices.Contains(rule.Resources, "secrets") || slices.Contains(rule.Resources, "*") || slices.Contains(rule.Verbs, "*") {
				t.Errorf("ClusterRole %s, bound cluster-wide, grants %v on %v", name, rule.Verbs, rule.Resources)
			}
			if slices.Contains(rule.Resources, "clusterroles") && slices.Contains(rule.Verbs, "bind") {
				if !slices.Equal(rule.ResourceNames, []string{desired.EnvironmentRole}) {
					t.Errorf("ClusterRole %s may bind %v: only %s", name, rule.ResourceNames, desired.EnvironmentRole)
				}
				canBind = true
			}
			if slices.Contains(rule.Resources, "rolebindings") && slices.ContainsFunc(rule.Verbs, func(v string) bool { return v != "create" }) {
				t.Errorf("ClusterRole %s may %v rolebindings: only create", name, rule.Verbs)
			}
			if slices.Contains(rule.Resources, "namespaces") && slices.ContainsFunc(rule.Verbs, func(v string) bool { return v == "update" || v == "patch" }) {
				t.Errorf("ClusterRole %s may relabel namespaces (%v): it could make another's match the policy", name, rule.Verbs)
			}
		}
	}
	if !canBind {
		t.Errorf("the agent may not bind %s", desired.EnvironmentRole)
	}
}

// verbsOn is every verb the rules grant on group/resource ("*" in a rule
// counts for everything).
func verbsOn(rules []rbacv1.PolicyRule, group, resource string) []string {
	var out []string
	for _, r := range rules {
		if (slices.Contains(r.APIGroups, group) || slices.Contains(r.APIGroups, "*")) &&
			(slices.Contains(r.Resources, resource) || slices.Contains(r.Resources, "*")) {
			out = append(out, r.Verbs...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestAgentCannotEraseDataClusterWide: the verbs that create, grow or erase
// data (volumes, CloudNativePG databases, deployments and services) are
// granted only by the environment role, which is bound in environment
// namespaces: no ClusterRole bound by a ClusterRoleBinding (the generated
// odyssey-agent one included) grants them, nor a wildcard. The environment
// role grants exactly what the controllers need.
func TestAgentCannotEraseDataClusterWide(t *testing.T) {
	roles := map[string]rbacv1.ClusterRole{}
	var bound []string
	for _, m := range manifests(t) {
		switch m["kind"] {
		case "ClusterRole":
			r := decode[rbacv1.ClusterRole](t, m)
			roles[r.Name] = r
		case "ClusterRoleBinding":
			bound = append(bound, decode[rbacv1.ClusterRoleBinding](t, m).RoleRef.Name)
		}
	}
	var clusterWide []rbacv1.PolicyRule
	for _, name := range bound {
		r, ok := roles[name]
		if !ok {
			continue
		}
		for _, rule := range r.Rules {
			if slices.Contains(rule.Resources, "*") || slices.Contains(rule.Verbs, "*") || slices.Contains(rule.APIGroups, "*") {
				t.Errorf("ClusterRole %s, bound cluster-wide, has a wildcard: %v %v %v", name, rule.APIGroups, rule.Resources, rule.Verbs)
			}
		}
		clusterWide = append(clusterWide, r.Rules...)
	}
	forbidden := []struct {
		group, resource string
		verbs           []string
	}{
		{"", "persistentvolumeclaims", []string{"create", "update", "patch", "delete", "deletecollection"}},
		{"apps", "deployments", []string{"delete", "deletecollection"}},
		{"", "services", []string{"delete", "deletecollection"}},
		{"postgresql.cnpg.io", "clusters", []string{"create", "update", "patch", "delete", "deletecollection"}},
		{"networking.k8s.io", "networkpolicies", []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}},
	}
	for _, f := range forbidden {
		got := verbsOn(clusterWide, f.group, f.resource)
		for _, v := range f.verbs {
			if slices.Contains(got, v) {
				t.Errorf("a cluster-wide role grants %s on %s/%s: only the environment role may", v, f.group, f.resource)
			}
		}
	}
	// What the caches and watches need stays cluster-wide.
	for _, want := range []struct{ group, resource string }{{"", "persistentvolumeclaims"}, {"postgresql.cnpg.io", "clusters"}} {
		got := verbsOn(clusterWide, want.group, want.resource)
		for _, v := range []string{"get", "list", "watch"} {
			if !slices.Contains(got, v) {
				t.Errorf("no cluster-wide %s on %s/%s: the controller's cache needs it", v, want.group, want.resource)
			}
		}
	}

	env, ok := roles[desired.EnvironmentRole]
	if !ok {
		t.Fatalf("no ClusterRole %s", desired.EnvironmentRole)
	}
	for _, rule := range env.Rules {
		if slices.Contains(rule.Resources, "*") || slices.Contains(rule.Verbs, "*") || slices.Contains(rule.APIGroups, "*") {
			t.Errorf("%s has a wildcard: %v %v %v", desired.EnvironmentRole, rule.APIGroups, rule.Resources, rule.Verbs)
		}
	}
	// Exactly what the controllers write (reads come from the cluster-wide role).
	exact := []struct {
		group, resource string
		verbs           []string
	}{
		{"", "secrets", []string{"create", "delete", "get", "update"}},
		{"", "persistentvolumeclaims", []string{"create", "delete", "update"}},
		{"", "services", []string{"delete"}},
		{"apps", "deployments", []string{"delete"}},
		{"postgresql.cnpg.io", "clusters", []string{"create", "delete", "update"}},
		{"networking.k8s.io", "networkpolicies", []string{"create", "delete", "get", "update"}},
	}
	granted := 0
	for _, e := range exact {
		if got := verbsOn(env.Rules, e.group, e.resource); !slices.Equal(got, e.verbs) {
			t.Errorf("%s grants %v on %s/%s, want %v", desired.EnvironmentRole, got, e.group, e.resource, e.verbs)
		}
	}
	for _, r := range env.Rules {
		granted += len(r.Resources)
	}
	if granted != len(exact) {
		t.Errorf("%s grants on %d resources, want %d: %v", desired.EnvironmentRole, granted, len(exact), env.Rules)
	}
}

// TestEgressPermissionsAreTheLeastTheEnforcerNeeds: the enforcer reads and
// writes NetworkPolicies by name through the API (no cache), so it gets
// get/create/update/delete in environment namespaces only (the environment
// role: no patch, no list, no watch, nothing cluster-wide); it lists the nodes
// (their addresses stay closed in the internet mode) and nothing more on them;
// and its ConfigMap is one name: get/update only on odyssey-cluster-policy.
func TestEgressPermissionsAreTheLeastTheEnforcerNeeds(t *testing.T) {
	var clusterWide []rbacv1.PolicyRule
	var policyRole *rbacv1.Role
	roles := map[string]rbacv1.ClusterRole{}
	var bound []string
	for _, m := range manifests(t) {
		switch m["kind"] {
		case "ClusterRole":
			r := decode[rbacv1.ClusterRole](t, m)
			roles[r.Name] = r
		case "ClusterRoleBinding":
			bound = append(bound, decode[rbacv1.ClusterRoleBinding](t, m).RoleRef.Name)
		case "Role":
			if r := decode[rbacv1.Role](t, m); r.Name == "odyssey-agent-policy" {
				policyRole = &r
			}
		}
	}
	for _, name := range bound {
		clusterWide = append(clusterWide, roles[name].Rules...)
	}
	if got := verbsOn(clusterWide, "networking.k8s.io", "networkpolicies"); len(got) != 0 {
		t.Errorf("a cluster-wide role grants %v on networkpolicies: the enforcer never lists or watches them", got)
	}
	if got := verbsOn(clusterWide, "", "nodes"); !slices.Equal(got, []string{"list"}) {
		t.Errorf("nodes: %v, want only list", got)
	}
	if got := verbsOn(clusterWide, "", "configmaps"); len(got) != 0 {
		t.Errorf("a cluster-wide role grants %v on configmaps", got)
	}
	if policyRole == nil {
		t.Fatal("no Role odyssey-agent-policy for the policy ConfigMap")
	}
	if policyRole.Namespace != "odyssey-system" {
		t.Errorf("the policy role is in %q", policyRole.Namespace)
	}
	for _, rule := range policyRole.Rules {
		switch {
		case slices.Contains(rule.Verbs, "create"):
			if len(rule.ResourceNames) != 0 || len(rule.Verbs) != 1 {
				t.Errorf("create is the only verb that cannot name the ConfigMap: %+v", rule)
			}
		default:
			if !slices.Equal(rule.ResourceNames, []string{"odyssey-cluster-policy"}) || !slices.Equal(rule.Verbs, []string{"get", "update"}) {
				t.Errorf("the ConfigMap rule is not get/update on its one name: %+v", rule)
			}
		}
	}
	// The agent's own Role in odyssey-system no longer grants configmaps on every name.
	for _, m := range manifests(t) {
		if m["kind"] != "Role" {
			continue
		}
		r := decode[rbacv1.Role](t, m)
		if r.Name == "odyssey-agent-policy" {
			continue
		}
		for _, rule := range r.Rules {
			if slices.Contains(rule.Resources, "configmaps") && len(rule.ResourceNames) == 0 && slices.Contains(rule.Verbs, "get") {
				t.Errorf("Role %s grants get on every ConfigMap: %+v", r.Name, rule)
			}
		}
	}
}
