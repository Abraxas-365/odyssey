package app

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
)

func withAI(base string) *v1alpha1.OdysseyApp {
	a := withSecrets(1, managedRef("ODICEA_AI_KEY", 3))
	a.Spec.AI = &v1alpha1.AISpec{BaseURL: base, KeySecret: "ODICEA_AI_KEY"}
	return a
}

func envByName(vars []corev1.EnvVar) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range vars {
		out[e.Name] = e
	}
	return out
}

// firstOf is a's first service (the only one of an app without services).
func firstOf(a *v1alpha1.OdysseyApp) workload { return workloads(a)[0] }

func TestAppWithAIGetsTheFourVariablesAndTheKeyIsAReference(t *testing.T) {
	a := withAI("https://llm.test/")
	got := envByName(withAIEnv(env(a), a, firstOf(a)))
	if got["OPENAI_BASE_URL"].Value != "https://llm.test/v1" || got["ANTHROPIC_BASE_URL"].Value != "https://llm.test" {
		t.Errorf("base urls: %+v %+v", got["OPENAI_BASE_URL"], got["ANTHROPIC_BASE_URL"])
	}
	for _, n := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		ref := got[n].ValueFrom.SecretKeyRef
		if got[n].Value != "" || ref == nil || ref.Name != "portal-secrets" || ref.Key != "ODICEA_AI_KEY" {
			t.Errorf("%s: %+v, want a reference to portal-secrets/ODICEA_AI_KEY", n, got[n])
		}
	}
}

func TestAIVariablesWinOverEverythingTheUserSet(t *testing.T) {
	a := withAI("https://llm.test")
	a.Spec.Env = map[string]string{"OPENAI_API_KEY": "sk-evil", "OPENAI_BASE_URL": "http://evil", "ANTHROPIC_BASE_URL": "http://evil", "KEEP": "1"}
	a.Spec.Secrets = append(a.Spec.Secrets, managedRef("ANTHROPIC_API_KEY", 1))
	vars := withAIEnv(env(a), a, firstOf(a))
	seen := map[string]int{}
	for _, e := range vars {
		seen[e.Name]++
	}
	for n := range aiNames {
		if seen[n] != 1 {
			t.Errorf("%s appears %d times", n, seen[n])
		}
	}
	got := envByName(vars)
	if got["OPENAI_API_KEY"].Value != "" || got["OPENAI_BASE_URL"].Value != "https://llm.test/v1" || got["ANTHROPIC_API_KEY"].ValueFrom.SecretKeyRef.Key != "ODICEA_AI_KEY" {
		t.Errorf("user values survived: %+v", got)
	}
	if got["KEEP"].Value != "1" {
		t.Errorf("an unrelated variable was lost")
	}
}

func TestAppWithoutAIKeepsItsEnvUntouched(t *testing.T) {
	a := newApp(1)
	a.Spec.Env = map[string]string{"OPENAI_API_KEY": "mine"} // a hand-edited object: nothing reserves it without spec.ai
	in := env(a)
	out := withAIEnv(in, a, firstOf(a))
	if len(in) != len(out) || envByName(out)["OPENAI_API_KEY"].Value != "mine" {
		t.Errorf("env changed: %v -> %v", in, out)
	}
}

func TestDeploymentCarriesTheAIVariables(t *testing.T) {
	a := withAI("https://llm.test")
	at := now
	r, c := secretsRec(t, esoMapper(), &at, a, managedSecret(map[string]int64{"ODICEA_AI_KEY": 3}, map[string]string{"ODICEA_AI_KEY": "k"}))
	runWith(t, r, c, a)
	d := deploymentOf(t, c, a)
	got := envByName(d.Spec.Template.Spec.Containers[0].Env)
	if got["OPENAI_BASE_URL"].Value != "https://llm.test/v1" || got["OPENAI_API_KEY"].ValueFrom == nil {
		t.Errorf("deployment env: %+v", got)
	}
}

// The egress fence opens the AI gateway to the pods that carry this label
// (policy odicea-ai); the CR's own labels cannot grant it.
func TestPodCarriesTheAILabelOnlyWithAI(t *testing.T) {
	a := withAI("http://gw:3000")
	a.Labels = map[string]string{LabelAI: "true"}
	a.Spec.AI = nil
	d := &appsv1.Deployment{}
	mutateDeployment(d, a, Config{})
	if _, ok := d.Spec.Template.Labels[LabelAI]; ok {
		t.Errorf("a pod of an app without AI carries %s: %v", LabelAI, d.Spec.Template.Labels)
	}
	a.Spec.AI = &v1alpha1.AISpec{BaseURL: "http://gw:3000", KeySecret: "ODICEA_AI_KEY"}
	d = &appsv1.Deployment{}
	mutateDeployment(d, a, Config{})
	if d.Spec.Template.Labels[LabelAI] != "true" {
		t.Errorf("labels %v", d.Spec.Template.Labels)
	}
}
