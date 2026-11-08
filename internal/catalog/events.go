package catalog

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the catalog's public contract. Payloads carry facts; add
// fields freely, rename or remove one only in a new version.
const (
	EventOfferingUpdated = "catalog.offering_updated"
)

// SubjectKind prefixes the offering's event subjects ("catalog_recipe/<recipe>").
const SubjectKind = "catalog_recipe"

// OfferingUpdated v1: the offering as it is now.
type OfferingUpdated struct {
	Recipe      string           `json:"recipe"`
	Version     int              `json:"version"`
	Active      bool             `json:"active"`
	Tiers       []string         `json:"tiers"`
	Defaults    []string         `json:"defaults"`
	Limits      map[string]Range `json:"limits"`
	Preapproved map[string]Range `json:"preapproved"`
	Revision    int              `json:"revision"`
}

type recipeName string

func (r recipeName) String() string { return string(r) }

func NewOfferingUpdated(org identity.OrganizationID, actor identity.UserID, title string, o Offering) event.Event {
	summary := "Cambió la oferta de «" + title + "» en el catálogo"
	if !o.Active {
		summary = "Dejó de ofrecer «" + title + "» en el catálogo"
	}
	return event.New(EventOfferingUpdated, 1, org, actor, event.Subject(SubjectKind, recipeName(o.Recipe)), summary,
		OfferingUpdated{Recipe: o.Recipe, Version: o.Version, Active: o.Active, Tiers: tierNames(o.Tiers), Defaults: tierNames(o.Defaults),
			Limits: o.Limits, Preapproved: o.Preapproved, Revision: o.Revision})
}

func tierNames[T ~string](ts []T) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
