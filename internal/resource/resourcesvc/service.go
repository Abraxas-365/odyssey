// Package resourcesvc implements resource.Commands and resource.Queries.
//
// Locks are taken in one order so changes never deadlock: the environment's
// resource count (Create), then the environment's settings (Create's
// connection, Connect, Disconnect and Delete, through resource.Usage), then
// the resource row. Delete and Connect serialize on the settings lock, which
// is what makes "not in use" stay true while the resource is marked deleting.
package resourcesvc

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

type Service struct {
	repo     resource.Repository
	cluster  resource.Cluster
	usage    resource.Usage
	projects project.Queries
	catalog  resource.Catalog
	tx       txn.Runner
	events   event.Publisher
	// policy is IT's rules (the policyresource adapter); nil refuses every
	// governed action.
	policy resource.Policy
	// approvals holds what a rule wants approved (the approvalresource
	// adapter); nil fails an approval verdict closed.
	approvals resource.Approvals
}

func New(repo resource.Repository, cluster resource.Cluster, usage resource.Usage, projects project.Queries, catalog resource.Catalog,
	tx txn.Runner, events event.Publisher, policy resource.Policy) *Service {
	return &Service{repo: repo, cluster: cluster, usage: usage, projects: projects, catalog: catalog, tx: tx, events: events, policy: policy}
}

// UseApprovals wires where approvals are held (the approvalresource adapter).
func (s *Service) UseApprovals(a resource.Approvals) { s.approvals = a }

var (
	_ resource.Commands = (*Service)(nil)
	_ resource.Queries  = (*Service)(nil)
)

// authorize is the seam where resource asks IT's rules about an action
// judged on its own (deleting): nil, nil lets it go on; a Pending means a
// rule wants IT's approval, the request was stored and nothing may change
// (the caller answers 202); an error is POLICY_DENIED (403). The approved
// action re-runs as the requester with the approval's mark on ctx and
// redeems it here (gate.Settle).
func (s *Service) authorize(ctx context.Context, p project.Project, e project.Environment, request gate.Request, hold gate.Hold) (*gate.Pending, error) {
	v, err := s.judge(ctx, p, e, request)
	if err != nil {
		return nil, err
	}
	return s.settle(ctx, p, e, request.Action, v, hold)
}

// judge is IT's rules' verdict on request combined with the others (the
// catalog's floor, the verdict on connecting): the strongest wins, so a
// rule only tightens the catalog (gate.Verdict.With). It writes nothing.
func (s *Service) judge(ctx context.Context, p project.Project, e project.Environment, request gate.Request, others ...gate.Verdict) (gate.Verdict, error) {
	if s.policy == nil { // fail closed: nothing is allowed by accident
		return gate.Verdict{}, errx.Internal("IT's rules are not wired")
	}
	request.Tier, request.ProjectSlug, request.Environment = string(p.Tier), p.Slug, e.Slug
	v, err := s.policy.Check(ctx, p.Organization, request)
	if err != nil {
		return gate.Verdict{}, err
	}
	return v.With(others...), nil
}

// settle holds (or, re-running approved, redeems) the ONE request of an
// operation judged by judge, under action.
func (s *Service) settle(ctx context.Context, p project.Project, e project.Environment, action string, v gate.Verdict, hold gate.Hold) (*gate.Pending, error) {
	hold.Project, hold.Environment = p.ID.String(), e.ID.String()
	return gate.Settle(ctx, p.Organization, s.approvals, action, v, hold)
}

// place resolves the project and environment of a request.
func (s *Service) place(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Project, project.Environment, error) {
	p, err := s.projects.Find(ctx, org, id)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	e, err := s.projects.FindEnvironment(ctx, org, id, env)
	if err != nil {
		return project.Project{}, project.Environment{}, err
	}
	return p, e, nil
}

func names(p project.Project, e project.Environment) resource.Names {
	return resource.Names{Project: p.Name, Environment: e.Slug}
}

func where(p project.Project, e project.Environment) string {
	return "«" + p.Name + "» (" + e.Slug + ")"
}

