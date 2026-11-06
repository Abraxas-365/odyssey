package inventorysvc

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// Attention composes "Necesita tu atención" for the actor on ctx, from each
// domain's own Queries. Every source degrades on its own (its name goes to
// Unavailable and the rest of the list is still answered), like Uses.
//
//   - Holders of approvals:decide see the requests waiting for them.
//   - Holders of inventory:read (TI) see the apps whose image online has
//     critical vulnerabilities, the apps whose last publication failed in the
//     last 24 hours, and the clusters that do not answer or do not apply
//     their network policy.
//   - Holders of integrations:manage see a GitHub that is available and not
//     connected, and its installations that GitHub suspended.
//   - Everyone sees their own apps (the ones they created) with what Uses
//     lists for people, their failed publications and the ones under way,
//     and their own requests to TI that wait, were rejected or could not
//     run.
func (s *Service) Attention(ctx context.Context, org identity.OrganizationID) (inventory.Attention, error) {
	a := actor.From(ctx)
	out := inventory.Attention{Items: []inventory.Item{}, Unavailable: []inventory.Source{}}
	down := func(src inventory.Source, err error) {
		slog.Warn("attention: a source is unavailable", "source", src, "error", err)
		if !slices.Contains(out.Unavailable, src) {
			out.Unavailable = append(out.Unavailable, src)
		}
	}
	add := func(items []inventory.Item, truncated bool) {
		out.Items = append(out.Items, items...)
		out.Truncated = out.Truncated || truncated
	}
	now := s.now()
	canDecide, isIT := a.Can(permission.ApprovalsDecide), a.Can(permission.InventoryRead)

	if canDecide {
		items, truncated, err := s.pendingForMe(ctx, org)
		if err != nil {
			down(inventory.SourceApprovals, err)
		}
		add(items, truncated)
	}
	if isIT {
		items, truncated, err := s.clusterItems(ctx, org)
		if err != nil {
			down(inventory.SourceClusters, err)
		}
		add(items, truncated)
	}
	if a.Can(permission.IntegrationsManage) {
		items, err := s.githubItems(ctx, org)
		if err != nil {
			down(inventory.SourceGitHub, err)
		}
		add(items, false)
	}
	if isIT || !a.User.IsZero() {
		items, truncated, err := s.appItems(ctx, org, a, isIT, now)
		if err != nil {
			down(inventory.SourceApps, err)
		}
		add(items, truncated)
	}
	if !a.User.IsZero() {
		items, truncated, err := s.myRequests(ctx, org, now)
		if err != nil {
			down(inventory.SourceRequests, err)
		}
		add(items, truncated)
	}
	sortItems(out.Items)
	return out, nil
}

// pendingForMe are the requests the actor could decide now.
func (s *Service) pendingForMe(ctx context.Context, org identity.OrganizationID) ([]inventory.Item, bool, error) {
	page, err := s.approvals.List(ctx, org, approval.ViewerOf(ctx),
		approval.Filter{Decidable: true, Status: []approval.Status{approval.StatusPending}}, query.Pagination{Limit: inventory.MaxItems})
	if err != nil {
		return nil, false, err
	}
	items := make([]inventory.Item, 0, len(page.Items))
	for _, v := range page.Items {
		if !v.CanDecide {
			continue
		}
		items = append(items, inventory.Item{Kind: inventory.KindApprovalPending, Scope: inventory.ScopeIT, Severity: inventory.SeverityWarning,
			Sentence: "Espera tu decisión: " + sentence(v.Summary), Link: "/bandeja", Request: v.ID.String(),
			App: ptr(v.Project), Environment: ptr(v.Environment), At: ptr(v.CreatedAt)})
	}
	return items, page.Page.Total > len(page.Items), nil
}

// myRequests are the actor's own requests that wait for TI, that TI rejected
// (for a week) or that were approved and could not run.
func (s *Service) myRequests(ctx context.Context, org identity.OrganizationID, now time.Time) ([]inventory.Item, bool, error) {
	page, err := s.approvals.List(ctx, org, approval.ViewerOf(ctx), approval.Filter{Mine: true,
		Status: []approval.Status{approval.StatusPending, approval.StatusRejected, approval.StatusApproved}},
		query.Pagination{Limit: inventory.MaxItems})
	if err != nil {
		return nil, false, err
	}
	var items []inventory.Item
	for _, v := range page.Items {
		it := inventory.Item{Scope: inventory.ScopeMine, Link: "/bandeja", Request: v.ID.String(),
			App: ptr(v.Project), Environment: ptr(v.Environment), At: ptr(v.CreatedAt)}
		recent := v.DecidedAt != nil && now.Sub(*v.DecidedAt) <= inventory.RejectedWithin
		switch {
		case v.Status == approval.StatusPending:
			it.Kind, it.Severity = inventory.KindRequestPending, inventory.SeverityInfo
			it.Sentence = "Tu solicitud espera la decisión de TI: " + sentence(v.Summary)
		case v.Status == approval.StatusRejected && recent:
			it.Kind, it.Severity, it.At = inventory.KindRequestRejected, inventory.SeverityWarning, v.DecidedAt
			it.Sentence = "TI rechazó tu solicitud: " + sentence(v.Summary)
			if v.DecisionNote != "" {
				it.Sentence += " Motivo: " + sentence(v.DecisionNote)
			}
		case v.Status == approval.StatusApproved && v.ExecutionError != "" && recent:
			it.Kind, it.Severity, it.At = inventory.KindRequestFailed, inventory.SeverityWarning, v.DecidedAt
			it.Sentence = "TI aprobó tu solicitud, pero no se pudo hacer: " + sentence(v.Summary) + " " + sentence(v.ExecutionError)
		default:
			continue
		}
		items = append(items, it)
	}
	return items, page.Page.Total > len(page.Items), nil
}

