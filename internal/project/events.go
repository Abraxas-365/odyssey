package project

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/text"
)

// Event types: the project's public contract. Payloads carry facts and IDs;
// add fields freely, rename or remove one only in a new version.
const (
	EventCreated     = "project.created"
	EventUpdated     = "project.updated"
	EventArchived    = "project.archived"
	EventTierChanged = "project.tier_changed"
	// EventServicesChanged: the app's services were replaced.
	EventServicesChanged = "project.services_changed"

	EventEnvironmentCreated = "environment.created"
)

// SubjectKind prefixes the project's event subjects ("project/<id>").
const SubjectKind = "project"

// EnvironmentSubjectKind prefixes environment subjects ("environment/<id>").
const EnvironmentSubjectKind = "environment"

// Created v1.
type Created struct {
	Project ID     `json:"project_id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
}

// EnvironmentCreated v1.
type EnvironmentCreated struct {
	Environment EnvironmentID   `json:"environment_id"`
	Project     ID              `json:"project_id"`
	Slug        string          `json:"slug"`
	Kind        EnvironmentKind `json:"kind"`
}

// Updated v1: only the fields that changed are set.
type Updated struct {
	Project     ID      `json:"project_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// Archived v1.
type Archived struct {
	Project ID     `json:"project_id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
}

// TierChanged v1.
type TierChanged struct {
	Project ID     `json:"project_id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	From    Tier   `json:"from"`
	To      Tier   `json:"to"`
}

func NewTierChanged(org identity.OrganizationID, actor identity.UserID, p Project, to Tier) event.Event {
	return event.New(EventTierChanged, 1, org, actor, event.Subject(SubjectKind, p.ID),
		"Cambió el nivel de la app "+p.Name+" a "+string(to),
		TierChanged{Project: p.ID, Name: p.Name, Slug: p.Slug, From: p.Tier, To: to})
}

// ServicesChanged v1: the whole list before and after.
type ServicesChanged struct {
	Project ID             `json:"project_id"`
	Name    string         `json:"name"`
	Slug    string         `json:"slug"`
	From    []ServiceEntry `json:"from"`
	To      []ServiceEntry `json:"to"`
}

// ServiceEntry is a service in an event payload.
type ServiceEntry struct {
	Name        string      `json:"name"`
	Kind        ServiceKind `json:"kind"`
	Root        string      `json:"root"`
	Route       string      `json:"route"`
	StripPrefix bool        `json:"strip_prefix"`
}

func entries(services []Service) []ServiceEntry {
	out := make([]ServiceEntry, len(services))
	for i, s := range services {
		out[i] = ServiceEntry{Name: s.Name, Kind: s.Kind, Root: s.Root, Route: s.Route, StripPrefix: s.StripPrefix}
	}
	return out
}

func NewServicesChanged(org identity.OrganizationID, actor identity.UserID, p Project, from, to []Service) event.Event {
	return event.New(EventServicesChanged, 1, org, actor, event.Subject(SubjectKind, p.ID),
		"Cambió los servicios de la app "+p.Name,
		ServicesChanged{Project: p.ID, Name: p.Name, Slug: p.Slug, From: entries(from), To: entries(to)})
}

func NewCreated(org identity.OrganizationID, actor identity.UserID, id ID, name, slug string) event.Event {
	return event.New(EventCreated, 1, org, actor, event.Subject(SubjectKind, id),
		"Creó la app "+name, Created{Project: id, Name: name, Slug: slug})
}

func NewUpdated(org identity.OrganizationID, actor identity.UserID, current Project, changes Update) event.Event {
	clean := func(s *string) *string {
		if s == nil {
			return nil
		}
		v := text.Clean(*s)
		return &v
	}
	p := Updated{Project: current.ID, Name: clean(changes.Name), Description: clean(changes.Description)}
	name := current.Name
	if p.Name != nil {
		name = *p.Name
	}
	return event.New(EventUpdated, 1, org, actor, event.Subject(SubjectKind, current.ID), "Editó la app "+name, p)
}

func NewEnvironmentCreated(org identity.OrganizationID, actor identity.UserID, e Environment, projectName string) event.Event {
	return event.New(EventEnvironmentCreated, 1, org, actor, event.Subject(EnvironmentSubjectKind, e.ID),
		"Creó el entorno "+e.Slug+" de "+projectName,
		EnvironmentCreated{Environment: e.ID, Project: e.Project, Slug: e.Slug, Kind: e.Kind})
}

func NewArchived(org identity.OrganizationID, actor identity.UserID, p Project) event.Event {
	return event.New(EventArchived, 1, org, actor, event.Subject(SubjectKind, p.ID),
		"Archivó la app "+p.Name, Archived{Project: p.ID, Name: p.Name, Slug: p.Slug})
}