// view puts a stored resource together with what the cluster reports and
// whether the app uses it.
func (s *Service) view(ctx context.Context, r resource.Resource, connected []resource.Connection) (resource.View, error) {
	o, err := s.cluster.Observed(ctx, r.Organization, r.Environment, r.Name)
	if err != nil {
		return resource.View{}, err
	}
	var conn *resource.Connection
	for i := range connected {
		if connected[i].Name == r.Name {
			conn = &connected[i]
		}
	}
	return resource.NewView(r, o, conn), nil
}

func (s *Service) viewOf(ctx context.Context, p project.Project, r resource.Resource) (resource.View, error) {
	connected, err := s.usage.Connections(ctx, r.Organization, p.ID, r.Environment)
	if err != nil {
		return resource.View{}, err
	}
	return s.view(ctx, r, connected)
}

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input resource.Create) (resource.Result, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return resource.Result{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	if p.Archived() {
		return resource.Result{}, project.ArchivedError()
	}
	// The catalog decides what fulfils the request on this cluster, in this
	// tier (a refusal here stores nothing). A re-run of an approved request
	// pins the version IT saw: a recipe changed since is refused, never
	// silently upgraded.
	caps, err := s.cluster.Capabilities(ctx, org, e.ID)
	if err != nil {
		return resource.Result{}, err
	}
	resolved, err := s.catalog.Resolve(ctx, org, catalog.Request{Kind: input.Kind, Recipe: input.Recipe, Version: input.RecipeVersion,
		Params: input.Params, Tier: p.Tier, Capabilities: caps})
	if err != nil {
		return resource.Result{}, err
	}
	// The whole operation is judged BEFORE anything is provisioned (the
	// cluster object Apply creates is not undone by a rollback, so a refusal
	// found after it would leave an orphan) and held as ONE request: IT's
	// rules on creating, the catalog's pre-approved range and, when it
	// connects, the rules on connecting. The connection is then made Judged
	// (not asked again). A tier changed in the milliseconds between this
	// check and the connection is ignored on purpose.
	others := []gate.Verdict{catalog.Floor(resolved)}
	step := resource.CreateStep{Name: input.Name, Kind: string(input.Kind), Recipe: resolved.Recipe, RecipeVersion: resolved.Version,
		Params: resolved.Params}
	summary := "Crear " + recipeTitle(resolved) + " «" + input.Name + "» (" + paramsLabel(resolved.Params) + ") en " + where(p, e)
	if input.Connect != nil {
		cv, err := s.usage.JudgeConnect(ctx, org, id, e.ID, resource.Ask{Name: input.Name, Kind: input.Kind, Services: input.Connect.Services})
		if err != nil {
			return resource.Result{}, err
		}
		others = append(others, cv)
		step.Connect = &resource.CreateConnect{Prefix: input.Connect.Prefix, Services: input.Connect.Services}
		summary += " y conectarla" + servicesLabel(input.Connect.Services)
	}
	verdict, err := s.judge(ctx, p, e, gate.Request{Action: gate.ActionCreateResource, ResourceKind: string(input.Kind), Recipe: resolved.Gate()}, others...)
	if err != nil {
		return resource.Result{}, err
	}
	pending, err := s.settle(ctx, p, e, gate.ActionCreateResource, verdict, gate.Hold{Summary: summary,
		Payload: resource.CreateApproval{Op: resource.OpCreate, SchemaVersion: resource.PayloadVersion, Steps: []resource.CreateStep{step}}})
	if err != nil {
		return resource.Result{}, err
	}
	if pending != nil { // held for IT: nothing was created
		return resource.Result{View: resource.View{Outcome: gate.Holding(*pending)}}, nil
	}
	r := resource.Resource{ID: resource.NewID(), Organization: org, Project: id, Environment: e.ID, Name: input.Name, Kind: input.Kind,
		StorageGi: resolved.Params[catalog.ParamStorage], Recipe: resolved.Recipe, RecipeVersion: resolved.Version,
		Params: resolved.Params, Generation: 1, Status: resource.StatusActive, CreatedBy: input.RequestedBy}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockEnvironment(ctx, org, e.ID); err != nil {
			return err
		}
		n, err := s.repo.Count(ctx, org, e.ID)
		if err != nil {
			return err
		}
		if n >= resource.MaxPerEnvironment {
			return resource.ErrLimit()
		}
		if err := s.repo.Create(ctx, r); err != nil {
			return err
		}
		// A cluster that cannot prepare the kind refuses here and the row
		// above is rolled back: nothing waits for a resource that never comes.
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		if err := s.events.Publish(ctx, resource.NewCreated(org, input.RequestedBy, r, names(p, e))); err != nil {
			return err
		}
		if input.Connect == nil {
			return nil
		}
		applied, err = s.usage.Connect(ctx, org, id, e.ID, resource.Link{Name: r.Name, Prefix: input.Connect.Prefix, Services: input.Connect.Services, Judged: true}, input.RequestedBy)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	stored, err := s.repo.Find(ctx, org, e.ID, r.Name)
	if err != nil {
		return resource.Result{}, err
	}
	v, err := s.viewOf(ctx, p, stored)
	if err != nil {
		return resource.Result{}, err
	}
	return resource.Result{View: v, Revision: applied.Revision, Deployment: applied.Deployment}, nil
}

// Change changes a resource's parameters within its recipe@version. The
// catalog checks them against the version (a size only grows) and the
// offering's hard limits; IT's rules and the catalog's pre-approved range
// judge the change as one request (change_resource), before the
// transaction. Values that change nothing are a no-op, never held.
func (s *Service) Change(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, input resource.Change) (resource.View, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return resource.View{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	if p.Archived() {
		return resource.View{}, project.ArchivedError()
	}
	cur, err := s.repo.Find(ctx, org, e.ID, name)
	if err != nil {
		return resource.View{}, err
	}
	if cur.Status == resource.StatusDeleting {
		return resource.View{}, resource.ErrDeleting(name)
	}
	// An approved change re-runs on the version IT saw.
	if input.Recipe != "" && (input.Recipe != cur.Recipe || input.RecipeVersion != cur.RecipeVersion) {
		return resource.View{}, catalog.RecipeChanged(input.Recipe + "@" + strconv.Itoa(input.RecipeVersion))
	}
	resolved, err := s.catalog.ResolveChange(ctx, org, catalog.Change{Recipe: cur.Recipe, Version: cur.RecipeVersion, Current: cur.Params,
		Params: input.Params, Tier: p.Tier})
	if err != nil {
		return resource.View{}, err
	}
	if unchanged(cur.Params, input.Params) {
		// Nothing to change (nor to judge). An approved change that became a
		// no-op is used up by the approval module after it runs.
		return s.viewOf(ctx, p, cur)
	}
	verdict, err := s.judge(ctx, p, e, gate.Request{Action: gate.ActionChangeResource, ResourceKind: string(cur.Kind), Recipe: resolved.Gate()},
		catalog.Floor(resolved))
	if err != nil {
		return resource.View{}, err
	}
	pending, err := s.settle(ctx, p, e, gate.ActionChangeResource, verdict, gate.Hold{
		Summary: "Cambiar " + recipeTitle(resolved) + " «" + name + "» de " + where(p, e) + " a " + paramsLabel(resolved.Params),
		Payload: resource.ChangeApproval{Op: resource.OpChange, SchemaVersion: resource.PayloadVersion, Name: name, Recipe: cur.Recipe,
			RecipeVersion: cur.RecipeVersion, Params: input.Params}})
	if err != nil {
		return resource.View{}, err
	}
	if pending != nil { // held for IT: nothing changed
		return resource.View{Outcome: gate.Holding(*pending)}, nil
	}
	r := cur
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		locked, err := s.repo.FindForUpdate(ctx, org, e.ID, name)
		if err != nil {
			return err
		}
		switch {
		case locked.Status == resource.StatusDeleting:
			return resource.ErrDeleting(name)
		case locked.Generation != cur.Generation: // what was judged is not what is there any more
			return resource.ErrChangedMeanwhile(name)
		}
		r.Params, r.StorageGi, r.Generation = resolved.Params, resolved.Params[catalog.ParamStorage], cur.Generation+1
		if err := s.repo.Change(ctx, org, e.ID, name, r.Params, r.Generation); err != nil {
			return err
		}
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		// Every parameter today is the size: a change is a resize.
		return s.events.Publish(ctx, resource.NewResized(org, input.RequestedBy, r, cur.StorageGi, names(p, e)))
	})
	if err != nil {
		return resource.View{}, err
	}
	return s.viewOf(ctx, p, r)
}