// appItems are what the apps need: for TI, every app's critical
// vulnerabilities and recent failed publication; for the actor, the things
// their own apps' uses list for people and their failed publications.
func (s *Service) appItems(ctx context.Context, org identity.OrganizationID, a actor.Actor, isIT bool, now time.Time) ([]inventory.Item, bool, error) {
	filter := project.Filter{}
	if !isIT {
		filter.CreatedBy = a.User // a builder reads only their own apps
	}
	projects, truncated, err := s.listProjects(ctx, org, filter, "", inventory.MaxApps)
	if err != nil {
		return nil, false, err
	}
	apps, err := s.load(ctx, org, projects, loadOptions{scans: true})
	if err != nil {
		return nil, false, err
	}
	var items []inventory.Item
	var mine []project.Environment
	byEnv := map[project.EnvironmentID]app{}
	for _, ap := range apps {
		own := !a.User.IsZero() && ap.project.CreatedBy == a.User
		for _, e := range ap.envs {
			sc := scanOf(e.serving, e.scan)
			if isIT && sc.State == inventory.ScanVulnerable && sc.Critical > 0 {
				items = append(items, appItem(ap, e, inventory.KindAppVulnerable, inventory.ScopeIT, inventory.SeverityCritical,
					fmt.Sprintf("%s tiene %s en la versión que está en línea.", label(ap, e), plural(sc.Critical, "vulnerabilidad crítica", "vulnerabilidades críticas")), sc.ScannedAt))
			}
			failed := e.serving != nil && e.serving.FailedWithin(now.Add(-inventory.FailedWithin))
			if failed && isIT {
				items = append(items, failedItem(ap, e, inventory.ScopeIT))
			} else if failed && own {
				items = append(items, failedItem(ap, e, inventory.ScopeMine))
			}
			if own && publishing(e.serving) {
				items = append(items, appItem(ap, e, inventory.KindPublishing, inventory.ScopeMine, inventory.SeverityInfo,
					label(ap, e)+": se está publicando una versión nueva.", ptr(e.serving.Last.CreatedAt)))
			}
			if own {
				mine = append(mine, e.environment)
				byEnv[e.environment.ID] = ap
			}
		}
	}
	// The uses of the actor's own environments: the only per-environment
	// read, bounded to the actor's apps and UsesWorkers at a time.
	uses := s.usesOf(ctx, org, mine)
	for i, e := range mine {
		u := uses[i]
		if u == nil {
			continue
		}
		ap := byEnv[e.ID]
		for _, x := range u.Attention {
			if x.Who == release.WhoWait {
				continue
			}
			it := inventory.Item{Kind: inventory.KindAppAttention, Scope: inventory.ScopeMine, Severity: inventory.SeverityWarning,
				Sentence: label(ap, env{environment: e}) + ": " + sentence(x.Message), Link: "/apps/" + ap.project.ID.String(),
				Detail: string(x.Kind), App: ptr(ap.project.ID), AppSlug: ap.project.Slug, Environment: ptr(e.ID)}
			items = append(items, it)
		}
	}
	return items, truncated, nil
}

func failedItem(ap app, e env, scope inventory.Scope) inventory.Item {
	sentence := label(ap, e) + ": la última publicación falló."
	if f := e.serving.Last.Error; f != nil && f.Message != "" {
		sentence = label(ap, e) + ": la última publicación falló. " + f.Message
	}
	return appItem(ap, e, inventory.KindDeploymentFailed, scope, inventory.SeverityWarning, sentence, e.serving.Last.FinishedAt)
}

func appItem(ap app, e env, kind inventory.Kind, scope inventory.Scope, sev inventory.Severity, sentence string, at *time.Time) inventory.Item {
	return inventory.Item{Kind: kind, Scope: scope, Severity: sev, Sentence: sentence, Link: "/apps/" + ap.project.ID.String(),
		App: ptr(ap.project.ID), AppSlug: ap.project.Slug, Environment: ptr(e.environment.ID), At: at}
}

