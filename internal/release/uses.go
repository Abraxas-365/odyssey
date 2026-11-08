package release

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Uses (2e) is what an app depends on and what still stands in its way, as
// one read model: who may open it, what it is connected to, which secrets
// are still to load, whether the AI works and what the cluster lets it reach.
// release assembles it from its own settings and from ports (Resources,
// AIStatus, Cluster); it stores nothing of its own.
//
// Each source the settings do not hold (the resource domain, the secrets
// against the cluster, the AI, the cluster's egress) may fail on its own: the
// model then comes without that section (null, listed in Unavailable) and
// without the attention that depends on it, never as an error.

// ResourceStatus is where a resource the app uses stands.
type ResourceStatus string

const (
	ResourceReady     ResourceStatus = "ready"
	ResourcePreparing ResourceStatus = "preparing" // also: nothing reported yet
	ResourceFailed    ResourceStatus = "failed"
	ResourceDeleting  ResourceStatus = "deleting"
	// ResourceUnknown: the resource domain could not be asked.
	ResourceUnknown ResourceStatus = "unknown"
)

// ResourceState is how the resource domain sees one resource of the
// environment (the Resources port's Statuses).
type ResourceState struct {
	Name   string
	Kind   resourcekind.Kind
	Status ResourceStatus
	// Message is what the cluster said (a failure's reason).
	Message string
}

// AIState is what the AI looks like from outside release (the AIStatus port).
type AIState struct {
	// Available: the AI can be turned on for the app.
	Available bool
	// Status is the ai domain's state: off, enabling, enabled, rotating,
	// disabling or revoke_failed.
	Status string
	// Warning: a key could not be revoked at the gateway (also while the app
	// has the AI on again with a new key).
	Warning bool
}

// AIRevokeFailed is the AI status of an app whose key could not be revoked
// at the gateway.
const AIRevokeFailed = "revoke_failed"

// ClusterEgressState is how the cluster's agent took the egress policy.
type ClusterEgressState string

const (
	EgressApplied     ClusterEgressState = "applied"
	EgressPending     ClusterEgressState = "pending"
	EgressFailed      ClusterEgressState = "failed"
	EgressUnsupported ClusterEgressState = "unsupported"
)

// EgressRestricted is the egress mode that blocks the open internet.
const EgressRestricted = "restricted"

// ClusterEgress is the egress policy of the environment's cluster (the
// Cluster port's Egress).
type ClusterEgress struct {
	// Available is false when the environment has no cluster (or it was
	// revoked): there is nothing to apply a policy to.
	Available bool
	// Mode is internet or restricted.
	Mode  string
	State ClusterEgressState
	// Enforced: the agent confirmed the policy.
	Enforced bool
}

// UsesSection is a part of Uses that comes from a source that may be down.
type UsesSection string

const (
	SectionResources UsesSection = "resources"
	SectionSecrets   UsesSection = "secrets"
	SectionAI        UsesSection = "ai"
	SectionEgress    UsesSection = "egress"
)

// UsesSections are every section that can be unavailable.
var UsesSections = []UsesSection{SectionResources, SectionSecrets, SectionAI, SectionEgress}

// AttentionKind is the closed list of things that block an app or are still
// in progress. The console and the assistants branch on it.
type AttentionKind string

const (
	AttentionSecretMissing      AttentionKind = "secret_missing"
	AttentionSecretsUnavailable AttentionKind = "secrets_unavailable"
	AttentionResourceNotReady   AttentionKind = "resource_not_ready"
	AttentionResourceFailed     AttentionKind = "resource_failed"
	AttentionAIError            AttentionKind = "ai_error"
	AttentionAIPending          AttentionKind = "ai_pending"
	AttentionConfigPending      AttentionKind = "config_pending"
	AttentionConfigFailed       AttentionKind = "config_failed"
	AttentionEgressNotApplied   AttentionKind = "egress_not_applied"
)

// AttentionKinds are every kind, in the order they are reported.
var AttentionKinds = []AttentionKind{AttentionSecretMissing, AttentionSecretsUnavailable, AttentionResourceNotReady, AttentionResourceFailed,
	AttentionAIError, AttentionAIPending, AttentionConfigPending, AttentionConfigFailed, AttentionEgressNotApplied}

