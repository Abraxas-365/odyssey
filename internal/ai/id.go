package ai

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "ai_key_id" }

// KeyID identifies a key the platform made for an environment's app.
type KeyID = identity.ID[tag]

func NewKeyID() KeyID                      { return identity.NewID[tag]() }
func ParseKeyID(raw string) (KeyID, error) { return identity.ParseID[tag](raw) }
func MustParseKeyID(raw string) KeyID      { return identity.MustParseID[tag](raw) }