// publishing: a publication is under way there. One waiting for TI is not:
// it is the requester's request_pending (and TI's approval_pending).
func publishing(sv *release.Serving) bool {
	return sv != nil && sv.Last != nil && !sv.Last.Status.Finished() && sv.Last.Status != release.StatusWaitingApproval
}

// githubItems are, for whoever manages the integrations, a GitHub that is
// available here and that the organization has not connected, and its
// installations that GitHub suspended (their apps stop publishing from git).
func (s *Service) githubItems(ctx context.Context, org identity.OrganizationID) ([]inventory.Item, error) {
	if !s.sources.Info(ctx).Enabled {
		return nil, nil // not offered here: nothing to connect
	}
	list, err := s.sources.ListInstallations(ctx, org)
	if err != nil {
		return nil, err
	}
	const link = "/configuracion/github"
	if len(list) == 0 {
		return []inventory.Item{{Kind: inventory.KindGitHubNotConnected, Scope: inventory.ScopeIT, Severity: inventory.SeverityInfo, Link: link,
			Sentence: "GitHub no está conectado: nadie puede importar un repositorio ni publicar desde sus cambios."}}, nil
	}
	var items []inventory.Item
	for _, i := range list {
		if i.Suspended {
			items = append(items, inventory.Item{Kind: inventory.KindGitHubSuspended, Scope: inventory.ScopeIT, Severity: inventory.SeverityWarning, Link: link,
				Sentence: fmt.Sprintf("GitHub suspendió la conexión con «%s»: sus apps no se publican desde sus cambios.", i.Account)})
		}
	}
	return items, nil
}

// clusterItems are the clusters that do not answer and, among the ones that
// do, those that have not taken their network policy.
func (s *Service) clusterItems(ctx context.Context, org identity.OrganizationID) ([]inventory.Item, bool, error) {
	list, err := s.clusters(ctx, org)
	if err != nil {
		return nil, false, err
	}
	var items []inventory.Item
	var firstErr error
	for _, c := range list {
		if c.Status != fleet.StatusActive {
			continue // pending: not enrolled yet; revoked: gone on purpose
		}
		link := "/infraestructura/clusteres"
		if !c.Connected {
			it := inventory.Item{Kind: inventory.KindClusterDisconnected, Scope: inventory.ScopeIT, Severity: inventory.SeverityCritical, Link: link,
				Sentence: fmt.Sprintf("El clúster «%s» no responde: sus apps siguen corriendo, pero no se pueden cambiar.", c.Name),
				Cluster:  ptr(c.ID), At: c.LastSeenAt}
			items = append(items, it)
			continue
		}
		e, err := s.fleet.Egress(ctx, org, c.ID)
		if err != nil {
			slog.Warn("attention: a cluster's egress policy is unavailable", "cluster", c.ID, "error", err)
			firstErr = err
			continue
		}
		if e.Mode == fleet.EgressRestricted && e.State != fleet.EgressApplied {
			why := "todavía la está aplicando"
			if e.State == fleet.EgressFailed || e.State == fleet.EgressUnsupported {
				why = "no pudo aplicarla"
			}
			items = append(items, inventory.Item{Kind: inventory.KindClusterPolicy, Scope: inventory.ScopeIT, Severity: inventory.SeverityWarning, Link: link,
				Sentence: fmt.Sprintf("El clúster «%s» %s: la política de salida a internet no está en vigor.", c.Name, why),
				Cluster:  ptr(c.ID), At: e.AcknowledgedAt})
		}
	}
	return items, false, firstErr
}

// sortItems puts TI's list first, then by severity, kind, newest first.
func sortItems(items []inventory.Item) {
	rank := func(s inventory.Severity) int {
		switch s {
		case inventory.SeverityCritical:
			return 0
		case inventory.SeverityWarning:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(items, func(a, b inventory.Item) int {
		if a.Scope != b.Scope {
			if a.Scope == inventory.ScopeIT {
				return -1
			}
			return 1
		}
		if ra, rb := rank(a.Severity), rank(b.Severity); ra != rb {
			return ra - rb
		}
		if ka, kb := slices.Index(inventory.Kinds, a.Kind), slices.Index(inventory.Kinds, b.Kind); ka != kb {
			return ka - kb
		}
		switch {
		case a.At != nil && b.At != nil && !a.At.Equal(*b.At):
			return b.At.Compare(*a.At)
		case a.At != nil && b.At == nil:
			return -1
		case a.At == nil && b.At != nil:
			return 1
		}
		return strings.Compare(a.Sentence, b.Sentence)
	})
}

func label(ap app, e env) string {
	if e.environment.Slug == "" || e.environment.Kind == project.KindProduction {
		return ap.project.Name
	}
	return ap.project.Name + " (" + e.environment.Slug + ")"
}

// sentence ends s with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func ptr[T any](v T) *T { return &v }
