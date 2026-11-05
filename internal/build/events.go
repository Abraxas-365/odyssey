package build

import (
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Event types: the build domain's public contract. Payloads carry facts and
// IDs; add fields freely, rename or remove one only in a new version. What a
// runner reports while a build runs (phases, log lines) are observations,
// not events: these carry their outcome.
const (
	EventSourceUploaded = "source.uploaded"
	EventSourceExpired  = "source.expired"
	EventStarted        = "build.started"
	EventSucceeded      = "build.succeeded"
	EventFailed         = "build.failed"
	EventCancelled      = "build.cancelled"
	EventScanned        = "build.scanned"
)

// Subject kinds ("source/<id>", "build/<id>").
const (
	SourceSubjectKind = "source"
	SubjectKind       = "build"
)

// SourceUploaded v1.
type SourceUploaded struct {
	Source       SourceID `json:"source_id"`
	Project      string   `json:"project_id"`
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	SHA256       string   `json:"sha256"`
	Files        int      `json:"files"`
	UnpackedSize int64    `json:"unpacked_size"`
}

// SourceExpired v1: its bytes were deleted (the images built from it stay).
type SourceExpired struct {
	Source  SourceID `json:"source_id"`
	Project string   `json:"project_id"`
}

// Started v1.
type Started struct {
	Build       ID       `json:"build_id"`
	Project     string   `json:"project_id"`
	Environment string   `json:"environment_id"`
	Source      SourceID `json:"source_id"`
}

// Succeeded v1: Image is pinned by digest.
type Succeeded struct {
	Build       ID     `json:"build_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Image       string `json:"image"`
}

// Failed v1.
type Failed struct {
	Build       ID     `json:"build_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail"`
}

// Cancelled v1.
type Cancelled struct {
	Build       ID     `json:"build_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
}

// Scanned v1: the image's vulnerability scan was recorded. Error is set (and
// the counts mean nothing) when the image could not be scanned.
type Scanned struct {
	Build       ID     `json:"build_id"`
	Project     string `json:"project_id"`
	Environment string `json:"environment_id"`
	Image       string `json:"image"`
	Critical    int    `json:"critical"`
	High        int    `json:"high"`
	Medium      int    `json:"medium"`
	Low         int    `json:"low"`
	Tool        string `json:"tool"`
	Error       string `json:"error"`
}

// Names are the human names events are summarised with.
type Names struct {
	Project string
}

func NewSourceUploaded(org identity.OrganizationID, actor identity.UserID, s Source, n Names) event.Event {
	return event.New(EventSourceUploaded, 1, org, actor, event.Subject(SourceSubjectKind, s.ID),
		"Subió el código de "+n.Project,
		SourceUploaded{Source: s.ID, Project: s.Project.String(), Name: s.Name, Size: s.Size, SHA256: s.SHA256,
			Files: s.Files, UnpackedSize: s.UnpackedSize})
}

func NewSourceExpired(org identity.OrganizationID, s Source) event.Event {
	return event.New(EventSourceExpired, 1, org, identity.UserID{}, event.Subject(SourceSubjectKind, s.ID),
		"Se borró un código subido antiguo",
		SourceExpired{Source: s.ID, Project: s.Project.String()})
}

func NewStarted(org identity.OrganizationID, actor identity.UserID, b Build, n Names) event.Event {
	return event.New(EventStarted, 1, org, actor, event.Subject(SubjectKind, b.ID),
		"Empezó a construir "+n.Project,
		Started{Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String(), Source: b.Source})
}

func NewSucceeded(org identity.OrganizationID, b Build, n Names) event.Event {
	return event.New(EventSucceeded, 1, org, b.RequestedBy, event.Subject(SubjectKind, b.ID),
		"Construyó "+n.Project,
		Succeeded{Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String(), Image: b.Image})
}

func NewFailed(org identity.OrganizationID, b Build, f Failure, n Names) event.Event {
	return event.New(EventFailed, 1, org, b.RequestedBy, event.Subject(SubjectKind, b.ID),
		"Falló al construir "+n.Project,
		Failed{Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String(),
			Code: f.Code, Message: f.Message, Detail: f.Detail})
}

func NewCancelled(org identity.OrganizationID, actor identity.UserID, b Build, n Names) event.Event {
	return event.New(EventCancelled, 1, org, actor, event.Subject(SubjectKind, b.ID),
		"Canceló la construcción de "+n.Project,
		Cancelled{Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String()})
}

// NewScanned is the event of b's recorded scan (b.Scan set). The actor is
// nobody: the scan is the platform's observation, not someone's action.
func NewScanned(org identity.OrganizationID, b Build, n Names) event.Event {
	s := *b.Scan
	summary := "Revisó las vulnerabilidades de " + n.Project
	if !s.Known() {
		summary = "No pudo revisar las vulnerabilidades de " + n.Project
	}
	return event.New(EventScanned, 1, org, identity.UserID{}, event.Subject(SubjectKind, b.ID), summary,
		Scanned{Build: b.ID, Project: b.Project.String(), Environment: b.Environment.String(), Image: b.Image,
			Critical: s.Critical, High: s.High, Medium: s.Medium, Low: s.Low, Tool: s.Tool, Error: s.Error})
}
