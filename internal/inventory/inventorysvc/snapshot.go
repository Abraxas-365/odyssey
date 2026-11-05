package inventorysvc

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// app is one app with its environments and the batch facts about them.
type app struct {
	project project.Project
	envs    []env
}

// env is one environment with what the batch reads know of it (no uses).
type env struct {
	environment project.Environment
	serving     *release.Serving
	scan        *build.Scan // nil: none recorded
	cluster     *inventory.ClusterRef
	// down are the sources that could not be read for it.
	down []string
}

// listProjects pages through the apps that match, at most inventory.MaxApps;
// truncated says there were more.
func (s *Service) listProjects(ctx context.Context, org identity.OrganizationID, filter project.Filter, search string, max int) (projects []project.Project, truncated bool, err error) {
	for offset := 0; ; {
		page, err := s.projects.List(ctx, org, filter, query.Pagination{Limit: query.MaxLimit, Offset: offset, Search: search})
		if err != nil {
			return nil, false, err
		}
		projects = append(projects, page.Items...)
		offset += len(page.Items)
		if len(projects) > max {
			return projects[:max], true, nil
		}
		if len(page.Items) == 0 || offset >= page.Page.Total || offset >= query.MaxOffset {
			return projects, false, nil
		}
	}
}

// load reads, in one query per kind of data (never one per app), the
// environments of the apps, what each serves, the scans of the images
// online and, with placement, the cluster each runs on. The environments and
// what is online are core (their failure fails the read); the scans and the
// clusters degrade: the rows carry what could not be read.
func (s *Service) load(ctx context.Context, org identity.OrganizationID, projects []project.Project, placement bool) ([]app, error) {
	if len(projects) == 0 {
		return nil, nil
	}
	ids := make([]project.ID, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	var environments []project.Environment
	if err := chunks(ids, project.MaxBatch, func(part []project.ID) error {
		list, err := s.projects.EnvironmentsOf(ctx, org, part)
		environments = append(environments, list...)
		return err
	}); err != nil {
		return nil, err
	}
	envIDs := make([]project.EnvironmentID, len(environments))
	for i, e := range environments {
		envIDs[i] = e.ID
	}
	serving := map[project.EnvironmentID]release.Serving{}
	if err := chunks(envIDs, release.MaxServing, func(part []project.EnvironmentID) error {
		list, err := s.releases.Serving(ctx, org, part)
		for _, sv := range list {
			serving[sv.Environment] = sv
		}
		return err
	}); err != nil {
		return nil, err
	}
	scans, scansDown := s.scans(ctx, org, serving)
	var (
		placed       map[project.EnvironmentID]fleet.ClusterID
		clusters     map[fleet.ClusterID]fleet.Cluster
		clustersDown bool
	)
	if placement {
		placed, clusters, clustersDown = s.placements(ctx, org, envIDs)
	}

	byProject := map[project.ID][]env{}
	for _, e := range environments {
		x := env{environment: e}
		if sv, ok := serving[e.ID]; ok {
			x.serving = &sv
			if sv.Build != nil {
				if sc, ok := scans[*sv.Build]; ok {
					x.scan = &sc
				}
				if scansDown {
					x.down = append(x.down, "scan")
				}
			}
		}
		if placement {
			if clustersDown {
				x.down = append(x.down, "cluster")
			} else if id, ok := placed[e.ID]; ok {
				ref := inventory.ClusterRef{ID: id, Name: clusters[id].Name}
				x.cluster = &ref
			}
		}
		byProject[e.Project] = append(byProject[e.Project], x)
	}
	out := make([]app, len(projects))
	for i, p := range projects {
		out[i] = app{project: p, envs: byProject[p.ID]}
	}
	return out, nil
}

func (s *Service) scans(ctx context.Context, org identity.OrganizationID, serving map[project.EnvironmentID]release.Serving) (map[build.ID]build.Scan, bool) {
	out := map[build.ID]build.Scan{}
	var ids []build.ID
	seen := map[build.ID]bool{}
	for _, sv := range serving {
		if sv.Build != nil && sv.Image != "" && !seen[*sv.Build] {
			seen[*sv.Build] = true
			ids = append(ids, *sv.Build)
		}
	}
	if err := chunks(ids, build.MaxScans, func(part []build.ID) error {
		list, err := s.builds.Scans(ctx, org, part)
		for _, bs := range list {
			out[bs.Build] = bs.Scan
		}
		return err
	}); err != nil {
		slog.Warn("inventory: scans are unavailable", "error", err)
		return out, true
	}
	return out, false
}

func (s *Service) placements(ctx context.Context, org identity.OrganizationID, envs []project.EnvironmentID) (map[project.EnvironmentID]fleet.ClusterID, map[fleet.ClusterID]fleet.Cluster, bool) {
	placed := map[project.EnvironmentID]fleet.ClusterID{}
	if err := chunks(envs, fleet.MaxPlacements, func(part []project.EnvironmentID) error {
		m, err := s.fleet.Placements(ctx, org, part)
		for k, v := range m {
			placed[k] = v
		}
		return err
	}); err != nil {
		slog.Warn("inventory: placements are unavailable", "error", err)
		return nil, nil, true
	}
	list, err := s.clusters(ctx, org)
	if err != nil {
		slog.Warn("inventory: clusters are unavailable", "error", err)
		return nil, nil, true
	}
	byID := make(map[fleet.ClusterID]fleet.Cluster, len(list))
	for _, c := range list {
		byID[c.ID] = c
	}
	return placed, byID, false
}

// clusters reads every cluster of the organization (a handful: they are
// connections to customer infrastructure), page by page.
func (s *Service) clusters(ctx context.Context, org identity.OrganizationID) ([]fleet.Cluster, error) {
	var out []fleet.Cluster
	for offset := 0; ; {
		page, err := s.fleet.List(ctx, org, fleet.Filter{}, query.Pagination{Limit: query.MaxLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Page.Total || offset >= query.MaxOffset {
			return out, nil
		}
	}
}

// usesOf reads the uses of every environment, inventory.UsesWorkers at a
// time. An environment whose uses cannot be read maps to nil (and the caller
// says so): one app that fails does not fail the rest.
func (s *Service) usesOf(ctx context.Context, org identity.OrganizationID, envs []project.Environment) []*release.Uses {
	out := make([]*release.Uses, len(envs))
	sem := make(chan struct{}, inventory.UsesWorkers)
	var wg sync.WaitGroup
	for i := range envs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			u, err := s.releases.Uses(ctx, org, envs[i].Project, envs[i].ID)
			if err != nil {
				slog.Warn("inventory: uses are unavailable", "environment", envs[i].ID, "error", err)
				return
			}
			out[i] = &u
		}()
	}
	wg.Wait()
	return out
}

// chunks calls f over consecutive parts of at most size items.
func chunks[T any](items []T, size int, f func([]T) error) error {
	for len(items) > 0 {
		n := min(size, len(items))
		if err := f(items[:n]); err != nil {
			return err
		}
		items = items[n:]
	}
	return nil
}
