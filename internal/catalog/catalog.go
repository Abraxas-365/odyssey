// Package catalog is what an organization offers to be created as a
// resource (docs/catalog.md): which recipe fulfils each kind, in which
// tiers, within which limits, and which part of that range is created
// without asking IT.
//
// Four pieces, four owners:
//   - a kind (pkg/resourcekind) is Odicea's contract: what the app gets;
//   - a RecipeVersion is immutable (its engine, parameters and settings).
//     Factory versions ship in the binary; an update adds versions, never
//     changes one;
//   - an Offering is the organization's, mutable: which version is offered,
//     in which tiers, its hard limits, its pre-approved range, and whether it
//     is the kind's default in a tier. Changing it is not retroactive;
//   - a resource is pinned to recipe@version with normalized parameters.
//
// The catalog is the floor IT's rules cannot lower: outside the hard limits
// a request is refused (422), outside the pre-approved range it needs IT's
// approval whatever the rules say (OutOfRange). Rules only tighten.
package catalog

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// Error codes.
const (
	// CodeUnavailable: nothing fulfils the kind here (no active offering in
	// the tier, or the cluster cannot honour it). Business 422.
	CodeUnavailable = "CATALOG_UNAVAILABLE"
	// CodeOutOfLimits: a parameter is outside the offering's hard limits.
	// Business 422: it cannot be asked for, not even with approval.
	CodeOutOfLimits = "CATALOG_OUT_OF_LIMITS"
	// CodeChooseRecipe: the kind has several offerings in the tier and none
	// is the default. Business 422 with the choices.
	CodeChooseRecipe = "CATALOG_CHOOSE_RECIPE"
	// CodeDefaultTaken: another offering of the kind is already the default
	// in a tier. Conflict 409.
	CodeDefaultTaken = "CATALOG_DEFAULT_TAKEN"
	// CodeRecipeChanged: the pinned recipe@version is no longer what is
	// offered (an approval granted for it cannot run something else).
	CodeRecipeChanged = "CATALOG_RECIPE_CHANGED"
	// CodeTeamsUnavailable: an offering names teams but Odicea cannot read
	// the organization's groups (no IAMKit directory configured). Business 422.
	CodeTeamsUnavailable = "CATALOG_TEAMS_UNAVAILABLE"
)

// MaxTeams is how many teams one offering may name.
const MaxTeams = 50

// Mutability is what may happen to a parameter after the resource exists.
type Mutability string

const (
	// Immutable: fixed at creation.
	Immutable Mutability = "immutable"
	// Grows: only goes up (a volume's size).
	Grows Mutability = "grows"
	// Mutable: changes either way.
	Mutable Mutability = "mutable"
)

// Param is one parameter of a recipe version: an integer in [Min, Max]. The
// schema's bounds are the widest any offering may allow.
type Param struct {
	Name       string     `json:"name"`
	Label      string     `json:"label"`
	Unit       string     `json:"unit,omitempty"`
	Min        int        `json:"min"`
	Max        int        `json:"max"`
	Default    int        `json:"default"`
	Mutability Mutability `json:"mutability"`
}

// ParamStorage is the size parameter of the built-in recipes: it is the
// resource's StorageGi.
const ParamStorage = "storage_gi"

