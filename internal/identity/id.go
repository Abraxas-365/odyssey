// Package identity owns typed entity IDs. Each entity kind has its own Go
// type (OrganizationID, aisystem.ID, ...), so mixing them is a compile error.
package identity

import (
	"database/sql/driver"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/google/uuid"
)

// ID is a validated UUID identity. The zero value is invalid. The phantom
// type parameter discriminates entity kinds at compile time at no runtime cost.
type ID[T any] struct{ v uuid.UUID }

// ParseID parses a raw string into a typed ID.
func ParseID[T any](raw string) (ID[T], error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ID[T]{}, errx.Validation(tagName[T]() + " must be a valid UUID")
	}
	return ID[T]{v: parsed}, nil
}

// MustParseID is like ParseID but panics. Use only in tests and static init.
func MustParseID[T any](raw string) ID[T] {
	id, err := ParseID[T](raw)
	if err != nil {
		panic(err)
	}
	return id
}

// NewID generates a new random UUID for the given entity kind.
func NewID[T any]() ID[T] { return ID[T]{v: uuid.New()} }

// FromUUID types a raw UUID (e.g. ref.Ref.ID in a ref resolver).
func FromUUID[T any](u uuid.UUID) ID[T] { return ID[T]{v: u} }

func (id ID[T]) String() string  { return id.v.String() }
func (id ID[T]) IsZero() bool    { return id.v == uuid.Nil }
func (id ID[T]) UUID() uuid.UUID { return id.v }

// MarshalText implements encoding.TextMarshaler.
func (id ID[T]) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return []byte(""), nil
	}
	return []byte(id.v.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ID[T]) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		id.v = uuid.Nil
		return nil
	}
	parsed, err := uuid.Parse(string(b))
	if err != nil {
		return errx.Validation(tagName[T]() + " must be a valid UUID")
	}
	id.v = parsed
	return nil
}

// Value implements database/sql/driver.Valuer.
func (id ID[T]) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil
	}
	return id.v.String(), nil
}

// Scan implements database/sql.Scanner.
func (id *ID[T]) Scan(src any) error {
	if src == nil {
		id.v = uuid.Nil
		return nil
	}
	switch v := src.(type) {
	case string:
		parsed, err := uuid.Parse(v)
		if err != nil {
			return errx.Validation(tagName[T]() + " must be a valid UUID")
		}
		id.v = parsed
	case []byte:
		parsed, err := uuid.ParseBytes(v)
		if err != nil {
			return errx.Validation(tagName[T]() + " must be a valid UUID")
		}
		id.v = parsed
	default:
		return errx.Internal("cannot scan into " + tagName[T]())
	}
	return nil
}

// Tag names an entity kind for error messages ("ai_system_id must be a valid
// UUID"). Modules declare their own tags next to their entities:
//
//	type tag struct{}
//	func (tag) IDName() string { return "ai_system_id" }
//	type ID = identity.ID[tag]
type Tag interface{ IDName() string }

func tagName[T any]() string {
	var zero T
	if t, ok := any(zero).(Tag); ok {
		return t.IDName()
	}
	return "id"
}

type (
	organizationTag struct{}
	userTag         struct{}
)

func (organizationTag) IDName() string { return "organization_id" }
func (userTag) IDName() string         { return "user_id" }

// OrganizationID is the tenant: the IAMKit organization of the customer
// company. Every module scopes its data by it.
type OrganizationID = ID[organizationTag]

func NewOrganizationID() OrganizationID { return NewID[organizationTag]() }
func ParseOrganizationID(raw string) (OrganizationID, error) {
	return ParseID[organizationTag](raw)
}
func MustParseOrganizationID(raw string) OrganizationID { return MustParseID[organizationTag](raw) }

// UserID is an IAMKit user (the token subject). IAMKit generates it; Odyssey
// only parses it.
type UserID = ID[userTag]

func ParseUserID(raw string) (UserID, error) { return ParseID[userTag](raw) }
func MustParseUserID(raw string) UserID      { return MustParseID[userTag](raw) }
