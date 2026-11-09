package catalogfleet_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/catalogfleet"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

// clusters is a fleet.Queries holding one organization's clusters, paged two
// at a time whatever the limit asked (so paging is exercised).
type clusters struct {
	fleet.Queries
	org   identity.OrganizationID
	all   []fleet.Cluster
	calls int
}

func (c *clusters) List(_ context.Context, org identity.OrganizationID, f fleet.Filter, p query.Pagination) (query.Paginated[fleet.Cluster], error) {
	c.calls++
	var match []fleet.Cluster
	for _, x := range c.all {
		if org == c.org && (len(f.Status) == 0 || slices.Contains(f.Status, x.Status)) {
			match = append(match, x)
		}
	}
	end := min(p.Offset+2, len(match))
	if p.Offset > end {
		end = p.Offset
	}
	return query.Paginated[fleet.Cluster]{Items: match[min(p.Offset, len(match)):end], Page: query.Page{Total: len(match), Limit: 2, Offset: p.Offset}}, nil
}

func cluster(name string, status fleet.Status, stores ...string) fleet.Cluster {
	c := fleet.Cluster{ID: fleet.NewClusterID(), Name: name, Status: status}
	c.Agent.Capabilities.SecretStores = stores
	return c
}

// Stores are grouped by name with the active clusters announcing them, in
// order; a revoked or pending cluster, or another organization's, adds none.
func TestSecretStores(t *testing.T) {
	org := identity.NewOrganizationID()
	lima, quito, cusco := cluster("lima", fleet.StatusActive, "vault", "aws"), cluster("quito", fleet.StatusActive, "vault"),
		cluster("cusco", fleet.StatusActive)
	fake := &clusters{org: org, all: []fleet.Cluster{quito, cluster("viejo", fleet.StatusRevoked, "gcp"), lima,
		cluster("nuevo", fleet.StatusPending, "azure"), cusco}}
	got, err := catalogfleet.New(fake).SecretStores(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	want := []catalog.Store{
		{Name: "aws", Clusters: []catalog.StoreCluster{{ID: lima.ID.String(), Name: "lima"}}},
		{Name: "vault", Clusters: []catalog.StoreCluster{{ID: lima.ID.String(), Name: "lima"}, {ID: quito.ID.String(), Name: "quito"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stores:\n got %+v\nwant %+v", got, want)
	}
	if fake.calls != 2 {
		t.Fatalf("pages read: %d", fake.calls)
	}
	if other, err := catalogfleet.New(fake).SecretStores(context.Background(), identity.NewOrganizationID()); err != nil || other == nil || len(other) != 0 {
		t.Fatalf("another organization: %#v %v", other, err)
	}
}
