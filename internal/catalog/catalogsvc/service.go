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

	// seeded remembers the organizations whose factory offerings this
	// process already ensured (a restart forgets it: seeding is idempotent).
	seeded sync.Map
}

func New(repo catalog.Repository, tx txn.Runner, events event.Publisher) *Service {
	return &Service{repo: repo, tx: tx, events: events}
}

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
	return catalog.Entry{RecipeVersion: v, Offering: o}
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

// usable says the entry can be created in tier on a cluster with caps.
func usable(e catalog.Entry, tier project.Tier, caps []string) bool {
	c, known := resourcekind.Lookup(e.Kind)
	return known && c.Available && e.Offering.Offers(tier) && len(resourcespec.Missing(e.Requires, caps)) == 0
}

func (s *Service) Available(ctx context.Context, org identity.OrganizationID, tier project.Tier, caps []string) ([]catalog.Entry, error) {
	entries, err := s.List(ctx, org)
	if err != nil {
		return nil, err
	}
	var out []catalog.Entry
	for _, k := range resourcekind.Kinds {
		var kind []catalog.Entry
		for _, e := range entries {
			if e.Kind == k && usable(e, tier, caps) {
				kind = append(kind, e)
			}
		}
		slices.SortStableFunc(kind, func(a, b catalog.Entry) int {
			return boolRank(b.Offering.DefaultIn(tier)) - boolRank(a.Offering.DefaultIn(tier))
		})
		out = append(out, kind...)
	}
	return out, nil
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
	} else {
		var offered, defaults []catalog.Entry
		for _, c := range entries {
			if c.Kind == req.Kind && c.Offering.Offers(req.Tier) {
				offered = append(offered, c)
				if c.Offering.DefaultIn(req.Tier) {
					defaults = append(defaults, c)
				}
			}
		}
		switch {
		case len(defaults) == 1:
			e = defaults[0]
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

// defaultsFree refuses a default tier another offering of the kind holds:
// the most specific default must be unique, so a tie is an error on save.
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
			if o.DefaultIn(t) {
				return errx.Conflict("«"+v.Title+"» ya es la receta por defecto en nivel "+catalog.TierLabel(t)+": quítasela primero.").
					WithCode(catalog.CodeDefaultTaken).WithDetail("recipe", o.Recipe).WithDetail("tier", string(t))
			}
		}
	}
	return nil
}
