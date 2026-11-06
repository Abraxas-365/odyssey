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

	"github.com/Abraxas-365/odyssey/internal/channel"
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

	// Read-time, outside the hash: where it came from (channel.Of of
	// Client; "" when not recorded) and who did it, by name ("" for the
	// platform or someone the directory does not know).
	Channel   string `json:"channel" db:"-"`
	ActorName string `json:"actor_name" db:"-"`
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
	// Project is everything about one app: the entries whose payload names it
	// (project_id), whatever their subject.
	Project uuid.UUID
	// Channel is where the actions came from (a channel, channel.Assistant or
	// channel.Unrecorded); the service resolves it into Clients.
	Channel string
	// Clients is the event clients Channel selects: set by the service, read
	// by the repository (nil: any).
	Clients *channel.Match `json:"-"`
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
	if len(f.Channel) > 64 {
		return errx.InvalidField("channel", "channel is too long")
	}
	return nil
}

// Verification is the result of re-hashing an organization's chain.
type Verification struct {
	Valid   bool  `json:"valid"`
	Entries int64 `json:"entries"`
	// BrokenAt is the first entry whose hash or link does not match (nil when valid).
	BrokenAt *int64 `json:"broken_at"`
	// VerifiedAt is when the chain was re-hashed (this answer).
	VerifiedAt time.Time `json:"verified_at"`
}

// SummaryDays is the window of Summary: the last week.
const SummaryDays = 7

// Summary is the organization's last week at a glance.
type Summary struct {
	// From is where the week starts (now minus SummaryDays).
	From time.Time `json:"from"`
	// Events is how many entries were recorded; People how many people acted.
	Events int64 `json:"events"`
	People int64 `json:"people"`
	// Deployments are the publications (deployment.started): how many, and
	// through which channel ("" for the ones whose channel was not recorded).
	Deployments int64          `json:"deployments"`
	ByChannel   []ChannelCount `json:"by_channel"`
}

// ChannelCount is how many publications came through one channel.
type ChannelCount struct {
	Channel string `json:"channel"`
	Count   int64  `json:"count"`
}

// Tally is how many entries of one type came from one event client (what
// the repository counts; the service turns clients into channels).
type Tally struct {
	Type    string `db:"type"`
	Client  string `db:"client"`
	Entries int64  `db:"entries"`
}

// Person is someone who appears in the trail.
type Person struct {
	Actor identity.UserID `json:"actor_id" db:"actor_id"`
	// Name is their name in the organization's directory ("" when it does
	// not know them, or is not configured).
	Name    string    `json:"name" db:"-"`
	Entries int64     `json:"entries" db:"entries"`
	LastAt  time.Time `json:"last_at" db:"last_at"`
}