// unchanged reports whether every value asked is what the resource already
// has (a parameter it lacks, filled with its default by the catalog, is not
// a change asked for).
func unchanged(current, asked map[string]int) bool {
	for name, v := range asked {
		if c, ok := current[name]; !ok || c != v {
			return false
		}
	}
	return true
}

// recipeTitle words what a recipe prepares in a summary.
func recipeTitle(r catalog.Resolved) string {
	if v, ok := catalog.Version(r.Recipe, r.Version); ok {
		return v.Title
	}
	return string(r.Kind)
}

// paramsLabel words parameters in a summary, sorted by name.
func paramsLabel(params map[string]int) string {
	parts := make([]string, 0, len(params))
	for _, name := range slices.Sorted(maps.Keys(params)) {
		if name == catalog.ParamStorage {
			parts = append(parts, resourceSize(params[name]))
			continue
		}
		parts = append(parts, name+" "+strconv.Itoa(params[name]))
	}
	return strings.Join(parts, ", ")
}

func resourceSize(gi int) string { return strconv.Itoa(gi) + " GiB" }

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, input resource.Delete) (resource.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	// Judged before anything is locked or marked. The kind is read for the
	// rule's `resource.kind`; an unknown resource is NotFound as ever.
	found, err := s.repo.Find(ctx, org, e.ID, name)
	if err != nil {
		return resource.View{}, err
	}
	pending, err := s.authorize(ctx, p, e, gate.Request{Action: gate.ActionDeleteResource, ResourceKind: string(found.Kind)}, gate.Hold{
		Summary: "Eliminar " + string(found.Kind) + " «" + name + "» de " + where(p, e) + " y todos sus datos",
		Payload: resource.DeleteApproval{Op: resource.OpDelete, Name: name}})
	if err != nil {
		return resource.View{}, err
	}
	if pending != nil { // held for IT: nothing was deleted
		return resource.View{Outcome: gate.Holding(*pending)}, nil
	}
	var r resource.Resource
	var connected []resource.Connection
	// Connecting a resource takes the same settings lock: a connection made
	// meanwhile either is seen here or waits and finds the resource deleting.
	err = s.usage.WithConnectionsLocked(ctx, org, id, e.ID, func(ctx context.Context, conns []resource.Connection) error {
		connected = conns
		cur, err := s.repo.FindForUpdate(ctx, org, e.ID, name)
		if err != nil {
			return err
		}
		r = cur
		if cur.Status == resource.StatusDeleting {
			return nil // already on its way out
		}
		for _, c := range conns {
			if c.Name == name {
				return resource.ErrInUse(name)
			}
		}
		r.Status, r.Generation = resource.StatusDeleting, cur.Generation+1
		if err := s.repo.MarkDeleting(ctx, org, e.ID, name, r.Generation); err != nil {
			return err
		}
		if err := s.cluster.Apply(ctx, resource.DesiredOf(r)); err != nil {
			return err
		}
		return s.events.Publish(ctx, resource.NewDeleted(org, input.RequestedBy, r, names(p, e)))
	})
	if err != nil {
		return resource.View{}, err
	}
	return s.view(ctx, r, connected)
}

