package fleet

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "cluster_id" }

// ClusterID identifies an enrolled (or enrolling) cluster.
type ClusterID = identity.ID[tag]

func NewClusterID() ClusterID                      { return identity.NewID[tag]() }
func ParseClusterID(raw string) (ClusterID, error) { return identity.ParseID[tag](raw) }
func MustParseClusterID(raw string) ClusterID      { return identity.MustParseID[tag](raw) }
