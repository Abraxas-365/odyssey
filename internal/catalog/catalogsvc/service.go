// Package catalogsvc implements catalog.Commands and catalog.Queries over
// the offerings repository and the recipe versions in the binary.
package catalogsvc

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

type Service struct {
	repo   catalog.Repository
	tx     txn.Runner
	events event.Publisher
	// directory reads the organization's teams (nil: offerings cannot name
	// teams, and one that does is nobody's).
	directory catalog.Directory

	// seeded remembers the organizations whose factory offerings this
	// process already ensured (a restart forgets it: seeding is idempotent).
	seeded sync.Map
}

func New(repo catalog.Repository, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, tx: tx, events: events}
}

// UseDirectory lets offerings name teams (IAMKit groups), read from d.
func (s *Service) UseDirectory(d catalog.Directory) { s.directory = d }

var (
	_ catalog.Commands = (*Service)(nil)
	_ catalog.Queries  = (*Service)(nil)
)

// ensureFactory inserts the factory offerings the organization lacks, once
// per process and organization. It writes no event: the factory catalog is
// the product, not something a person did.
func (s *Service) ensureFactory(ctx context.Context, org identity.OrganizationID) error {
	if _, ok := s.seeded.Load(org); ok {
		return nil
	}
	now := time.Now().UTC()
	var offerings []catalog.Offering
	for _, v := range catalog.Latest() {
		first, _ := catalog.Version(v.Recipe, 1)
		offerings = append(offerings, catalog.Offering{Organization: org, Recipe: v.Recipe,
			OfferingDraft: first.Factory.Normalize(), Revision: 1, CreatedAt: now, UpdatedAt: now})
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		_, err := s.repo.SeedFactory(ctx, offerings)
		return err
	})
	if err != nil {
		return err
	}
	if _, joined := txn.Tx(ctx); !joined {
		s.seeded.Store(org, true)
	}
	return nil
}