func (k AttentionKind) Valid() bool { return slices.Contains(AttentionKinds, k) }

// AttentionWho says who can do something about an Attention.
type AttentionWho string

const (
	// WhoOwner: the app's people can act (load a secret, save the change again).
	WhoOwner AttentionWho = "owner"
	// WhoIT: only TI can fix it (the cluster, the AI gateway, a failed resource).
	WhoIT AttentionWho = "it"
	// WhoWait: it is in progress and nobody has anything to do.
	WhoWait AttentionWho = "wait"
)

// AttentionWhos are every who.
var AttentionWhos = []AttentionWho{WhoOwner, WhoIT, WhoWait}

func (w AttentionWho) Valid() bool { return slices.Contains(AttentionWhos, w) }

// Attention is one thing to know about, in plain Spanish.
type Attention struct {
	Kind    AttentionKind `json:"kind"`
	Who     AttentionWho  `json:"who"`
	Message string        `json:"message"`
}

// UsesAccess is who may open the app and whether the app online has it yet.
type UsesAccess struct {
	Mode AccessMode `json:"mode"`
	// Pending: the mode is stored but the app online does not have it yet.
	Pending bool `json:"pending"`
	// Failed (only with Pending): nothing is applying it any more; saving
	// the change again retries.
	Failed bool `json:"failed"`
}

// UsesIdentity says whether the app can tell who is visiting.
type UsesIdentity struct {
	// Verifiable: the app receives the signed X-Odicea-Assertion header (the
	// team and organization modes).
	Verifiable bool `json:"verifiable"`
}

// UsedResource is a resource the app is connected to.
type UsedResource struct {
	Name      string            `json:"name"`
	Kind      resourcekind.Kind `json:"kind"`
	Prefix    string            `json:"prefix"`
	Status    ResourceStatus    `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Variables []string          `json:"variables"`
}

// UsedSecrets counts the app's secret variables.
type UsedSecrets struct {
	Total int `json:"total"`
	// Missing are the names whose value the app does not receive yet: nobody
	// loaded it, or it must be loaded again (and the cluster can take it).
	Missing []string `json:"missing"`
	// External are the ones read from the company's vault.
	External int `json:"external"`
	// Unavailable are the ones that cannot reach the app now whatever its
	// people do: the cluster takes no secrets, or a vault one cannot be read.
	Unavailable int `json:"unavailable"`
}

// UsedAI is the app's use of the AI gateway.
type UsedAI struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status"`
}

// UsedEgress is what the cluster lets the app reach.
type UsedEgress struct {
	// Mode is internet or restricted ("" when the app has no cluster).
	Mode     string             `json:"mode"`
	State    ClusterEgressState `json:"state"`
	Enforced bool               `json:"enforced"`
}

// Uses is an app's dependencies and what needs attention. Secrets, AI and
// Egress are nil, and named in Unavailable, when their source could not be
// asked; the resources are then listed with the status unknown.
type Uses struct {
	Access    UsesAccess     `json:"access"`
	Identity  UsesIdentity   `json:"identity"`
	Resources []UsedResource `json:"resources"`
	Secrets   *UsedSecrets   `json:"secrets"`
	// Variables counts the plain (non-secret) variables.
	Variables int         `json:"variables"`
	AI        *UsedAI     `json:"ai"`
	Egress    *UsedEgress `json:"egress"`
	// Unavailable are the sections that could not be read now.
	Unavailable []UsesSection `json:"unavailable"`
	Attention   []Attention   `json:"attention"`
}

// NeedsPeople is true when someone must act: any attention that is not just
// in progress (who is not wait).
func (u Uses) NeedsPeople() bool {
	return slices.ContainsFunc(u.Attention, func(a Attention) bool { return a.Who != WhoWait })
}

// UsesInput is everything Uses is made of.
type UsesInput struct {
	Settings Settings
	// Serving: a release is online (an access change is pending only then).
	Serving bool
	// Active: a deployment is in progress (it applies the latest settings).
	Active bool
	// Applied is the settings revision the cluster was last told (nil: never).
	Applied   *int64
	Secrets   Secrets
	Resources []ResourceState
	AI        AIState
	Egress    ClusterEgress
	// Unavailable are the sources that could not be asked; their fields
	// above are ignored.
	Unavailable []UsesSection
}

