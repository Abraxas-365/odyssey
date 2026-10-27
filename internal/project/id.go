package project

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "project_id" }

// ID identifies a project.
type ID = identity.ID[tag]

func NewID() ID                      { return identity.NewID[tag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[tag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[tag](raw) }
