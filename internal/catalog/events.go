package catalog

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the catalog's public contract. Payloads carry facts; add
// fields freely, rename or remove one only in a new version.
const (
	EventOfferingUpdated = "catalog.offering_updated"
	EventRecipePublished = "catalog.recipe_published"
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
	// Teams are the IAMKit group ids it is limited to (empty: everyone).
	// Added within v1.
	Teams []string `json:"teams"`
}

type recipeName string

func (r recipeName) String() string { return string(r) }

func NewOfferingUpdated(org identity.OrganizationID, actor identity.UserID, title string, o Offering) event.Event {
	summary := "Cambió la oferta de «" + title + "» en el catálogo"
	if !o.Active {
		summary = "Dejó de ofrecer «" + title + "» en el catálogo"
	}
	teams := o.Teams
	if teams == nil {
		teams = []string{}
	}
	return event.New(EventOfferingUpdated, 1, org, actor, event.Subject(SubjectKind, recipeName(o.Recipe)), summary,
		OfferingUpdated{Recipe: o.Recipe, Version: o.Version, Active: o.Active, Tiers: tierNames(o.Tiers), Defaults: tierNames(o.Defaults),
			Limits: o.Limits, Preapproved: o.Preapproved, Revision: o.Revision, Teams: teams})
}

// RecipePublished v1: a version of the organization's own recipe (version 1
// registers it, with an inactive offering). It names the secret stores the
// connection reads from (who pointed a recipe at the company's vault is in
// the trail), never the connection itself: no host, no key, no value.
type RecipePublished struct {
	Recipe  string   `json:"recipe"`
	Version int      `json:"version"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Engine  string   `json:"engine"`
	Stores  []string `json:"stores"`
}

// NewRecipePublished is the event of recipe@version, just published; stores
// are the stores its connection reads from.
func NewRecipePublished(org identity.OrganizationID, actor identity.UserID, recipe, title, kind, engine string, version int, stores []string) event.Event {
	summary := "Registró «" + title + "», un recurso que ya existe en la empresa, en el catálogo"
	if version > 1 {
		summary = "Publicó una versión nueva de «" + title + "» en el catálogo"
	}
	if stores == nil {
		stores = []string{}
	}
	return event.New(EventRecipePublished, 1, org, actor, event.Subject(SubjectKind, recipeName(recipe)), summary,
		RecipePublished{Recipe: recipe, Version: version, Kind: kind, Title: title, Engine: engine, Stores: stores})
}

func tierNames[T ~string](ts []T) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
