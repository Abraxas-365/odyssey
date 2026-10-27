package project

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "project_id" }

// ID identifies a project.
type ID = identity.ID[tag]

func NewID() ID                      { return identity.NewID[tag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[tag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[tag](raw) }

type environmentTag struct{}

func (environmentTag) IDName() string { return "environment_id" }

// EnvironmentID identifies one environment of a project.
type EnvironmentID = identity.ID[environmentTag]

func NewEnvironmentID() EnvironmentID { return identity.NewID[environmentTag]() }
func ParseEnvironmentID(raw string) (EnvironmentID, error) {
	return identity.ParseID[environmentTag](raw)
}
func MustParseEnvironmentID(raw string) EnvironmentID {
	return identity.MustParseID[environmentTag](raw)
}
