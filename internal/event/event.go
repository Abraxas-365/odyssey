// Package event is how one domain reacts to another without importing it: the
// emitting service publishes a domain event in the same transaction as its
// write (transactional outbox, adapters/outboxpg) and the reacting domain is
// subscribed to it in the composition root. The audit trail is one more
// subscriber, so no write can reach the database without its audit entry.
//
//	err := s.tx.Do(ctx, func(ctx context.Context) error {
//		if err := s.repo.Create(ctx, org, id, input); err != nil {
//			return err
//		}
//		return s.events.Publish(ctx, project.NewCreated(org, input.CreatedBy, p))
//	})
//
// Events are business facts: they live in <domain>/events.go, are named
// "<domain>.<verb_past>" and carry a versioned payload of facts and IDs (never
// another domain's types). Add payload fields freely; renaming or removing one
// is a new version. Each published version has a golden JSON contract.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/google/uuid"
)

// Event is the envelope stored in the outbox and handed to subscribers.
type Event struct {
	ID           uuid.UUID               `json:"id"`
	Type         string                  `json:"type"`    // "<domain>.<verb_past>", e.g. "project.created"
	Version      int                     `json:"version"` // version of the payload contract
	Organization identity.OrganizationID `json:"organization_id"`
	// Actor is the user who caused the event; zero for system actions.
	Actor identity.UserID `json:"actor_id"`
	// Subject is the entity the event is about: "<kind>/<id>" (Subject()).
	Subject string `json:"subject"`
	// Summary is one plain Spanish sentence for the activity trail.
	Summary    string          `json:"summary"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
	Meta
}

// Meta is cross-cutting provenance the domain never handles: it travels in
// the context (WithMeta, WithCause) and Publish stamps it on every event.
type Meta struct {
	// CorrelationID is shared by the whole chain an action started: the HTTP
	// request ID, or the first event's ID when nothing set one.
	CorrelationID string `json:"correlation_id"`
	// CausationID is the event whose handler published this one; zero when a
	// person (or the system) acted directly.
	CausationID uuid.UUID `json:"causation_id"`
	// Client is the OAuth client the actor acted through (the console, an MCP
	// agent such as Claude Code: "mcp:<client id>"), or what started the chain
	// when nobody did: ClientGitHub, ClientPlatform. Only the edges set it
	// (the HTTP and MCP middleware, the webhook and poll, the workflow
	// engine, the gateway, the platform's own restore; architecture test):
	// empty means it was not recorded.
	Client string `json:"client,omitempty"`
}

// Clients that are not an OAuth client.
const (
	// ClientGitHub: a push nobody in Odicea asked for (the webhook, the poll).
	ClientGitHub = "github"
	// ClientPlatform: the platform on its own (a schedule, a cluster's agent
	// reporting, the automatic restore).
	ClientPlatform = "platform"
	// ClientAPI: a REST token that names no OAuth client (a password grant).
	ClientAPI = "api"
)

var typePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// ValidType reports whether typ is "<domain>.<verb_past>" in snake_case.
func ValidType(typ string) bool { return typePattern.MatchString(typ) }

// New builds an event. Payloads are plain structs, so a marshal failure (or
// a malformed type) is a programming error and panics.
func New[P any](typ string, version int, org identity.OrganizationID, actor identity.UserID, subject, summary string, payload P) Event {
	if !ValidType(typ) || version < 1 {
		panic(fmt.Sprintf("event: invalid type %q or version %d", typ, version))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("event %s: payload: %v", typ, err))
	}
	return Event{
		ID: uuid.New(), Type: typ, Version: version, Organization: org, Actor: actor,
		Subject: subject, Summary: summary, Payload: raw, OccurredAt: time.Now().UTC(),
	}
}

// Subject formats an entity reference: Subject("project", id) → "project/<id>".
func Subject(kind string, id fmt.Stringer) string { return kind + "/" + id.String() }

// Decode unmarshals the payload. Readers are tolerant: unknown fields are ignored.
func Decode[P any](e Event) (P, error) {
	var p P
	err := json.Unmarshal(e.Payload, &p)
	return p, err
}

// Publisher stores events in the transaction carried by ctx. Outside a
// transaction it fails: nothing may be published without atomicity.
type Publisher interface {
	Publish(ctx context.Context, events ...Event) error
}

type metaKey struct{}

// WithMeta sets the provenance for everything published under ctx. The HTTP
// layer calls it once per request (request ID + OAuth client).
func WithMeta(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom is the provenance carried by ctx (zero when none).
func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	return m
}

// WithCause marks ctx as handling e: events published by the handler share
// e's correlation and client, and name e as their cause. The dispatcher calls
// it before every handler.
func WithCause(ctx context.Context, e Event) context.Context {
	return WithMeta(ctx, Meta{CorrelationID: e.CorrelationID, CausationID: e.ID, Client: e.Client})
}

// Stamp fills e's provenance from ctx. Publishers call it; an event that
// starts a chain correlates to itself.
func Stamp(ctx context.Context, e *Event) {
	m := MetaFrom(ctx)
	if e.CorrelationID == "" {
		e.CorrelationID = m.CorrelationID
	}
	if e.CorrelationID == "" {
		e.CorrelationID = e.ID.String()
	}
	if e.CausationID == uuid.Nil {
		e.CausationID = m.CausationID
	}
	if e.Client == "" {
		e.Client = m.Client
	}
}
