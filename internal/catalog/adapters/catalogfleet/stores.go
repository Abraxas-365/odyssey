// Package catalogfleet is the catalog.Fleet adapter on the fleet domain: the
// secret stores the organization's clusters announce, read through fleet's
// Queries port only (never fleetsvc). It is the only way catalog reaches the
// fleet.
package catalogfleet

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
)

type Fleet struct{ fleet fleet.Queries }

func New(f fleet.Queries) *Fleet { return &Fleet{fleet: f} }

var _ catalog.Fleet = (*Fleet)(nil)

// SecretStores are the stores the organization's active clusters last
// announced, by name, each with its clusters by name. A revoked or pending
// cluster reads nothing.
func (f *Fleet) SecretStores(ctx context.Context, org identity.OrganizationID) ([]catalog.Store, error) {
	byName := map[string][]catalog.StoreCluster{}
	page := query.Pagination{Limit: query.MaxLimit}
	for {
		got, err := f.fleet.List(ctx, org, fleet.Filter{Status: []fleet.Status{fleet.StatusActive}}, page)
		if err != nil {
			return nil, err
		}
		for _, c := range got.Items {
			for _, s := range c.Agent.Capabilities.SecretStores {
				byName[s] = append(byName[s], catalog.StoreCluster{ID: c.ID.String(), Name: c.Name})
			}
		}
		page.Offset += len(got.Items)
		if len(got.Items) == 0 || page.Offset >= got.Page.Total {
			break
		}
	}
	out := make([]catalog.Store, 0, len(byName))
	for name, clusters := range byName {
		slices.SortFunc(clusters, func(a, b catalog.StoreCluster) int {
			return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
		})
		out = append(out, catalog.Store{Name: name, Clusters: slices.CompactFunc(clusters, func(a, b catalog.StoreCluster) bool { return a.ID == b.ID })})
	}
	slices.SortFunc(out, func(a, b catalog.Store) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
