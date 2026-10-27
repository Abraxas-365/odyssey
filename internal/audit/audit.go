// Package audit is the organization's tamper-evident activity trail: every
// domain event becomes one entry, hash-chained to the previous entry of the
// same organization, so a deleted or edited row breaks verification.
//
// Nothing calls audit directly: it is an ordered subscriber of every event
// (wired in the composition root), which is why "every write publishes an
// event" is also "every write is audited".
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/google/uuid"
)

// Entry is one audited event.
type Entry struct {
	// ID is the event's ID: recording the same event twice is a no-op.
	ID           uuid.UUID               `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	// Seq is the entry's position in its organization's chain (1, 2, …).
	Seq           int64           `json:"seq" db:"seq"`
	Type          string          `json:"type" db:"type"`
	Version       int             `json:"version" db:"version"`
	Actor         identity.UserID `json:"actor_id" db:"actor_id"`
	Client        string          `json:"client" db:"client"`
	Subject       string          `json:"subject" db:"subject"`
	Summary       string          `json:"summary" db:"summary"`
	Payload       json.RawMessage `json:"payload" db:"payload"`
	CorrelationID string          `json:"correlation_id" db:"correlation_id"`
	CausationID   uuid.NullUUID   `json:"causation_id" db:"causation_id"`
	OccurredAt    time.Time       `json:"occurred_at" db:"occurred_at"`
	RecordedAt    time.Time       `json:"recorded_at" db:"recorded_at"`
	PrevHash      string          `json:"prev_hash" db:"prev_hash"`
	Hash          string          `json:"hash" db:"hash"`
}

// Head is the tip of an organization's chain (zero before the first entry).
type Head struct {
	Seq  int64  `db:"seq"`
	Hash string `db:"hash"`
}

// FromEvent builds the entry that follows head. Timestamps are truncated to
// the database's microsecond precision so the hash verifies after a round trip.
func FromEvent(e event.Event, head Head, now time.Time) (Entry, error) {
	en := Entry{
		ID: e.ID, Organization: e.Organization, Seq: head.Seq + 1, Type: e.Type, Version: e.Version,
		Actor: e.Actor, Client: e.Client, Subject: e.Subject, Summary: e.Summary, Payload: e.Payload,
		CorrelationID: e.CorrelationID, CausationID: uuid.NullUUID{UUID: e.CausationID, Valid: e.CausationID != uuid.Nil},
		OccurredAt: e.OccurredAt.UTC().Truncate(time.Microsecond), RecordedAt: now.UTC().Truncate(time.Microsecond),
		PrevHash: head.Hash,
	}
	var err error
	en.Hash, err = en.ComputeHash()
	return en, err
}

// ComputeHash is SHA-256 over the previous hash and every recorded field.
func (e Entry) ComputeHash() (string, error) {
	causation := ""
	if e.CausationID.Valid {
		causation = e.CausationID.UUID.String()
	}
	canonical, err := json.Marshal([]any{
		e.PrevHash, e.ID.String(), e.Organization.String(), e.Seq, e.Type, e.Version, e.Actor.String(),
		e.Client, e.Subject, e.Summary, e.Payload, e.CorrelationID, causation,
		e.OccurredAt.UTC().Format(time.RFC3339Nano), e.RecordedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil { // a payload that is not valid JSON: never chain it
		return "", errx.Wrap(err, "hash audit entry", errx.TypeInternal)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Filter narrows a listing; zero fields match everything.
type Filter struct {
	// Subject is one entity ("project/<id>").
	Subject string
	// Types are exact event types ("project.created").
	Types         []string
	Actor         identity.UserID
	CorrelationID string
	// From and Before bound OccurredAt: [From, Before).
	From, Before time.Time
}

func (f Filter) Validate() error {
	for _, t := range f.Types {
		if !event.ValidType(t) {
			return errx.InvalidField("type", "type must look like project.created")
		}
	}
	if len(f.Subject) > 200 {
		return errx.InvalidField("subject", "subject is too long")
	}
	if len(f.CorrelationID) > 64 {
		return errx.InvalidField("correlation_id", "correlation_id is too long")
	}
	return nil
}

// Verification is the result of re-hashing an organization's chain.
type Verification struct {
	Valid   bool  `json:"valid"`
	Entries int64 `json:"entries"`
	// BrokenAt is the first entry whose hash or link does not match (nil when valid).
	BrokenAt *int64 `json:"broken_at"`
}
