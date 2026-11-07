package fleet

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// AI of an app (2d-ai). The organization's AI gateway (freerouter) is
// reached with a key the control plane makes for the environment; the key is
// a managed secret of the app (App.AI.KeySecret names it in App.Secrets) and
// the agent turns it into the variables the OpenAI and Anthropic SDKs read.
// A cluster whose agent does not list CapabilityAI would ignore App.AI and
// leave the app without a key, so the control plane refuses it.

// CapabilityAI is listed in Capabilities.APIs when the agent wires apps to
// the AI gateway (App.ai).
const CapabilityAI = "odicea.ai"

// CodeAIUnavailable: the AI is not available (same code as the ai domain's).
const CodeAIUnavailable = "AI_UNAVAILABLE"

// AIUnavailableMessage is the plain-language text of CodeAIUnavailable for a
// cluster without CapabilityAI.
const AIUnavailableMessage = "Este clúster todavía no puede conectar apps con la IA. Pide a TI que actualice su agente."

// AIRef is how an app reaches the AI gateway (App.AI).
type AIRef struct {
	// BaseURL is the gateway without "/v1".
	BaseURL string `json:"base_url"`
	// KeySecret names the managed secret of App.Secrets that holds the key.
	KeySecret string `json:"key_secret"`
	// Services get the AI (MS-4); empty: every service of the app.
	Services []string `json:"services,omitempty"`
}

// CanUseAI reports whether the cluster lists CapabilityAI.
func (c Capabilities) CanUseAI() bool { return slices.Contains(c.APIs, CapabilityAI) }

// MaxAIBaseURL bounds App.AI.BaseURL.
const MaxAIBaseURL = 2000

// Validate guards the AI of an app: the key must be one of its secrets.
func (r AIRef) Validate(secrets []AppSecret) error {
	switch {
	case r.BaseURL == "" || len(r.BaseURL) > MaxAIBaseURL:
		return errx.Validation("the app's AI needs a base_url")
	case !slices.ContainsFunc(secrets, func(s AppSecret) bool { return s.Name == r.KeySecret && s.Source == SecretManaged }):
		return errx.Validation("the app's AI key must be one of its managed secrets")
	}
	return nil
}

// CheckAI is Apply's guard: a cluster that cannot wire the AI must not get an
// app that uses it.
func (c Capabilities) CheckAI(ref *AIRef) error {
	if ref != nil && !c.CanUseAI() {
		return errx.Business(AIUnavailableMessage).WithCode(CodeAIUnavailable)
	}
	return nil
}