func (in UsesInput) down(s UsesSection) bool { return slices.Contains(in.Unavailable, s) }

// NewUses puts the app's uses together and lists what needs attention.
func NewUses(in UsesInput) Uses {
	s := in.Settings.Normalize()
	pending, failed := configPending(s, in)
	u := Uses{Access: UsesAccess{Mode: s.Access.Mode, Pending: pending, Failed: failed}, Identity: UsesIdentity{Verifiable: s.Access.Mode.Protected()},
		Variables: len(s.Env), Resources: make([]UsedResource, 0, len(s.Resources)), Attention: []Attention{}, Unavailable: []UsesSection{}}
	for _, sec := range UsesSections { // in a fixed order
		if in.down(sec) {
			u.Unavailable = append(u.Unavailable, sec)
		}
	}
	var att []Attention
	att = append(att, u.addSecrets(in)...)
	att = append(att, u.addResources(s, in)...)
	att = append(att, u.addAI(s, in)...)
	switch {
	case failed:
		att = append(att, Attention{AttentionConfigFailed, WhoOwner, "No se pudieron aplicar los últimos cambios de configuración. Vuelve a guardar el cambio para reintentar."})
	case pending:
		att = append(att, Attention{AttentionConfigPending, WhoWait, "Aplicando los últimos cambios de configuración."})
	}
	att = append(att, u.addEgress(in)...)
	// the closed list's order, stable inside a kind
	idx := func(a Attention) int { return slices.Index(AttentionKinds, a.Kind) }
	slices.SortStableFunc(att, func(a, b Attention) int { return idx(a) - idx(b) })
	u.Attention = append(u.Attention, att...)
	return u
}

func (u *Uses) addSecrets(in UsesInput) []Attention {
	if in.down(SectionSecrets) {
		return nil
	}
	sec := &UsedSecrets{Total: len(in.Secrets.Items), Missing: []string{}}
	u.Secrets = sec
	noKey := in.Secrets.Cluster.Key == nil // the cluster cannot take a managed value (or there is no cluster)
	var blocked, vault int
	for _, it := range in.Secrets.Items {
		switch {
		case it.Source == SecretExternal:
			sec.External++
			if it.State == SecretStateUnavailable {
				vault++
			}
		case it.State == SecretStateLoaded:
		case noKey:
			blocked++
		default: // missing, reload (the key changed) or failed: its people load it again
			sec.Missing = append(sec.Missing, it.Name)
		}
	}
	sec.Unavailable = blocked + vault
	var att []Attention
	if len(sec.Missing) > 0 {
		att = append(att, Attention{AttentionSecretMissing, WhoOwner, missingMessage(sec.Missing)})
	}
	if blocked > 0 {
		att = append(att, Attention{AttentionSecretsUnavailable, WhoIT, "El clúster de esta app todavía no puede recibir datos privados. TI debe revisarlo."})
	}
	if vault > 0 {
		att = append(att, Attention{AttentionSecretsUnavailable, WhoIT, vaultMessage(vault)})
	}
	return att
}

func (u *Uses) addResources(s Settings, in UsesInput) []Attention {
	var att []Attention
	for _, r := range s.Resources {
		used := UsedResource{Name: r.Name, Kind: r.Kind, Prefix: r.Prefix, Status: ResourceUnknown, Variables: r.Variables()}
		if !in.down(SectionResources) {
			if st, ok := resourceState(in.Resources, r.Name); ok {
				used.Status, used.Detail = st.Status, st.Message
			} else {
				used.Status, used.Detail = ResourceFailed, "el recurso no existe"
			}
		}
		u.Resources = append(u.Resources, used)
		switch used.Status {
		case ResourceFailed:
			att = append(att, Attention{AttentionResourceFailed, WhoIT, fmt.Sprintf("No se pudo preparar %s «%s». Pide ayuda a TI.", theKind(r.Kind), r.Name)})
		case ResourcePreparing:
			att = append(att, Attention{AttentionResourceNotReady, WhoWait, fmt.Sprintf("%s «%s» todavía se está preparando.", capital(theKind(r.Kind)), r.Name)})
		case ResourceDeleting:
			att = append(att, Attention{AttentionResourceNotReady, WhoWait, fmt.Sprintf("%s «%s» se está eliminando.", capital(theKind(r.Kind)), r.Name)})
		}
	}
	return att
}

