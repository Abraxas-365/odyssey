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
		Created{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind), StorageGi: r.StorageGi})
}

func NewResized(org identity.OrganizationID, actor identity.UserID, r Resource, from int, n Names) event.Event {
	return event.New(EventResized, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Agrandó "+r.Name+" de "+gi(from)+" a "+gi(r.StorageGi)+" en "+n.where(),
		Resized{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind), FromGi: from, ToGi: r.StorageGi})
}

func NewDeleted(org identity.OrganizationID, actor identity.UserID, r Resource, n Names) event.Event {
	return event.New(EventDeleted, 1, org, actor, event.Subject(SubjectKind, r.ID),
		"Eliminó "+withArticle(r)+" "+r.Name+" de "+n.where()+" y sus datos",
		Deleted{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind)})
}

func NewRemoved(org identity.OrganizationID, r Resource) event.Event {
	return event.New(EventRemoved, 1, org, identity.UserID{}, event.Subject(SubjectKind, r.ID),
		"El clúster borró los datos de "+r.Name,
		Removed{Resource: r.ID, Project: r.Project.String(), Environment: r.Environment.String(), Name: r.Name, Kind: string(r.Kind)})
}

func withArticle(r Resource) string {
	if r.Kind == resourcekind.Files {
		return "el " + KindLabel(r.Kind)
	}
	return "la " + KindLabel(r.Kind)
}

func gi(n int) string { return strconv.Itoa(n) + " GiB" }