// RecipeVersion is one immutable version of a recipe.
type RecipeVersion struct {
	Recipe      string            `json:"recipe"`
	Version     int               `json:"version"`
	Kind        resourcekind.Kind `json:"kind"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Engine      string            `json:"engine"`
	Params      []Param           `json:"params"`
	// Spec is the engine settings the version fixes (the zero spec: the
	// basic resource every agent prepares).
	Spec resourcespec.Spec `json:"spec"`
	// Requires are the cluster capabilities the version needs.
	Requires []string `json:"requires"`
	// Factory is the offering an organization starts with.
	Factory OfferingDraft `json:"-"`
}

// Ref is recipe@version.
func (v RecipeVersion) Ref() string { return v.Recipe + "@" + strconv.Itoa(v.Version) }

// Param is the version's parameter called name.
func (v RecipeVersion) Param(name string) (Param, bool) {
	i := slices.IndexFunc(v.Params, func(p Param) bool { return p.Name == name })
	if i < 0 {
		return Param{}, false
	}
	return v.Params[i], true
}

// Range is an inclusive integer range; a zero bound is "no narrower than
// the schema" (Min) or "up to the schema" (Max).
type Range struct {
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
}

// OfferingDraft is what IT edits of an offering.
type OfferingDraft struct {
	// Version is the recipe version offered (a version of the recipe).
	Version int  `json:"version"`
	Active  bool `json:"active"`
	// Tiers it is offered in.
	Tiers []project.Tier `json:"tiers"`
	// Defaults are the tiers (of Tiers) where it is its kind's default.
	Defaults []project.Tier `json:"defaults"`
	// Limits narrow a parameter's schema bounds: outside them it cannot be
	// asked for.
	Limits map[string]Range `json:"limits"`
	// Preapproved is the part of the limits created without asking IT
	// (absent parameter: all of its limits).
	Preapproved map[string]Range `json:"preapproved"`
	// Teams are the IAMKit groups whose members may create it (empty: everyone
	// in the organization). They narrow who creates, never what exists.
	Teams []string `json:"teams"`
}

// Offering is an organization's offer of a recipe.
type Offering struct {
	Organization identity.OrganizationID `json:"-"`
	Recipe       string                  `json:"recipe"`
	OfferingDraft
	// Revision moves up with each edit.
	Revision  int             `json:"revision"`
	UpdatedBy identity.UserID `json:"updated_by"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Offers says whether the offering is active in tier.
func (o Offering) Offers(tier project.Tier) bool { return o.Active && slices.Contains(o.Tiers, tier) }

// DefaultIn says whether the offering is its kind's default in tier.
func (o Offering) DefaultIn(tier project.Tier) bool {
	return o.Offers(tier) && slices.Contains(o.Defaults, tier)
}

// ForTeams says whether the offering is limited to teams.
func (o Offering) ForTeams() bool { return len(o.Teams) > 0 }

// For says whether a member of teams may create it: an offering for
// everyone, or one of whose teams they are in.
func (o Offering) For(teams []string) bool {
	return !o.ForTeams() || slices.ContainsFunc(o.Teams, func(t string) bool { return slices.Contains(teams, t) })
}

// OffersTo says whether the offering is active in tier for a member of teams.
func (o Offering) OffersTo(tier project.Tier, teams []string) bool {
	return o.Offers(tier) && o.For(teams)
}

// sameAudience says whether a and b, defaults in the same tier, would tie:
// both for everyone, or teams in common. A team's default over everyone's is
// not a tie: the more specific one wins.
func sameAudience(a, b Offering) bool {
	if !a.ForTeams() || !b.ForTeams() {
		return a.ForTeams() == b.ForTeams()
	}
	return slices.ContainsFunc(a.Teams, func(t string) bool { return slices.Contains(b.Teams, t) })
}

// Ties says whether a and b, of the same kind, are both the default in tier
// for the same people: saving the second is CodeDefaultTaken.
func Ties(a, b Offering, tier project.Tier) bool {
	return a.DefaultIn(tier) && b.DefaultIn(tier) && sameAudience(a, b)
}

// DefaultAmong is the kind's default among offered (entries of one kind
// offered in tier to the asker): a team's default wins over everyone's. ok
// is false when there is none, or several of the asker's teams each have
// one (the asker chooses).
func DefaultAmong(offered []Entry, tier project.Tier) (Entry, bool) {
	var teams, all []Entry
	for _, e := range offered {
		switch {
		case !e.Offering.DefaultIn(tier):
		case e.Offering.ForTeams():
			teams = append(teams, e)
		default:
			all = append(all, e)
		}
	}
	switch {
	case len(teams) == 1:
		return teams[0], true
	case len(teams) == 0 && len(all) == 1:
		return all[0], true
	}
	return Entry{}, false
}

// Bounds are the hard limits of param p in the offering.
func (o Offering) Bounds(p Param) Range {
	return narrow(Range{Min: p.Min, Max: p.Max}, o.Limits[p.Name])
}

// Preapproved is the pre-approved range of param p (within Bounds).
func (o Offering) PreapprovedRange(p Param) Range {
	return narrow(o.Bounds(p), o.Preapproved[p.Name])
}

// DefaultOf is p's value when not asked for: the schema's, kept inside the
// offering's limits.
func (o Offering) DefaultOf(p Param) int {
	b := o.Bounds(p)
	return min(max(p.Default, b.Min), b.Max)
}

func narrow(outer, inner Range) Range {
	out := outer
	if inner.Min != 0 && inner.Min > out.Min {
		out.Min = inner.Min
	}
	if inner.Max != 0 && inner.Max < out.Max {
		out.Max = inner.Max
	}
	return out
}

// Validate checks the draft against the recipe's version v.
func (d OfferingDraft) Validate(v RecipeVersion) error {
	if len(d.Tiers) == 0 && d.Active {
		return errx.InvalidField("tiers", "an active offering is offered in at least one tier")
	}
	for _, t := range d.Tiers {
		if !t.Valid() {
			return errx.InvalidField("tiers", project.TierMessage)
		}
	}
	for _, t := range d.Defaults {
		if !slices.Contains(d.Tiers, t) {
			return errx.InvalidField("defaults", "an offering is the default only in tiers it is offered in")
		}
	}
	if len(d.Teams) > MaxTeams {
		return errx.InvalidField("teams", "an offering names at most "+strconv.Itoa(MaxTeams)+" teams")
	}
	for _, t := range d.Teams {
		if t == "" || len(t) > 64 || strings.ContainsAny(t, " /?#%\\") {
			return errx.InvalidField("teams", "a team is the id of an IAMKit group")
		}
	}
	for _, field := range []struct {
		name string
		m    map[string]Range
	}{{"limits", d.Limits}, {"preapproved", d.Preapproved}} {
		for name, r := range field.m {
			p, ok := v.Param(name)
			if !ok {
				return errx.InvalidField(field.name, v.Ref()+" has no parameter "+name)
			}
			if r.Min < 0 || r.Max < 0 || (r.Min != 0 && r.Max != 0 && r.Min > r.Max) ||
				(r.Min != 0 && (r.Min < p.Min || r.Min > p.Max)) || (r.Max != 0 && (r.Max < p.Min || r.Max > p.Max)) {
				return errx.InvalidField(field.name, name+" must stay between "+strconv.Itoa(p.Min)+" and "+strconv.Itoa(p.Max))
			}
		}
	}
	o := Offering{OfferingDraft: d}
	for _, p := range v.Params {
		b := o.Bounds(p)
		if b.Min > b.Max {
			return errx.InvalidField("limits", p.Name+": the limits leave no value")
		}
		if r, ok := d.Preapproved[p.Name]; ok && ((r.Min != 0 && r.Min > b.Max) || (r.Max != 0 && r.Max < b.Min)) {
			return errx.InvalidField("preapproved", p.Name+": the pre-approved range must be inside the limits")
		}
	}
	return nil
}

// Normalize sorts and de-duplicates the tiers (so equal drafts compare and
// store alike).
func (d OfferingDraft) Normalize() OfferingDraft {
	order := func(ts []project.Tier) []project.Tier {
		out := slices.Clone(ts)
		slices.SortFunc(out, func(a, b project.Tier) int { return tierRank(a) - tierRank(b) })
		return slices.Compact(out)
	}
	d.Tiers, d.Defaults = order(d.Tiers), order(d.Defaults)
	d.Teams = slices.Compact(slices.Sorted(slices.Values(d.Teams)))
	if d.Teams == nil {
		d.Teams = []string{}
	}
	if d.Tiers == nil {
		d.Tiers = []project.Tier{}
	}
	if d.Defaults == nil {
		d.Defaults = []project.Tier{}
	}
	if d.Limits == nil {
		d.Limits = map[string]Range{}
	}
	if d.Preapproved == nil {
		d.Preapproved = map[string]Range{}
	}
	return d
}

func tierRank(t project.Tier) int {
	switch t {
	case project.TierSandbox:
		return 0
	case project.TierInternal:
		return 1
	case project.TierProduction:
		return 2
	}
	return 3
}

// UpdateOffering is IT's edit of an offering.
type UpdateOffering struct {
	OfferingDraft
	UpdatedBy identity.UserID `json:"-"`
}

// Entry is a recipe version with the organization's offering of it.
type Entry struct {
	RecipeVersion
	// Versions are every version of the recipe IT can offer, oldest first.
	Versions []int    `json:"versions"`
	Offering Offering `json:"offering"`
}

// Usable says why e cannot be created in tier on a cluster with caps by a
// member of teams (nil: it can): a kind that is not available, a recipe not
// offered in the tier (CodeUnavailable), offered only to other teams
// (NotForTeams), or a cluster that cannot honour it (ClusterCannot).
func (e Entry) Usable(tier project.Tier, caps, teams []string) error {
	if c, known := resourcekind.Lookup(e.Kind); !known || !c.Available || !e.Offering.Offers(tier) {
		return Unavailable(e.Kind, tier)
	}
	if !e.Offering.For(teams) {
		return NotForTeams(e.RecipeVersion)
	}
	if missing := resourcespec.Missing(e.Requires, caps); len(missing) > 0 {
		return ClusterCannot(e.RecipeVersion, missing)
	}
	return nil
}

// Request is what someone asks the catalog for: a kind, maybe a recipe,
// and parameters, in a tier, on a cluster with those capabilities.
type Request struct {
	Kind resourcekind.Kind
	// Recipe ("" : the kind's default in the tier).
	Recipe string
	// Version pins the version (0: the offered one). A pinned version that
	// is not the offered one is CodeRecipeChanged.
	Version int
	Params  map[string]int
	Tier    project.Tier
	// Capabilities the environment's cluster announced.
	Capabilities []string
	// Asker is who creates it (the authenticated user, or who asked for an
	// approved request): an offering for teams is theirs only if they are in
	// one. Zero: nobody's team.
	Asker identity.UserID
}

// Team is an IAMKit group of the organization an offering can be limited to.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Members is how many people it has.
	Members int `json:"members"`
	// Directory says it is synced from the company directory (SCIM), not
	// made by hand in IAMKit.
	Directory bool `json:"directory"`
}

