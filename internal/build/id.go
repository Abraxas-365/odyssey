package build

import "github.com/Abraxas-365/odyssey/internal/identity"

type (
	buildTag  struct{}
	sourceTag struct{}
)

func (buildTag) IDName() string  { return "build_id" }
func (sourceTag) IDName() string { return "source_id" }

// ID identifies one build: a source turned into an image.
type ID = identity.ID[buildTag]

func NewID() ID                      { return identity.NewID[buildTag]() }
func ParseID(raw string) (ID, error) { return identity.ParseID[buildTag](raw) }
func MustParseID(raw string) ID      { return identity.MustParseID[buildTag](raw) }

// SourceID identifies an uploaded source archive.
type SourceID = identity.ID[sourceTag]

func NewSourceID() SourceID                      { return identity.NewID[sourceTag]() }
func ParseSourceID(raw string) (SourceID, error) { return identity.ParseID[sourceTag](raw) }
func MustParseSourceID(raw string) SourceID      { return identity.MustParseID[sourceTag](raw) }
