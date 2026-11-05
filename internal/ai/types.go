package ai

import (
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Value types of the ports (ports.go holds only interfaces).

// ProviderKey is a key as the gateway reports it when made. Secret is the
// only copy: it is sealed at once and never stored or logged.
type ProviderKey struct {
	ID     string
	Secret string
}

// KeyInfo is a key the gateway lists (never its secret).
type KeyInfo struct {
	ID   string
	Name string
}

// ClusterKey is the cluster's public key a key is sealed to.
type ClusterKey struct {
	Cluster string
	KeyID   string
	Public  []byte
	// App is the cluster's name of the app the secret belongs to.
	App string
}

// Target is what an environment offers for the AI.
type Target struct {
	// Capable: the environment's cluster lists the AI capability.
	Capable bool
	// Key is where a key is sealed to (nil: the cluster takes no secrets).
	Key *ClusterKey
	// NextVersion is the version of the managed secret a key made now takes.
	NextVersion int64
	// Revision is the latest settings revision.
	Revision int64
	// Conflict is why turning the AI on is refused for this app now
	// (VARIABLE_TAKEN), nil when it is not.
	Conflict error
}

// Delivery is what Release.Enable stores and sends.
type Delivery struct {
	Version int64
	KeyID   string // the cluster key it was sealed to
	BaseURL string
	Sealed  []byte
	By      identity.UserID
}

// Applied is the settings revision a change produced.
type Applied struct {
	Revision int64
}