// Teams are the teams IT can limit an offering to.
type Teams struct {
	// Available is false when Odicea cannot read them (no directory).
	Available bool   `json:"available"`
	Items     []Team `json:"items"`
}

// NeedsTeams says whether any active offering among entries is limited to
// teams: only then are the asker's teams worth reading.
func NeedsTeams(entries []Entry) bool {
	return slices.ContainsFunc(entries, func(e Entry) bool { return e.Offering.Active && e.Offering.ForTeams() })
}

// Resolved is a request the catalog accepts: pinned, normalized, and what
// part of it is outside the pre-approved range.
type Resolved struct {
	Kind    resourcekind.Kind `json:"kind"`
	Recipe  string            `json:"recipe"`
	Version int               `json:"recipe_version"`
	Engine  string            `json:"engine"`
	// Params are every parameter of the version, defaults filled in.
	Params map[string]int    `json:"params"`
	Spec   resourcespec.Spec `json:"-"`
	// OutOfRange are the parameters outside the pre-approved range (sorted):
	// IT must approve, whatever the rules say.
	OutOfRange []string `json:"out_of_range"`
}

// NeedsApproval says the catalog alone asks IT.
func (r Resolved) NeedsApproval() bool { return len(r.OutOfRange) > 0 }

// Change is a request to change a resource's parameters, pinned to its
// recipe@version.
type Change struct {
	Recipe  string
	Version int
	Current map[string]int
	// Params are the values asked for (absent: unchanged).
	Params map[string]int
	Tier   project.Tier
}