func (s *Service) offerings(ctx context.Context, org identity.OrganizationID) (map[string]catalog.Offering, error) {
	if err := s.ensureFactory(ctx, org); err != nil {
		return nil, err
	}
	list, err := s.repo.List(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make(map[string]catalog.Offering, len(list))
	for _, o := range list {
		out[o.Recipe] = o
	}
	return out, nil
}

// entry is recipe v as offered (the offered version when the binary knows
// it, else v).
func entry(v catalog.RecipeVersion, o catalog.Offering) catalog.Entry {
	if offered, ok := catalog.Version(v.Recipe, o.Version); ok {
		v = offered
	}
	return catalog.Entry{RecipeVersion: v, Versions: catalog.VersionsOf(v.Recipe), Offering: o}
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID) ([]catalog.Entry, error) {
	offs, err := s.offerings(ctx, org)
	if err != nil {
		return nil, err
	}
	var out []catalog.Entry
	for _, v := range catalog.Latest() {
		if _, known := resourcekind.Lookup(v.Kind); !known {
			continue
		}
		o, ok := offs[v.Recipe]
		if !ok {
			o = catalog.Offering{Organization: org, Recipe: v.Recipe, OfferingDraft: catalog.OfferingDraft{Version: v.Version}.Normalize()}
		}
		out = append(out, entry(v, o))
	}
	return out, nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, recipe string) (catalog.Entry, error) {
	entries, err := s.List(ctx, org)
	if err != nil {
		return catalog.Entry{}, err
	}
	i := slices.IndexFunc(entries, func(e catalog.Entry) bool { return e.Recipe == recipe })
	if i < 0 {
		return catalog.Entry{}, catalog.NotFound(recipe)
	}
	return entries[i], nil
}

// teamsOf are the asker's teams when entries need them (an active offering
// limited to teams): nil otherwise, or without a directory (such offerings
// are then nobody's). A directory that fails is an error, not "no teams": a
// team's default would silently become everyone's.
func (s *Service) teamsOf(ctx context.Context, org identity.OrganizationID, user identity.UserID, entries []catalog.Entry) ([]string, error) {
	if s.directory == nil || user.IsZero() || !catalog.NeedsTeams(entries) {
		return nil, nil
	}
	return s.directory.TeamsOf(ctx, org, user)
}

func (s *Service) TeamsOf(ctx context.Context, org identity.OrganizationID, user identity.UserID) ([]string, error) {
	entries, err := s.List(ctx, org)
	if err != nil {
		return nil, err
	}
	return s.teamsOf(ctx, org, user, entries)
}

func (s *Service) Teams(ctx context.Context, org identity.OrganizationID) (catalog.Teams, error) {
	if s.directory == nil {
		return catalog.Teams{Items: []catalog.Team{}}, nil
	}
	teams, err := s.directory.Teams(ctx, org)
	if err != nil {
		return catalog.Teams{}, err
	}
	if teams == nil {
		teams = []catalog.Team{}
	}
	return catalog.Teams{Available: true, Items: teams}, nil
}

func (s *Service) Available(ctx context.Context, org identity.OrganizationID, tier project.Tier, caps []string, asker identity.UserID) ([]catalog.Entry, error) {
	entries, err := s.List(ctx, org)
	if err != nil {
		return nil, err
	}
	teams, err := s.teamsOf(ctx, org, asker, entries)
	if err != nil {
		return nil, err
	}
	var out []catalog.Entry
	for _, k := range resourcekind.Kinds {
		offered := offeredTo(entries, k, tier, teams)
		def, hasDefault := catalog.DefaultAmong(offered, tier)
		var kind []catalog.Entry
		for _, e := range offered {
			if e.Usable(tier, caps, teams) == nil {
				kind = append(kind, e)
			}
		}
		slices.SortStableFunc(kind, func(a, b catalog.Entry) int {
			return boolRank(hasDefault && b.Recipe == def.Recipe) - boolRank(hasDefault && a.Recipe == def.Recipe)
		})
		out = append(out, kind...)
	}
	return out, nil
}

// offeredTo are the entries of kind k offered in tier to a member of teams.
func offeredTo(entries []catalog.Entry, k resourcekind.Kind, tier project.Tier, teams []string) []catalog.Entry {
	var out []catalog.Entry
	for _, e := range entries {
		if e.Kind == k && e.Offering.OffersTo(tier, teams) {
			out = append(out, e)
		}
	}
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) Resolve(ctx context.Context, org identity.OrganizationID, req catalog.Request) (catalog.Resolved, error) {
	if !req.Kind.Valid() {
		return catalog.Resolved{}, errx.InvalidField("kind", "kind must be "+resourcekind.KindsLabel())
	}
	entries, err := s.List(ctx, org)
	if err != nil {
		return catalog.Resolved{}, err
	}
	teams, err := s.teamsOf(ctx, org, req.Asker, entries)
	if err != nil {
		return catalog.Resolved{}, err
	}
	var e catalog.Entry
	if req.Recipe != "" {
		i := slices.IndexFunc(entries, func(e catalog.Entry) bool { return e.Recipe == req.Recipe })
		if i < 0 {
			return catalog.Resolved{}, errx.InvalidField("recipe", "no recipe "+req.Recipe)
		}
		e = entries[i]
		if e.Kind != req.Kind {
			return catalog.Resolved{}, errx.InvalidField("recipe", e.Recipe+" prepares "+string(e.Kind)+", not "+string(req.Kind))
		}
		if !e.Offering.Offers(req.Tier) {
			return catalog.Resolved{}, catalog.Unavailable(req.Kind, req.Tier)
		}
		if !e.Offering.For(teams) {
			return catalog.Resolved{}, catalog.NotForTeams(e.RecipeVersion)
		}
	} else {
		offered := offeredTo(entries, req.Kind, req.Tier, teams)
		def, ok := catalog.DefaultAmong(offered, req.Tier)
		switch {
		case ok:
			e = def
		case len(offered) == 1:
			e = offered[0]
		case len(offered) == 0:
			return catalog.Resolved{}, catalog.Unavailable(req.Kind, req.Tier)
		default:
			names := make([]string, len(offered))
			for i, c := range offered {
				names[i] = c.Recipe
			}
			return catalog.Resolved{}, errx.Business("Hay varias recetas de "+resourcekind.Of(req.Kind).Noun+" para este nivel: elige una.").
				WithCode(catalog.CodeChooseRecipe).WithDetail("recipes", names)
		}
	}
	if req.Version != 0 && req.Version != e.Version {
		return catalog.Resolved{}, catalog.RecipeChanged(e.Recipe + "@" + strconv.Itoa(req.Version))
	}
	if missing := resourcespec.Missing(e.Requires, req.Capabilities); len(missing) > 0 {
		return catalog.Resolved{}, catalog.ClusterCannot(e.RecipeVersion, missing)
	}
	params, outside, err := catalog.Check(e.RecipeVersion, e.Offering, req.Params)
	if err != nil {
		return catalog.Resolved{}, err
	}
	return catalog.Resolved{Kind: e.Kind, Recipe: e.Recipe, Version: e.Version, Engine: e.Engine, Params: params,
		Spec: e.Spec, OutOfRange: outside}, nil
}

func (s *Service) ResolveChange(ctx context.Context, org identity.OrganizationID, ch catalog.Change) (catalog.Resolved, error) {
	v, ok := catalog.Version(ch.Recipe, ch.Version)
	if !ok {
		return catalog.Resolved{}, catalog.RecipeChanged(ch.Recipe + "@" + strconv.Itoa(ch.Version))
	}
	offs, err := s.offerings(ctx, org)
	if err != nil {
		return catalog.Resolved{}, err
	}
	// The offering's limits still apply to a resource of an older version
	// (they are about the parameter, not the version); an offering that is
	// gone or inactive does not stop changing what already exists.
	o := offs[ch.Recipe]
	for name := range ch.Params {
		if _, ok := v.Param(name); !ok {
			return catalog.Resolved{}, errx.InvalidField("params", v.Ref()+" has no parameter "+name)
		}
	}
	// Only what changes is judged (limits, pre-approved range, mutability):
	// a value the resource already has stays, even if IT narrowed the
	// offering since, and a parameter it lacks takes its default without
	// that counting as a change.
	params := make(map[string]int, len(v.Params))
	var outside []string
	for _, p := range v.Params {
		cur, has := ch.Current[p.Name]
		want, asked := ch.Params[p.Name]
		switch {
		case !asked || (has && want == cur):
			if has {
				params[p.Name] = cur
			} else {
				params[p.Name] = o.DefaultOf(p)
			}
			continue
		case has && p.Mutability == catalog.Immutable:
			return catalog.Resolved{}, errx.InvalidField("params."+p.Name, p.Label+" no se puede cambiar después de crear el recurso")
		case has && want < cur && p.Mutability == catalog.Grows:
			return catalog.Resolved{}, errx.InvalidField("params."+p.Name, p.Label+" solo crece: no puede bajar de "+strconv.Itoa(cur))
		}
		if b := o.Bounds(p); want < b.Min || want > b.Max {
			return catalog.Resolved{}, catalog.OutOfLimits(p, b)
		}
		if pre := o.PreapprovedRange(p); want < pre.Min || want > pre.Max {
			outside = append(outside, p.Name)
		}
		params[p.Name] = want
	}
	slices.Sort(outside)
	return catalog.Resolved{Kind: v.Kind, Recipe: v.Recipe, Version: v.Version, Engine: v.Engine, Params: params,
		Spec: v.Spec, OutOfRange: outside}, nil
}

func (s *Service) UpdateOffering(ctx context.Context, org identity.OrganizationID, recipe string, input catalog.UpdateOffering) (catalog.Entry, error) {
	if input.UpdatedBy.IsZero() {
		return catalog.Entry{}, errx.Validation("editor is required")
	}
	if !slices.ContainsFunc(catalog.Latest(), func(v catalog.RecipeVersion) bool { return v.Recipe == recipe }) {
		return catalog.Entry{}, catalog.NotFound(recipe)
	}
	v, ok := catalog.Version(recipe, input.Version)
	if !ok {
		return catalog.Entry{}, errx.InvalidField("version", recipe+" has no version "+strconv.Itoa(input.Version))
	}
	draft := input.OfferingDraft.Normalize()
	if err := draft.Validate(v); err != nil {
		return catalog.Entry{}, err
	}
	if err := s.ensureFactory(ctx, org); err != nil {
		return catalog.Entry{}, err
	}
	if err := s.teamsExist(ctx, org, recipe, draft.Teams); err != nil {
		return catalog.Entry{}, err
	}
	var saved catalog.Offering
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCatalog(ctx, org); err != nil {
			return err
		}
		all, err := s.repo.List(ctx, org)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(all, func(o catalog.Offering) bool { return o.Recipe == recipe })
		if i < 0 {
			return catalog.NotFound(recipe)
		}
		cur := all[i]
		next := cur
		next.OfferingDraft = draft
		if !draft.Active {
			// an inactive offering is nobody's default
			next.Defaults = []project.Tier{}
		}
		if err := defaultsFree(all, next, v.Kind); err != nil {
			return err
		}
		next.UpdatedBy, next.UpdatedAt, next.Revision = input.UpdatedBy, time.Now().UTC(), cur.Revision+1
		if err := s.repo.Update(ctx, next); err != nil {
			return err
		}
		saved = next
		return s.events.Publish(ctx, catalog.NewOfferingUpdated(org, input.UpdatedBy, v.Title, next))
	})
	if err != nil {
		return catalog.Entry{}, err
	}
	return entry(v, saved), nil
}

