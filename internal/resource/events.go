package resource

import (
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Event types: the resource domain's public contract. Payloads carry facts
// and IDs, never credentials; add fields freely, rename or remove one only
// in a new version.
const (
	EventCreated = "resource.created"
	EventResized = "resource.resized"
	EventDeleted = "resource.deleted"
	// EventRemoved: the cluster confirmed the data erased and the resource
	// was forgotten.
	EventRemoved = "resource.removed"
)

// SubjectKind prefixes the resource's event subjects ("resource/<id>").
const SubjectKind = "resource"

// Names are the human names events are summarised with.
type Names struct {
	Project     string
	Environment string
}

func (n Names) where() string { return n.Project + " (" + n.Environment + ")" }

// Created v1.
type Created struct {
	Resource    ID     `json:"resource_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	StorageGi   int    `json:"storage_gi"`
	// Recipe@RecipeVersion fulfils it, with Params (additive in v1: absent
	// in events from before the catalog).
	Recipe        string         `json:"recipe,omitempty"`
	RecipeVersion int            `json:"recipe_version,omitempty"`
	Params        map[string]int `json:"params,omitempty"`
}

// Resized v1.
type Resized struct {
	Resource    ID     `json:"resource_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	FromGi      int    `json:"from_gi"`
	ToGi        int    `json:"to_gi"`
	// Params are all of them after the change (additive in v1).
	Params map[string]int `json:"params,omitempty"`
}

// Deleted v1: the resource and its data are to be erased.
type Deleted struct {
	Resource    ID     `json:"resource_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
}

// Removed v1.
type Removed = Deleted

func NewCreated(org identity.OrganizationID, actor identity.UserID, r Resource, n Names) event.Event {
	return event.New(EventCreated, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Creó "+withArticle(r)+" "+r.Name+" en "+n.where(),
		Created{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind), StorageGi: r.StorageGi,
			Recipe: r.Recipe, RecipeVersion: r.RecipeVersion, Params: r.Params})
}

func NewResized(org identity.OrganizationID, actor identity.UserID, r Resource, from int, n Names) event.Event {
	return event.New(EventResized, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Agrandó "+r.Name+" de "+gi(from)+" a "+gi(r.StorageGi)+" en "+n.where(),
		Resized{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind), FromGi: from, ToGi: r.StorageGi,
			Params: r.Params})
}

// NewDeleted is r on its way out; owned false (one the company already has)
// deletes only its connection: the company's data is not touched.
func NewDeleted(org identity.OrganizationID, actor identity.UserID, r Resource, n Names, owned bool) event.Event {
	summary := "Eliminó " + withArticle(r) + " " + r.Name + " de " + n.where() + " y sus datos"
	if !owned {
		summary = "Desconectó " + withArticle(r) + " " + r.Name + " de " + n.where() + " (ya existía en la empresa: sus datos no se tocan)"
	}
	return event.New(EventDeleted, 1, org, actor, event.Subject(SubjectKind, r.ID), summary,
		Deleted{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind)})
}

// NewRemoved is r gone from the cluster (owned: as in NewDeleted).
func NewRemoved(org identity.OrganizationID, r Resource, owned bool) event.Event {
	summary := "El clúster borró los datos de " + r.Name
	if !owned {
		summary = "El clúster quitó la conexión de " + r.Name
	}
	return event.New(EventRemoved, 1, org, identity.UserID{}, event.Subject(SubjectKind, r.ID), summary,
		Removed{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind)})
}

func withArticle(r Resource) string { return resourcekind.Of(r.Kind).The() }

func gi(n int) string { return strconv.Itoa(n) + " GiB" }
