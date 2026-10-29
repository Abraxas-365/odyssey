package fleet

import "github.com/Abraxas-365/odyssey/internal/identity"

type tag struct{}

func (tag) IDName() string { return "cluster_id" }

// ClusterID identifies an enrolled (or enrolling) cluster.
type ClusterID = identity.ID[tag]

func NewClusterID() ClusterID                      { return identity.NewID[tag]() }
func ParseClusterID(raw string) (ClusterID, error) { return identity.ParseID[tag](raw) }
func MustParseClusterID(raw string) ClusterID      { return identity.MustParseID[tag](raw) }

type buildTag struct{}

func (buildTag) IDName() string { return "build_id" }

// BuildID identifies a build sent to a cluster. It is the build domain's
// own id (its buildfleet adapter converts it): the fleet carries builds to
// clusters, it does not decide them.
type BuildID = identity.ID[buildTag]

func ParseBuildID(raw string) (BuildID, error) { return identity.ParseID[buildTag](raw) }
func MustParseBuildID(raw string) BuildID      { return identity.MustParseID[buildTag](raw) }
