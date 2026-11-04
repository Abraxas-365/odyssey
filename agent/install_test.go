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
// (desired.SecretsRole) exists, is bound by nothing in the install (the agent
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
			if b := decode[rbacv1.RoleBinding](t, m); b.RoleRef.Name == desired.SecretsRole {
				t.Errorf("the install binds %s in %s: only the agent binds it, per environment namespace", desired.SecretsRole, b.Namespace)
			}
		case "ValidatingAdmissionPolicy":
			p := decode[struct {
				Metadata struct{ Name string } `json:"metadata"`
				Spec     struct {
					Validations []struct{ Expression string } `json:"validations"`
				} `json:"spec"`
			}](t, m)
			if p.Metadata.Name == desired.SecretsRole {
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
		t.Errorf("no ValidatingAdmissionPolicy %s confines the Secrets role's bindings", desired.SecretsRole)
	}
	// The policy names the agent's identity literally: it must be the one
	// the agent's Deployment runs as (and binds for), or every bind is denied.
	if !slices.Contains(agentSA, runsAs) {
		t.Errorf("the agent runs as %q, not a ServiceAccount of the install %v", runsAs, agentSA)
	}
	if ns, name, _ := strings.Cut(runsAs, "/"); !strings.Contains(policyText, "s.namespace == '"+ns+"'") || !strings.Contains(policyText, "s.name == '"+name+"'") {
		t.Errorf("the policy does not admit the agent's service account %s:\n%s", runsAs, policyText)
	}
	secrets, ok := roles[desired.SecretsRole]
	if !ok {
		t.Fatalf("no ClusterRole %s", desired.SecretsRole)
	}
	if slices.Contains(bound, desired.SecretsRole) {
		t.Errorf("%s is bound cluster-wide", desired.SecretsRole)
	}
	grants := false
	for _, r := range secrets.Rules {
		grants = grants || slices.Contains(r.Resources, "secrets")
	}
	if !grants {
		t.Errorf("%s grants no access to secrets", desired.SecretsRole)
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
				if !slices.Equal(rule.ResourceNames, []string{desired.SecretsRole}) {
					t.Errorf("ClusterRole %s may bind %v: only %s", name, rule.ResourceNames, desired.SecretsRole)
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
		t.Errorf("the agent may not bind %s", desired.SecretsRole)
	}
}
