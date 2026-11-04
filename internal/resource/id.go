package resource

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "resource_id" }

// ID identifies a resource.
type ID = identity.ID[tag]

func NewID() ID                      { return identity.NewID[tag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[tag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[tag](raw) }
