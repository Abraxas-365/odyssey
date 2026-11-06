// Package inventorysvc implements inventory.Queries by composing the read
// ports of the domains (inventory.Projects, Releases, Builds, Fleet,
// Approvals). It writes nothing and owns no data.
package inventorysvc

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Service is inventory.Queries (and Export, which the HTTP adapter needs for
// the CSV).
type Service struct {
	projects  inventory.Projects
	releases  inventory.Releases
	builds    inventory.Builds
	fleet     inventory.Fleet
	approvals inventory.Approvals
	now       func() time.Time
}

func New(projects inventory.Projects, releases inventory.Releases, builds inventory.Builds, fleet inventory.Fleet, approvals inventory.Approvals) *Service {
	return &Service{projects: projects, releases: releases, builds: builds, fleet: fleet, approvals: approvals, now: time.Now}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

var _ inventory.Queries = (*Service)(nil)

// cand is one environment of one app that passed the cheap filters.
type cand struct {
	app app
	env env
}

// candidates are the environments of the apps that match the filter, in app
// name order (the apps' own order) and, within an app, production first.
// unavailable names the sources that could not be read and that the filter
// depends on: with ?cluster= and the clusters unreadable nothing can be said
// to run on that cluster, and an empty answer must not pass as "none does".
func (s *Service) candidates(ctx context.Context, org identity.OrganizationID, f inventory.Filter) (out []cand, truncated bool, unavailable []inventory.Source, err error) {
	if err := f.Validate(); err != nil {
		return nil, false, nil, err
	}
	projects, truncated, err := s.listProjects(ctx, org, project.Filter{Tier: f.Tier}, strings.TrimSpace(f.Search), inventory.MaxApps)
	if err != nil {
		return nil, false, nil, err
	}
	apps, err := s.load(ctx, org, projects, loadOptions{placement: true, scans: true})
	if err != nil {
		return nil, false, nil, err
	}
	for _, a := range apps {
		for _, e := range a.envs {
			if !f.Cluster.IsZero() {
				if slices.Contains(e.down, "cluster") {
					if !slices.Contains(unavailable, inventory.SourceClusters) {
						unavailable = append(unavailable, inventory.SourceClusters)
					}
					continue
				}
				if e.cluster == nil || e.cluster.ID != f.Cluster {
					continue
				}
			}
			out = append(out, cand{app: a, env: e})
		}
	}
	return out, truncated, unavailable, nil
}

// rows reads the uses of the candidates (a few at a time) and assembles
// their rows.
func (s *Service) rows(ctx context.Context, org identity.OrganizationID, cs []cand) []inventory.Row {
	envs := make([]project.Environment, len(cs))
	for i, c := range cs {
		envs[i] = c.env.environment
	}
	uses := s.usesOf(ctx, org, envs)
	out := make([]inventory.Row, len(cs))
	for i, c := range cs {
		out[i] = row(c.app, c.env, uses[i])
	}
	return out
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, f inventory.Filter, page query.Pagination) (inventory.Page, error) {
	if err := f.Validate(); err != nil {
		return inventory.Page{}, err
	}
	page = page.Normalize()
	cs, truncated, unavailable, err := s.candidates(ctx, org, f)
	if err != nil {
		return inventory.Page{}, err
	}
	out := inventory.Page{Items: []inventory.Row{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}, Truncated: truncated,
		Unavailable: orNone(unavailable)}
	if f.NeedsAttention {
		// Which environments need someone is only known by reading each one's
		// uses: all the candidates', a few at a time (MaxApps bounds it).
		all := slices.DeleteFunc(s.rows(ctx, org, cs), func(r inventory.Row) bool { return !r.NeedsAttention })
		out.Page.Total = len(all)
		out.Items = append(out.Items, window(all, page)...)
		return out, nil
	}
	out.Page.Total = len(cs)
	out.Items = append(out.Items, s.rows(ctx, org, window(cs, page))...)
	return out, nil
}

// Export is every row of the inventory (for the CSV), at most MaxApps apps.
// Only Items, Truncated and Unavailable of the Page are filled.
func (s *Service) Export(ctx context.Context, org identity.OrganizationID, f inventory.Filter) (inventory.Page, error) {
	if err := f.Validate(); err != nil {
		return inventory.Page{}, err
	}
	cs, truncated, unavailable, err := s.candidates(ctx, org, f)
	if err != nil {
		return inventory.Page{}, err
	}
	rows := s.rows(ctx, org, cs)
	if f.NeedsAttention {
		rows = slices.DeleteFunc(rows, func(r inventory.Row) bool { return !r.NeedsAttention })
	}
	return inventory.Page{Items: rows, Truncated: truncated, Unavailable: orNone(unavailable)}, nil
}

// orNone is the list for JSON: [] and never null.
func orNone(s []inventory.Source) []inventory.Source {
	if s == nil {
		return []inventory.Source{}
	}
	return s
}

func window[T any](items []T, p query.Pagination) []T {
	if p.Offset >= len(items) {
		return nil
	}
	return items[p.Offset:min(p.Offset+p.Limit, len(items))]
}

// NeedingAttention judges the apps from what comes in batches (the last
// deployment of an environment failed, or its image online has critical
// vulnerabilities): it never reads an app's uses, so list_apps stays cheap at
// any page size. What the uses list for people is in Attention (get_attention)
// and in get_app.
//
// It is scoped to the actor on ctx exactly like Attention: whoever holds
// inventory:read (TI) is told about every app; anyone else only about the apps
// they created. An app the actor may not be told about is ABSENT from the map
// (the flag is left out, never a false that would claim it is fine), so
// list_apps does not leak a colleague's failed deployment or vulnerabilities
// to a builder that /attention would not show them.
func (s *Service) NeedingAttention(ctx context.Context, org identity.OrganizationID, ids []project.ID) (map[project.ID]bool, error) {
	out := map[project.ID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > inventory.MaxApps {
		ids = ids[:inventory.MaxApps]
	}
	projects, err := s.projects.FindMany(ctx, org, ids)
	if err != nil {
		return nil, err
	}
	if a := actor.From(ctx); !a.Can(permission.InventoryRead) {
		projects = slices.DeleteFunc(projects, func(p project.Project) bool { return a.User.IsZero() || p.CreatedBy != a.User })
	}
	apps, err := s.load(ctx, org, projects, loadOptions{scans: true})
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		out[a.project.ID] = false
		for _, e := range a.envs {
			if quickAttention(e.serving, scanOf(e.serving, e.scan)) {
				out[a.project.ID] = true
			}
		}
	}
	return out, nil
}

// States reads the apps' production environments in batches (load: one query
// per kind of data) and says how each stands.
func (s *Service) States(ctx context.Context, org identity.OrganizationID, ids []project.ID) (inventory.States, error) {
	out := inventory.States{Items: []inventory.AppState{}}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > inventory.MaxStates {
		return out, errx.InvalidField("id", "at most "+strconv.Itoa(inventory.MaxStates)+" apps in one read")
	}
	projects, err := s.projects.FindMany(ctx, org, ids)
	if err != nil {
		return out, err
	}
	// what a visitor finds needs no scans: only the cluster it runs on
	apps, err := s.load(ctx, org, projects, loadOptions{placement: true})
	if err != nil {
		return out, err
	}
	byID := make(map[project.ID]app, len(apps))
	for _, a := range apps {
		byID[a.project.ID] = a
	}
	seen := map[project.ID]bool{}
	for _, id := range ids {
		a, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out.Items = append(out.Items, stateOf(a))
	}
	return out, nil
}

// stateOf is an app's state in its production environment (the only one the
// console publishes to today).
func stateOf(a app) inventory.AppState {
	st := inventory.AppState{App: a.project.ID, Status: inventory.AppNotDeployed, Unavailable: []string{}}
	i := slices.IndexFunc(a.envs, func(e env) bool { return e.environment.Kind == project.KindProduction })
	if a.project.Archived() {
		st.Status = inventory.AppArchived
		return st
	}
	if i < 0 {
		return st
	}
	e := a.envs[i]
	st.Cluster = e.cluster
	if slices.Contains(e.down, "cluster") {
		st.Unavailable = append(st.Unavailable, "cluster")
	}
	st.Status = appStatusOf(e.serving)
	if sv := e.serving; sv != nil && sv.Image != "" {
		st.URL, st.Since, st.Access = sv.URL, sv.Since, sv.Access
	}
	if sv := e.serving; sv != nil && sv.Last != nil {
		l := sv.Last
		st.LastChange = &inventory.AppChange{At: l.CreatedAt, Reason: l.Reason, RequestedBy: l.RequestedBy,
			RequestedByName: l.RequestedByName, RequestedVia: l.RequestedVia}
	}
	return st
}

// appStatusOf says what a visitor finds, the way the app's own page does: a
// publication under way (or waiting for TI) first, then what is online.
func appStatusOf(sv *release.Serving) inventory.AppStatus {
	switch {
	case sv == nil:
		return inventory.AppNotDeployed
	case sv.Last != nil && sv.Last.Status == release.StatusWaitingApproval:
		return inventory.AppWaitingApproval
	case sv.Last != nil && !sv.Last.Status.Finished():
		return inventory.AppDeploying
	case sv.Image != "":
		return inventory.AppOnline
	case sv.Last != nil && sv.Last.Status == release.StatusFailed:
		return inventory.AppFailed
	}
	return inventory.AppNotDeployed
}

// ---- derived facts ----

func statusOf(sv *release.Serving) inventory.Status {
	switch {
	case sv == nil || sv.Last == nil:
		return inventory.StatusNotDeployed
	case !sv.Last.Status.Finished():
		return inventory.StatusDeploying
	case sv.Last.Status == release.StatusFailed:
		return inventory.StatusFailed
	case sv.Image == "":
		return inventory.StatusNotDeployed
	}
	return inventory.StatusOnline
}

func scanOf(sv *release.Serving, sc *build.Scan) inventory.Scan {
	switch {
	case sv == nil || sv.Image == "" || sv.Build == nil:
		return inventory.Scan{State: inventory.ScanNone}
	case sc == nil:
		return inventory.Scan{State: inventory.ScanPending}
	}
	at := sc.ScannedAt
	out := inventory.Scan{Critical: sc.Critical, High: sc.High, ScannedAt: &at}
	switch {
	case !sc.Known():
		out.State, out.Critical, out.High, out.Error = inventory.ScanUnknown, 0, 0, sc.Error
	case sc.Critical > 0 || sc.High > 0:
		out.State = inventory.ScanVulnerable
	default:
		out.State = inventory.ScanClean
	}
	return out
}

// quickAttention is the attention the batch reads alone can tell: the last
// deployment failed, or the image online has critical vulnerabilities.
func quickAttention(sv *release.Serving, sc inventory.Scan) bool {
	failed := sv != nil && sv.Last != nil && sv.Last.Status == release.StatusFailed
	return failed || sc.State == inventory.ScanVulnerable && sc.Critical > 0
}

// row assembles one environment's row; u is nil when its uses could not be read.
func row(a app, e env, u *release.Uses) inventory.Row {
	sc := scanOf(e.serving, e.scan)
	r := inventory.Row{
		App:         inventory.AppRef{ID: a.project.ID, Slug: a.project.Slug, Name: a.project.Name, Tier: a.project.Tier},
		Environment: inventory.EnvironmentRef{ID: e.environment.ID, Slug: e.environment.Slug, Kind: e.environment.Kind},
		Owner:       a.project.CreatedBy,
		Status:      statusOf(e.serving),
		Resources:   []inventory.ResourceRef{},
		Secrets:     inventory.SecretsSummary{Missing: []string{}},
		Scan:        sc,
		Cluster:     e.cluster,
		Attention:   []inventory.AppAttention{},
		Unavailable: append([]string{}, e.down...),
	}
	if e.serving != nil {
		r.URL = e.serving.URL
		if e.serving.Image != "" {
			r.Online = &inventory.Online{Image: e.serving.Image, Since: e.serving.Since}
		}
	}
	needs := quickAttention(e.serving, sc)
	if u == nil {
		r.Unavailable = append(r.Unavailable, "uses")
	} else {
		r.Access = u.Access.Mode
		for _, x := range u.Resources {
			r.Resources = append(r.Resources, inventory.ResourceRef{Name: x.Name, Kind: string(x.Kind), Status: string(x.Status)})
		}
		r.AI = u.AI != nil && u.AI.Enabled
		if u.Secrets != nil {
			r.Secrets = inventory.SecretsSummary{Total: u.Secrets.Total, Missing: append([]string{}, u.Secrets.Missing...)}
		}
		for _, x := range u.Attention {
			r.Attention = append(r.Attention, inventory.AppAttention{Kind: string(x.Kind), Message: x.Message, Who: string(x.Who)})
		}
		for _, sec := range u.Unavailable {
			r.Unavailable = append(r.Unavailable, string(sec))
		}
		needs = needs || u.NeedsPeople()
	}
	r.NeedsAttention = needs
	return r
}