func (s *Service) Connect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, input resource.Connect) (resource.Result, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return resource.Result{}, err
	}
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	// IT's rules judge before the transaction (a refusal must not live in a
	// transaction it rolls back); the kind is read here, the transaction
	// re-reads the resource under its lock for the deleting check.
	found, err := s.repo.Find(ctx, org, e.ID, input.Name)
	if err != nil {
		return resource.Result{}, err
	}
	if found.Status != resource.StatusDeleting {
		verdict, err := s.usage.JudgeConnect(ctx, org, id, e.ID, resource.Ask{Name: input.Name, Kind: found.Kind, Services: input.Services})
		if err != nil {
			return resource.Result{}, err
		}
		pending, err := s.settle(ctx, p, e, gate.ActionConnectResource, verdict, gate.Hold{
			Summary: "Conectar " + string(found.Kind) + " «" + input.Name + "» a " + where(p, e) + servicesLabel(input.Services),
			Payload: resource.ConnectApproval{Op: resource.OpConnect, Name: input.Name, Prefix: input.Prefix, Services: input.Services}})
		if err != nil {
			return resource.Result{}, err
		}
		if pending != nil { // held for IT: nothing was connected
			return resource.Result{View: resource.View{Outcome: gate.Holding(*pending)}}, nil
		}
	}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		r, err := s.repo.Find(ctx, org, e.ID, input.Name)
		if err != nil {
			return err
		}
		if r.Status == resource.StatusDeleting {
			return resource.ErrDeleting(input.Name)
		}
		applied, err = s.usage.Connect(ctx, org, id, e.ID, resource.Link{Name: input.Name, Prefix: input.Prefix, Services: input.Services, Judged: true}, input.RequestedBy)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	return s.result(ctx, p, e, input.Name, applied)
}