// Check validates params for version v as offered by o: unknown parameters,
// values outside the hard limits; fills defaults and computes OutOfRange.
func Check(v RecipeVersion, o Offering, params map[string]int) (map[string]int, []string, error) {
	for name := range params {
		if _, ok := v.Param(name); !ok {
			return nil, nil, errx.InvalidField("params", v.Ref()+" has no parameter "+name)
		}
	}
	out := make(map[string]int, len(v.Params))
	var outside []string
	for _, p := range v.Params {
		val, asked := params[p.Name]
		if !asked {
			val = o.DefaultOf(p)
		}
		b := o.Bounds(p)
		if val < b.Min || val > b.Max {
			return nil, nil, OutOfLimits(p, b)
		}
		if pre := o.PreapprovedRange(p); val < pre.Min || val > pre.Max {
			outside = append(outside, p.Name)
		}
		out[p.Name] = val
	}
	slices.Sort(outside)
	return out, outside, nil
}

// OutOfLimits is the 422 for a value outside the hard limits, worded for
// the person (UnitLabel).
func OutOfLimits(p Param, b Range) error {
	unit := ""
	if p.Unit != "" {
		unit = " " + UnitLabel(p.Unit)
	}
	return errx.Business(p.Label + ": tu empresa permite entre " + strconv.Itoa(b.Min) + " y " + strconv.Itoa(b.Max) + unit + ".").
		WithCode(CodeOutOfLimits).WithField("params." + p.Name)
}

