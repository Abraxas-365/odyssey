package source

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the source domain's public contract. Payloads carry facts and
// IDs; add fields freely, rename or remove one only in a new version.
const (
	EventInstallationLinked   = "source.installation_linked"
	EventInstallationUnlinked = "source.installation_unlinked"
	EventConnected            = "source.connected"
	EventDisconnected         = "source.disconnected"
	// EventPushReceived: a commit to publish was queued (the worker imports it).
	EventPushReceived = "source.push_received"
	EventPushDeployed = "source.push_deployed"
	EventPushFailed   = "source.push_failed"
)

// Subject kinds ("installation/<id>", "connection/<id>", "push/<id>").
const (
	InstallationSubjectKind = "installation"
	ConnectionSubjectKind   = "connection"
	PushSubjectKind         = "push"
)

// InstallationLinked v1 (also on a refresh of a linked installation).
type InstallationLinked struct {
	Installation InstallationID `json:"installation_id"`
	ExternalID   int64          `json:"external_id"`
	Account      string         `json:"account"`
}

// InstallationUnlinked v1: by someone (Actor set) or because the App was
// uninstalled on the provider. Connections lists those that stopped.
type InstallationUnlinked struct {
	Installation InstallationID `json:"installation_id"`
	Account      string         `json:"account"`
	Connections  []ConnectionID `json:"connection_ids"`
}

// Connected v1 (also on a reconnect with another repository or branch).
type Connected struct {
	Connection ConnectionID `json:"connection_id"`
	Project    string       `json:"project_id"`
	Repository string       `json:"repository"`
	Branch     string       `json:"branch"`
	Root       string       `json:"root"`
}

// Disconnected v1.
type Disconnected struct {
	Connection ConnectionID `json:"connection_id"`
	Project    string       `json:"project_id"`
	Repository string       `json:"repository"`
	// Reason: ReasonUser, ReasonUnlinked, ReasonUninstalled or ReasonRepositoryRemoved.
	Reason string `json:"reason"`
}

// Why a connection stopped.
const (
	ReasonUser              = "user"               // someone disconnected the app
	ReasonUnlinked          = "unlinked"           // someone unlinked the installation in Odicea
	ReasonUninstalled       = "uninstalled"        // the App was uninstalled on GitHub
	ReasonRepositoryRemoved = "repository_removed" // the installation no longer reaches the repository
)

// PushReceived v1.
type PushReceived struct {
	Push       PushID       `json:"push_id"`
	Connection ConnectionID `json:"connection_id"`
	Project    string       `json:"project_id"`
	Repository string       `json:"repository"`
	Branch     string       `json:"branch"`
	Commit     string       `json:"commit"`
	Trigger    Trigger      `json:"trigger"`
}

// PushDeployed v1: the commit became a deployment (which may still fail:
// deployment events follow it).
type PushDeployed struct {
	Push       PushID `json:"push_id"`
	Project    string `json:"project_id"`
	Commit     string `json:"commit"`
	Deployment string `json:"deployment_id"`
}

// PushFailed v1: the commit could not be imported or deployed.
type PushFailed struct {
	Push    PushID `json:"push_id"`
	Project string `json:"project_id"`
	Commit  string `json:"commit"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Installation events.

func NewInstallationLinked(i Installation, actor identity.UserID) event.Event {
	return event.New(EventInstallationLinked, 1, i.Organization, actor,
		event.Subject(InstallationSubjectKind, i.ID), "Se conectó GitHub ("+i.Account+")",
		InstallationLinked{Installation: i.ID, ExternalID: i.ExternalID, Account: i.Account})
}

func NewInstallationUnlinked(i Installation, actor identity.UserID, connections []ConnectionID) event.Event {
	if connections == nil {
		connections = []ConnectionID{}
	}
	return event.New(EventInstallationUnlinked, 1, i.Organization, actor,
		event.Subject(InstallationSubjectKind, i.ID), "Se desconectó GitHub ("+i.Account+")",
		InstallationUnlinked{Installation: i.ID, Account: i.Account, Connections: connections})
}

// Connection events.

func NewConnected(c Connection, actor identity.UserID) event.Event {
	return event.New(EventConnected, 1, c.Organization, actor,
		event.Subject(ConnectionSubjectKind, c.ID), "Se conectó "+c.Repository+" ("+c.Branch+")",
		Connected{Connection: c.ID, Project: c.Project.String(), Repository: c.Repository, Branch: c.Branch, Root: c.Root})
}

func NewDisconnected(c Connection, actor identity.UserID, reason string) event.Event {
	return event.New(EventDisconnected, 1, c.Organization, actor,
		event.Subject(ConnectionSubjectKind, c.ID), "Se desconectó "+c.Repository,
		Disconnected{Connection: c.ID, Project: c.Project.String(), Repository: c.Repository, Reason: reason})
}

// Push events.

func NewPushReceived(p Push) event.Event {
	return event.New(EventPushReceived, 1, p.Organization, p.RequestedBy,
		event.Subject(PushSubjectKind, p.ID), "Nuevo commit "+p.Git.ShortCommit()+" en "+p.Git.Branch,
		PushReceived{Push: p.ID, Connection: p.Connection, Project: p.Project.String(), Repository: p.Git.Repository,
			Branch: p.Git.Branch, Commit: p.Git.Commit, Trigger: p.Trigger})
}

func NewPushDeployed(p Push) event.Event {
	deployment := ""
	if p.Deployment != nil {
		deployment = p.Deployment.String()
	}
	return event.New(EventPushDeployed, 1, p.Organization, p.RequestedBy,
		event.Subject(PushSubjectKind, p.ID), "Publicando el commit "+p.Git.ShortCommit(),
		PushDeployed{Push: p.ID, Project: p.Project.String(), Commit: p.Git.Commit, Deployment: deployment})
}

func NewPushFailed(p Push) event.Event {
	f := Failure{}
	if p.Error != nil {
		f = *p.Error
	}
	return event.New(EventPushFailed, 1, p.Organization, p.RequestedBy,
		event.Subject(PushSubjectKind, p.ID), "No se pudo publicar el commit "+p.Git.ShortCommit(),
		PushFailed{Push: p.ID, Project: p.Project.String(), Commit: p.Git.Commit, Code: f.Code, Message: f.Message})
}