// servicesLabel words the services asked for in a summary.
func servicesLabel(services *[]string) string {
	if services == nil || len(*services) == 0 {
		return ""
	}
	return " (solo " + strings.Join(*services, ", ") + ")"
}

func (s *Service) Disconnect(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string, by identity.UserID) (resource.Result, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Result{}, err
	}
	var applied resource.Applied
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := s.repo.Find(ctx, org, e.ID, name); err != nil {
			return err
		}
		applied, err = s.usage.Disconnect(ctx, org, id, e.ID, name, by)
		return err
	})
	if err != nil {
		return resource.Result{}, err
	}
	return s.result(ctx, p, e, name, applied)
}

func (s *Service) result(ctx context.Context, p project.Project, e project.Environment, name string, applied resource.Applied) (resource.Result, error) {
	r, err := s.repo.Find(ctx, p.Organization, e.ID, name)
	if err != nil {
		return resource.Result{}, err
	}
	v, err := s.viewOf(ctx, p, r)
	if err != nil {
		return resource.Result{}, err
	}
	return resource.Result{View: v, Revision: applied.Revision, Deployment: applied.Deployment}, nil
}

// Forget drops the row of a resource the cluster erased. Only a deleting
// resource is dropped: a report can never remove one that is in use.
func (s *Service) Forget(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		r, ok, err := s.repo.Forget(ctx, org, env, name)
		if err != nil || !ok {
			return err
		}
		return s.events.Publish(ctx, resource.NewRemoved(org, r))
	})
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (resource.Listing, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Listing{}, err
	}
	rows, err := s.repo.List(ctx, org, e.ID)
	if err != nil {
		return resource.Listing{}, err
	}
	connected, err := s.usage.Connections(ctx, org, p.ID, e.ID)
	if err != nil {
		return resource.Listing{}, err
	}
	out := resource.Listing{Items: make([]resource.View, 0, len(rows))}
	for _, r := range rows {
		v, err := s.view(ctx, r, connected)
		if err != nil {
			return resource.Listing{}, err
		}
		out.Items = append(out.Items, v)
	}
	if out.Kinds, err = s.cluster.Kinds(ctx, org, e.ID); err != nil {
		return resource.Listing{}, err
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, name string) (resource.View, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.View{}, err
	}
	r, err := s.repo.Find(ctx, org, e.ID, name)
	if err != nil {
		return resource.View{}, err
	}
	return s.viewOf(ctx, p, r)
}

func (s *Service) Named(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, names []string) ([]resource.View, error) {
	rows, err := s.repo.List(ctx, org, env)
	if err != nil {
		return nil, err
	}
	out := make([]resource.View, 0, len(names))
	for _, r := range rows {
		if !slices.Contains(names, r.Name) {
			continue
		}
		v, err := s.view(ctx, r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, name string) (resource.Resource, error) {
	return s.repo.Find(ctx, org, env, name)
}

func (s *Service) Stranded(ctx context.Context, org identity.OrganizationID, id project.ID, tier project.Tier) ([]resource.Resource, error) {
	rows, err := s.repo.ListProject(ctx, org, id)
	if err != nil || len(rows) == 0 {
		return []resource.Resource{}, err
	}
	entries, err := s.catalog.List(ctx, org)
	if err != nil {
		return nil, err
	}
	out := []resource.Resource{}
	for _, r := range rows {
		if r.Status == resource.StatusDeleting || !resourcekind.Of(r.Kind).HoldsData {
			continue // nothing to lose: it can be made again in the new tier
		}
		offered := slices.ContainsFunc(entries, func(e catalog.Entry) bool { return e.Recipe == r.Recipe && e.Offering.Offers(tier) })
		if !offered {
			out = append(out, r)
		}
	}
	return out, nil
}