// teamsExist checks the teams the edit adds are groups of the organization.
// A team already saved and since deleted in IAMKit is kept (it matches
// nobody), so IT is never stuck editing an offering over it; the console
// shows it as gone. Naming new teams without a directory is
// CATALOG_TEAMS_UNAVAILABLE. Read outside the transaction: IAMKit is not
// waited for while the catalog is locked.
func (s *Service) teamsExist(ctx context.Context, org identity.OrganizationID, recipe string, teams []string) error {
	if len(teams) == 0 {
		return nil
	}
	cur, err := s.repo.Find(ctx, org, recipe)
	if err != nil {
		return err
	}
	added := slices.DeleteFunc(slices.Clone(teams), func(t string) bool { return slices.Contains(cur.Teams, t) })
	if len(added) == 0 {
		return nil
	}
	if s.directory == nil {
		return catalog.TeamsUnavailable()
	}
	known, err := s.directory.Teams(ctx, org)
	if err != nil {
		return err
	}
	for _, t := range added {
		if !slices.ContainsFunc(known, func(k catalog.Team) bool { return k.ID == t }) {
			return errx.InvalidField("teams", "no team "+t+" in the organization")
		}
	}
	return nil
}

// defaultsFree refuses a default tier another offering of the kind holds
// for the same people (both for everyone, or teams in common): the most
// specific default must be unique, so a tie is an error on save. A team's
// default over everyone's is not a tie.
func defaultsFree(all []catalog.Offering, next catalog.Offering, kind resourcekind.Kind) error {
	for _, o := range all {
		if o.Recipe == next.Recipe || !o.Active {
			continue
		}
		v, ok := catalog.Version(o.Recipe, o.Version)
		if !ok || v.Kind != kind {
			continue
		}
		for _, t := range next.Defaults {
			if catalog.Ties(o, next, t) {
				return errx.Conflict("«"+v.Title+"» ya es la receta por defecto en nivel "+catalog.TierLabel(t)+" para estas personas: quítasela primero.").
					WithCode(catalog.CodeDefaultTaken).WithDetail("recipe", o.Recipe).WithDetail("tier", string(t))
			}
		}
	}
	return nil
}
