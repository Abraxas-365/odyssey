package app

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/secretstore"
)

// The variables an app with AI reads. The OpenAI and Anthropic SDKs find the
// gateway and the key from them with no code of their own.
const (
	EnvOpenAIBaseURL    = "OPENAI_BASE_URL"
	EnvOpenAIKey        = "OPENAI_API_KEY"
	EnvAnthropicBaseURL = "ANTHROPIC_BASE_URL"
	EnvAnthropicKey     = "ANTHROPIC_API_KEY"
)

// aiNames are the four variables the AI owns.
var aiNames = map[string]bool{EnvOpenAIBaseURL: true, EnvOpenAIKey: true, EnvAnthropicBaseURL: true, EnvAnthropicKey: true}

// aiEnv are the AI's variables for a (none without spec.ai). The key is read
// from <app>-secrets, where the console delivered it as spec.ai.keySecret;
// it is never a literal. OPENAI_BASE_URL has /v1 (the OpenAI SDKs append
// /chat/completions to it); ANTHROPIC_BASE_URL does not (the Anthropic SDKs
// append /v1/messages).
func aiEnv(a *v1alpha1.OdysseyApp) []corev1.EnvVar {
	if a.Spec.AI == nil {
		return nil
	}
	base := strings.TrimRight(a.Spec.AI.BaseURL, "/")
	key := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretstore.Name(a.Name)}, Key: a.Spec.AI.KeySecret}}}
	}
	return []corev1.EnvVar{
		{Name: EnvOpenAIBaseURL, Value: base + "/v1"}, key(EnvOpenAIKey),
		{Name: EnvAnthropicBaseURL, Value: base}, key(EnvAnthropicKey),
	}
}

// withAIEnv puts the AI's variables last, after dropping any of the same
// names (like withAuthEnv, the AI wins over everything: a hand-edited
// OdysseyApp cannot point the key elsewhere). Without spec.ai nothing changes.
func withAIEnv(in []corev1.EnvVar, a *v1alpha1.OdysseyApp) []corev1.EnvVar {
	ours := aiEnv(a)
	if ours == nil {
		return in
	}
	out := make([]corev1.EnvVar, 0, len(in)+len(ours))
	for _, e := range in {
		if !aiNames[e.Name] {
			out = append(out, e)
		}
	}
	return append(out, ours...)
}