func (u *Uses) addAI(s Settings, in UsesInput) []Attention {
	if in.down(SectionAI) {
		return nil
	}
	status := in.AI.Status
	if status == "" {
		status = "off"
	}
	u.AI = &UsedAI{Available: in.AI.Available, Enabled: s.AI.Enabled, Status: status}
	var att []Attention
	switch {
	case status == AIRevokeFailed:
		att = append(att, Attention{AttentionAIError, WhoIT, "No se pudo retirar la llave de IA anterior. La app ya no la usa. TI debe revisarlo."})
	case in.AI.Warning && (status == "enabled" || status == "rotating"):
		att = append(att, Attention{AttentionAIError, WhoIT, "La app ya usa la llave nueva de IA, pero no se pudo retirar la anterior. TI debe revisarlo."})
	case s.AI.Enabled && !in.AI.Available:
		att = append(att, Attention{AttentionAIError, WhoIT, "La app usa la IA, pero ahora no está disponible para ella (el gateway de IA no está conectado o el clúster no puede darle IA). TI debe revisarlo."})
	}
	switch status {
	case "enabling":
		att = append(att, Attention{AttentionAIPending, WhoWait, "La IA se está activando."})
	case "rotating":
		att = append(att, Attention{AttentionAIPending, WhoWait, "La llave de IA se está renovando."})
	case "disabling":
		att = append(att, Attention{AttentionAIPending, WhoWait, "La IA se está desactivando."})
	}
	return att
}

func (u *Uses) addEgress(in UsesInput) []Attention {
	if in.down(SectionEgress) {
		return nil
	}
	e := in.Egress
	u.Egress = &UsedEgress{Mode: e.Mode, State: e.State, Enforced: e.Enforced}
	if !e.Available || e.Mode != EgressRestricted || e.Enforced {
		return nil
	}
	switch e.State {
	case EgressUnsupported:
		return []Attention{{AttentionEgressNotApplied, WhoIT, "El clúster no puede aplicar la política de salida. TI debe revisarlo."}}
	case EgressFailed:
		return []Attention{{AttentionEgressNotApplied, WhoIT, "El agente no pudo aplicar la política de salida. TI debe revisarlo."}}
	}
	return []Attention{{AttentionEgressNotApplied, WhoWait, "La salida a internet está limitada y el clúster todavía está aplicando la política."}}
}

// configPending: the serving app has not received the settings in force yet.
// failed: and no deployment is applying them (a config deployment that
// failed does not start another: saving the change again retries).
func configPending(s Settings, in UsesInput) (pending, failed bool) {
	if !in.Serving {
		return false, false
	}
	applied := int64(-1) // the cluster was never told a mode
	if in.Applied != nil {
		applied = *in.Applied
	}
	pending = s.Revision > applied
	return pending, pending && !in.Active
}

func resourceState(all []ResourceState, name string) (ResourceState, bool) {
	i := slices.IndexFunc(all, func(r ResourceState) bool { return r.Name == name })
	if i < 0 {
		return ResourceState{}, false
	}
	return all[i], true
}

func theKind(k resourcekind.Kind) string { return resourcekind.Of(k).The() }

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func missingMessage(names []string) string {
	if len(names) == 1 {
		return "Falta cargar 1 secreto: la app no lo recibe hasta entonces."
	}
	return fmt.Sprintf("Faltan por cargar %d secretos: la app no los recibe hasta entonces.", len(names))
}

func vaultMessage(n int) string {
	if n == 1 {
		return "No pudimos leer 1 secreto de la bóveda. TI debe revisarlo."
	}
	return fmt.Sprintf("No pudimos leer %d secretos de la bóveda. TI debe revisarlo.", n)
}
