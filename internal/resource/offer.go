package resource

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Offered is the catalog as one environment sees it (docs/catalog.md): what
// can be asked for there, given its app's tier and what its cluster can
// honour, and how (at once or with IT's approval).
type Offered struct {
	Tier project.Tier `json:"tier"`
	// Recipes are what can be created, in kind order with each kind's
	// default first.
	Recipes []Offer `json:"recipes"`
	// Unavailable are the kinds nothing fulfils here, with why.
	Unavailable []UnavailableKind `json:"unavailable"`
}

// Offer is one recipe that can be created in the environment.
type Offer struct {
	Kind        resourcekind.Kind `json:"kind"`
	Recipe      string            `json:"recipe"`
	Version     int               `json:"recipe_version"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	// Default says it is what a request naming only the kind gets.
	Default   bool            `json:"default"`
	Params    []OfferParam    `json:"params"`
	Variables []OfferVariable `json:"variables"`
	// Verdict is what create_resource's default gets now — this recipe with
	// its default parameters, connected to the whole app: allow (created at
	// once), approval (held for IT) or deny. A value outside a parameter's
	// pre-approved range asks IT whatever this says, and connecting only
	// some services is judged on those.
	Verdict gate.Effect `json:"verdict"`
	// Rules name what decided a verdict other than allow.
	Rules []string `json:"rules"`
	// RulesVerdict is Verdict without the catalog's own floor (a default
	// outside the pre-approved range): what IT's rules say, so a client can
	// forecast other values (outside that range → approval, inside → this).
	RulesVerdict gate.Effect `json:"rules_verdict"`
}

// OfferParam is a parameter with the offering's limits: a value outside
// [Min, Max] is refused; one outside the pre-approved range asks IT.
type OfferParam struct {
	Name           string             `json:"name"`
	Label          string             `json:"label"`
	Unit           string             `json:"unit"`
	Min            int                `json:"min"`
	Max            int                `json:"max"`
	Default        int                `json:"default"`
	PreapprovedMin int                `json:"preapproved_min"`
	PreapprovedMax int                `json:"preapproved_max"`
	Mutability     catalog.Mutability `json:"mutability"`
}

// OfferVariable is one variable the app receives (its value never leaves
// the cluster).
type OfferVariable struct {
	Name    string `json:"name"`
	Secret  bool   `json:"secret"`
	Meaning string `json:"meaning"`
}

// NoClusterReason is why nothing can be created where the environment's
// cluster announces nothing (none enrolled, revoked, or an agent that
// prepares no resource).
const NoClusterReason = "Esta app todavía no tiene un clúster que pueda crear recursos. Pide a TI que conecte uno o actualice su agente."

// UnavailableKind is a kind that cannot be created in the environment.
type UnavailableKind struct {
	Kind   resourcekind.Kind `json:"kind"`
	Reason string            `json:"reason"`
}

// NewOffer words entry as offered in tier, with the verdict on creating it
// with defaults (its parameters' defaults): v all of it, rules IT's rules
// alone.
func NewOffer(e catalog.Entry, tier project.Tier, v, rules gate.Verdict) Offer {
	o := Offer{Kind: e.Kind, Recipe: e.Recipe, Version: e.Version, Title: e.Title, Description: e.Description,
		Default: e.Offering.DefaultIn(tier), Params: make([]OfferParam, 0, len(e.Params)), Variables: []OfferVariable{},
		Verdict: v.Effect, Rules: []string{}, RulesVerdict: rules.Effect}
	if o.Verdict == "" {
		o.Verdict = gate.Allow
	}
	if o.RulesVerdict == "" {
		o.RulesVerdict = gate.Allow
	}
	for _, p := range e.Params {
		b, pre := e.Offering.Bounds(p), e.Offering.PreapprovedRange(p)
		o.Params = append(o.Params, OfferParam{Name: p.Name, Label: p.Label, Unit: p.Unit, Min: b.Min, Max: b.Max,
			Default: e.Offering.DefaultOf(p), PreapprovedMin: pre.Min, PreapprovedMax: pre.Max, Mutability: p.Mutability})
	}
	for _, x := range resourcekind.Of(e.Kind).Variables {
		o.Variables = append(o.Variables, OfferVariable{Name: x.Name, Secret: x.Secret, Meaning: x.Meaning})
	}
	if o.Verdict != gate.Allow {
		for _, r := range v.Rules {
			if !slices.Contains(o.Rules, r.Name) {
				o.Rules = append(o.Rules, r.Name)
			}
		}
	}
	return o
}
