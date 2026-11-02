package source

import "github.com/Abraxas-365/odyssey/internal/identity"

type (
	installationTag struct{}
	connectionTag   struct{}
	pushTag         struct{}
)

func (installationTag) IDName() string { return "installation_id" }
func (connectionTag) IDName() string   { return "connection_id" }
func (pushTag) IDName() string         { return "push_id" }

// InstallationID identifies a linked installation of the GitHub App.
type InstallationID = identity.ID[installationTag]

func NewInstallationID() InstallationID { return identity.NewID[installationTag]() }
func ParseInstallationID(raw string) (InstallationID, error) {
	return identity.ParseID[installationTag](raw)
}
func MustParseInstallationID(raw string) InstallationID {
	return identity.MustParseID[installationTag](raw)
}

// ConnectionID identifies a project's connected repository.
type ConnectionID = identity.ID[connectionTag]

func NewConnectionID() ConnectionID { return identity.NewID[connectionTag]() }
func ParseConnectionID(raw string) (ConnectionID, error) {
	return identity.ParseID[connectionTag](raw)
}
func MustParseConnectionID(raw string) ConnectionID { return identity.MustParseID[connectionTag](raw) }

// PushID identifies one commit to publish.
type PushID = identity.ID[pushTag]

func NewPushID() PushID                      { return identity.NewID[pushTag]() }
func ParsePushID(raw string) (PushID, error) { return identity.ParseID[pushTag](raw) }
func MustParsePushID(raw string) PushID      { return identity.MustParseID[pushTag](raw) }
