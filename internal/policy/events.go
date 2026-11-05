package policy

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the policy's public contract. Payloads carry facts and IDs;
// add fields freely, rename or remove one only in a new version.
const (
	EventRuleCreated = "policy.rule_created"
	EventRuleUpdated = "policy.rule_updated"
	EventRuleDeleted = "policy.rule_deleted"
)

// SubjectKind prefixes the rule's event subjects ("policy_rule/<id>").
const SubjectKind = "policy_rule"

// RuleCreated v1.
type RuleCreated struct {
	Rule       RuleID `json:"rule_id"`
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Effect     Effect `json:"effect"`
	Enabled    bool   `json:"enabled"`
}

// RuleUpdated v1: the rule as it is now, and its version.
type RuleUpdated struct {
	Rule       RuleID `json:"rule_id"`
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Effect     Effect `json:"effect"`
	Enabled    bool   `json:"enabled"`
	Version    int    `json:"version"`
	Builtin    string `json:"builtin,omitempty"`
}

// RuleDeleted v1.
type RuleDeleted struct {
	Rule RuleID `json:"rule_id"`
	Name string `json:"name"`
}

func NewRuleCreated(org identity.OrganizationID, actor identity.UserID, r Rule) event.Event {
	return event.New(EventRuleCreated, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Creó la regla de TI «"+r.Name+"»",
		RuleCreated{Rule: r.ID, Name: r.Name, Expression: r.Expression, Effect: r.Effect, Enabled: r.Enabled})
}

func NewRuleUpdated(org identity.OrganizationID, actor identity.UserID, r Rule) event.Event {
	summary := "Cambió la regla de TI «" + r.Name + "»"
	if r.IsBuiltin() {
		summary = "Encendió la regla de TI «" + r.Name + "»"
		if !r.Enabled {
			summary = "Apagó la regla de TI «" + r.Name + "»"
		}
	}
	p := RuleUpdated{Rule: r.ID, Name: r.Name, Expression: r.Expression, Effect: r.Effect, Enabled: r.Enabled, Version: r.Version}
	if r.Builtin != nil {
		p.Builtin = *r.Builtin
	}
	return event.New(EventRuleUpdated, 1, org, actor, event.Subject(SubjectKind, r.ID), summary, p)
}

func NewRuleDeleted(org identity.OrganizationID, actor identity.UserID, r Rule) event.Event {
	return event.New(EventRuleDeleted, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Eliminó la regla de TI «"+r.Name+"»", RuleDeleted{Rule: r.ID, Name: r.Name})
}