// UnitLabel words a parameter's unit as people read it: GiB is "GB" (the
// console says the same).
func UnitLabel(unit string) string {
	if unit == "GiB" {
		return "GB"
	}
	return unit
}

// Unavailable is the 422 for a kind nothing fulfils here.
func Unavailable(k resourcekind.Kind, tier project.Tier) error {
	return errx.Business("Tu empresa no ofrece "+resourcekind.Of(k).The()+" en apps de nivel "+TierLabel(tier)+
		" todavía. Pídeselo a TI.").WithCode(CodeUnavailable).WithDetail("kind", string(k))
}

// NotForTeams is the 422 for a recipe offered only to teams the person is
// not in.
func NotForTeams(v RecipeVersion) error {
	return errx.Business("Tu empresa ofrece «"+v.Title+"» solo a algunos equipos, y no estás en ellos. Pídeselo a TI.").
		WithCode(CodeUnavailable).WithDetail("kind", string(v.Kind)).WithDetail("recipe", v.Recipe)
}

// TeamsUnavailable is the 422 for naming teams when Odicea cannot read the
// organization's groups.
func TeamsUnavailable() error {
	return errx.Business("Odicea no puede leer los equipos de tu empresa: falta conectarla al directorio de IAMKit.").
		WithCode(CodeTeamsUnavailable).WithField("teams")
}

// ClusterCannot is the 422 for a recipe the environment's cluster cannot
// honour. A cluster lacking the kind's built-in engine cannot provision the
// kind at all: that is resourcekind.CodeUnavailable with the contract's own
// words (what to install), as the fleet would say.
func ClusterCannot(v RecipeVersion, missing []string) error {
	if slices.Contains(missing, resourcespec.Builtin(v.Kind)) {
		return errx.Business(resourcekind.Of(v.Kind).Unavailable).WithCode(resourcekind.CodeUnavailable).
			WithDetail("kind", string(v.Kind))
	}
	return errx.Business("El clúster de esta app todavía no puede preparar «"+v.Title+"». Pide a TI que actualice su agente.").
		WithCode(CodeUnavailable).WithDetail("recipe", v.Ref()).WithDetail("missing", strings.Join(missing, ","))
}

// RuleCatalog names the catalog's own reason to ask IT (Floor): it is not a
// rule IT can switch off.
const (
	RuleCatalog     = "catalog:preapproved"
	RuleCatalogName = "Fuera de lo que TI preaprobó en el catálogo"
)

// Floor is the catalog's verdict on what it resolved: outside the
// pre-approved range IT must approve, whatever the rules say (approval,
// with RuleCatalog); inside, it adds nothing (allow). The governed service
// combines it with the rules' verdict (gate.Verdict.With), so a rule may
// only tighten it. It names no approvers: the rules' approvers decide, or
// approval's default (approvals:decide) when only the catalog asks.
func Floor(r Resolved) gate.Verdict {
	if !r.NeedsApproval() {
		return gate.Verdict{Effect: gate.Allow}
	}
	return gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{ID: RuleCatalog, Name: RuleCatalogName}}}
}

// Gate is r as IT's rules see it (gate.Request.Recipe).
func (r Resolved) Gate() *gate.Recipe {
	return &gate.Recipe{Name: r.Recipe, Version: r.Version, Engine: r.Engine, Params: maps.Clone(r.Params),
		OutOfRange: slices.Clone(r.OutOfRange)}
}

// RecipeChanged is the 422 for a pinned version that is no longer offered.
func RecipeChanged(ref string) error {
	return errx.Business("La receta "+ref+" ya no se ofrece así: TI cambió el catálogo desde que se pidió. Pídelo de nuevo.").
		WithCode(CodeRecipeChanged).WithDetail("recipe", ref)
}

// NotFound is the 404 of an unknown recipe.
func NotFound(recipe string) error { return errx.NotFound("no recipe " + recipe) }

// TierLabel words a tier in Spanish.
func TierLabel(t project.Tier) string {
	switch t {
	case project.TierSandbox:
		return "sandbox"
	case project.TierInternal:
		return "interna"
	case project.TierProduction:
		return "producción"
	}
	return string(t)
}

// SameParams says a and b hold the same values.
func SameParams(a, b map[string]int) bool { return maps.Equal(a, b) }
