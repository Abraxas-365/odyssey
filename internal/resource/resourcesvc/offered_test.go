package resourcesvc_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogsvc"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogtest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/resource/resourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// announcing is a cluster that announces caps.
type announcing struct {
	resource.Cluster
	caps []string
}

func (c announcing) Capabilities(context.Context, identity.OrganizationID, project.EnvironmentID) ([]string, error) {
	return c.caps, nil
}

// rules asks IT for redis and records what it was asked.
type rules struct{ asked []gate.Request }

func (r *rules) Check(_ context.Context, _ identity.OrganizationID, req gate.Request) (gate.Verdict, error) {
	r.asked = append(r.asked, req)
	if req.ResourceKind == string(resourcekind.Redis) {
		return gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{Name: "cache-review"}}}, nil
	}
	return gate.Verdict{Effect: gate.Allow}, nil
}

// connecting is the app's side: IT's verdict on connecting each kind (allow
// when absent), the kinds it was asked about, and a refusal of the app's
// shape for files (several services: one must be named).
type connecting struct {
	usage
	verdicts map[resourcekind.Kind]gate.Verdict
	asked    []resourcekind.Kind
}

func (c *connecting) JudgeConnect(_ context.Context, _ identity.OrganizationID, _ project.ID, _ project.EnvironmentID, ask resource.Ask) (gate.Verdict, error) {
	c.asked = append(c.asked, ask.Kind)
	if ask.Kind == resourcekind.Files {
		return gate.Verdict{}, errx.InvalidField("services", "un volumen de archivos lo monta un solo servicio: elige cuál")
	}
	if v, ok := c.verdicts[ask.Kind]; ok {
		return v, nil
	}
	return gate.Verdict{Effect: gate.Allow}, nil
}

// holding fails the test if anything is held: Offered writes nothing.
type holding struct{ t *testing.T }

func (h holding) Hold(context.Context, identity.OrganizationID, gate.Hold) (gate.Pending, error) {
	h.t.Fatal("Offered opened an approval request")
	return gate.Pending{}, nil
}

func (h holding) Redeem(context.Context, identity.OrganizationID, string, gate.Grant) error {
	h.t.Fatal("Offered redeemed an approval")
	return nil
}

type offerSetup struct {
	status  project.Status
	connect map[resourcekind.Kind]gate.Verdict
	offer   func(*catalogsvc.Service)
}

func offeredWith(t *testing.T, tier project.Tier, caps []string, s offerSetup) (resource.Offered, *rules, *connecting, error) {
	t.Helper()
	if s.status == "" {
		s.status = project.StatusActive
	}
	p := project.Project{ID: project.NewID(), Organization: org, Slug: "ventas", Status: s.status, Tier: tier}
	e := project.Environment{ID: project.NewEnvironmentID(), Project: p.ID, Organization: org, Slug: "production"}
	events := &eventtest.Recorder{}
	cat := catalogsvc.New(catalogtest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	if s.offer != nil {
		s.offer(cat)
	}
	pol, conn := &rules{}, &connecting{verdicts: s.connect}
	svc := resourcesvc.New(&repo{}, announcing{caps: caps}, conn, projects{p: p, e: e}, cat, txn.Passthrough{}, events, pol)
	svc.UseApprovals(holding{t})
	o, err := svc.Offered(context.Background(), org, p.ID, e.ID)
	if n := len(events.Events()); n != 0 {
		t.Fatalf("Offered published %d events", n)
	}
	return o, pol, conn, err
}

func offered(t *testing.T, tier project.Tier, caps []string) (resource.Offered, *rules) {
	t.Helper()
	o, pol, _, err := offeredWith(t, tier, caps, offerSetup{})
	if err != nil {
		t.Fatal(err)
	}
	return o, pol
}

var allCaps = []string{resourcespec.Builtin(resourcekind.Postgres), resourcespec.Builtin(resourcekind.Redis),
	resourcespec.Builtin(resourcekind.Files), resourcespec.SpecV1, resourcespec.PostgresInstances,
	resourcespec.PostgresVersion, resourcespec.PostgresResources}

func recipes(o resource.Offered) []string {
	var out []string
	for _, r := range o.Recipes {
		out = append(out, r.Recipe)
	}
	return out
}

// A production app on a modern cluster is offered every factory recipe,
// each kind's default first, with its limits, variables and the verdict of
// IT's rules on its defaults (judged as Create judges, writing nothing).
func TestOfferedJudgesEachRecipe(t *testing.T) {
	o, pol := offered(t, project.TierProduction, allCaps)
	want := []string{catalog.PostgresBasic, catalog.PostgresProduction, catalog.RedisBasic, catalog.FilesBasic}
	if got := recipes(o); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("recipes %v, want %v", got, want)
	}
	if o.Tier != project.TierProduction || len(o.Unavailable) != 0 {
		t.Fatalf("offered %+v", o)
	}
	basic, prod, redis := o.Recipes[0], o.Recipes[1], o.Recipes[2]
	if !basic.Default || prod.Default || basic.Verdict != gate.Allow || len(basic.Rules) != 0 {
		t.Fatalf("basic %+v / prod %+v", basic, prod)
	}
	if len(basic.Variables) == 0 || basic.Variables[0].Name != "DATABASE_URL" {
		t.Fatalf("variables %+v", basic.Variables)
	}
	// postgres-produccion: limits min 5, pre-approved up to 50, default 10
	sp := prod.Params[0]
	if sp.Name != catalog.ParamStorage || sp.Min != 5 || sp.PreapprovedMax != 50 || sp.Default != 10 || sp.Mutability != catalog.Grows {
		t.Fatalf("prod storage %+v", sp)
	}
	if prod.Verdict != gate.Allow {
		t.Fatalf("defaults inside the pre-approved range are instant: %+v", prod)
	}
	if redis.Verdict != gate.Approval || len(redis.Rules) != 1 || redis.Rules[0] != "cache-review" {
		t.Fatalf("redis %+v", redis)
	}
	// IT's rules saw the recipe and the tier, as for a real create
	for _, req := range pol.asked {
		if req.Action != gate.ActionCreateResource || req.Recipe == nil || req.Tier != string(project.TierProduction) {
			t.Fatalf("asked %+v", req)
		}
	}
	if len(pol.asked) != 4 {
		t.Fatalf("asked %d times", len(pol.asked))
	}
}

// What the tier is not offered and what the cluster cannot honour is left
// out; a kind with nothing left says why; without a cluster, nothing.
func TestOfferedLeavesOutWhatCannotBeCreated(t *testing.T) {
	// sandbox: no postgres-produccion (production only)
	o, _ := offered(t, project.TierSandbox, allCaps)
	for _, r := range o.Recipes {
		if r.Recipe == catalog.PostgresProduction {
			t.Fatalf("sandbox offered %v", recipes(o))
		}
	}
	// an older agent: only the basics (production needs spec/v1)
	old := []string{resourcespec.Builtin(resourcekind.Postgres), resourcespec.Builtin(resourcekind.Redis)}
	o, _ = offered(t, project.TierProduction, old)
	if got := recipes(o); len(got) != 2 || got[0] != catalog.PostgresBasic || got[1] != catalog.RedisBasic {
		t.Fatalf("old agent: %v", got)
	}
	if len(o.Unavailable) != 1 || o.Unavailable[0].Kind != resourcekind.Files || o.Unavailable[0].Reason == "" {
		t.Fatalf("unavailable %+v", o.Unavailable)
	}
	// no cluster: every kind unavailable, with the same reason
	o, pol := offered(t, project.TierProduction, []string{})
	if len(o.Recipes) != 0 || len(o.Unavailable) != len(resourcekind.Kinds) {
		t.Fatalf("no cluster %+v", o)
	}
	for _, u := range o.Unavailable {
		if u.Reason != resource.NoClusterReason {
			t.Fatalf("no cluster %+v", u)
		}
	}
	if len(pol.asked) != 0 {
		t.Fatalf("nothing to judge, asked %d", len(pol.asked))
	}
}

// The verdict is create_resource's default: the recipe connected to the whole
// app, so IT's rules on connecting count (a files volume, which an app of
// several services must give to one, is judged without it); a default outside
// the pre-approved range asks IT; an archived app is offered nothing.
func TestOfferedJudgesAsCreateDoes(t *testing.T) {
	ask := gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{Name: "connect-review"}}}
	o, _, conn, err := offeredWith(t, project.TierProduction, allCaps, offerSetup{
		connect: map[resourcekind.Kind]gate.Verdict{resourcekind.Postgres: ask},
		offer: func(cat *catalogsvc.Service) {
			// IT pre-approves production databases only up to 8 GiB: their
			// default, 10, is outside
			if _, err := cat.UpdateOffering(context.Background(), org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: identity.MustParseUserID("11111111-1111-4111-8111-111111111111"),
				OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction},
					Preapproved: map[string]catalog.Range{catalog.ParamStorage: {Min: 5, Max: 8}}}}); err != nil {
				t.Fatal(err)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]resource.Offer{}
	for _, r := range o.Recipes {
		by[r.Recipe] = r
	}
	basic, prod, files := by[catalog.PostgresBasic], by[catalog.PostgresProduction], by[catalog.FilesBasic]
	if basic.Verdict != gate.Approval || !slices.Contains(basic.Rules, "connect-review") {
		t.Fatalf("connecting is judged too: %+v", basic)
	}
	if prod.Verdict != gate.Approval || !slices.Contains(prod.Rules, catalog.RuleCatalogName) {
		t.Fatalf("a default outside the pre-approved range asks IT: %+v", prod)
	}
	if basic.RulesVerdict != gate.Approval {
		t.Fatalf("the rules on connecting are IT's rules: %+v", basic)
	}
	if files.Verdict != gate.Allow {
		t.Fatalf("files judged without the connection the app's shape refuses: %+v", files)
	}
	if !slices.Contains(conn.asked, resourcekind.Redis) || !slices.Contains(conn.asked, resourcekind.Files) {
		t.Fatalf("connect asked %v", conn.asked)
	}

	if _, _, _, err := offeredWith(t, project.TierProduction, allCaps, offerSetup{status: project.StatusArchived}); err == nil {
		t.Fatal("an archived app was offered something")
	}
}

// RulesVerdict is the verdict without the catalog's floor: a default outside
// the pre-approved range asks IT (Verdict), but a value inside it gets only
// what IT's rules say — the console's forecast for other values.
func TestOfferedSeparatesTheCatalogFloor(t *testing.T) {
	o, _, _, err := offeredWith(t, project.TierProduction, allCaps, offerSetup{
		offer: func(cat *catalogsvc.Service) {
			if _, err := cat.UpdateOffering(context.Background(), org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: identity.MustParseUserID("11111111-1111-4111-8111-111111111111"),
				OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierProduction},
					Preapproved: map[string]catalog.Range{catalog.ParamStorage: {Min: 5, Max: 8}}}}); err != nil {
				t.Fatal(err)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range o.Recipes {
		switch r.Recipe {
		case catalog.PostgresProduction:
			if r.Verdict != gate.Approval || r.RulesVerdict != gate.Allow || len(r.Rules) != 1 || r.Rules[0] != catalog.RuleCatalogName {
				t.Fatalf("floor only: %+v", r)
			}
		case catalog.RedisBasic:
			if r.Verdict != gate.Approval || r.RulesVerdict != gate.Approval {
				t.Fatalf("a rule: %+v", r)
			}
		default:
			if r.Verdict != gate.Allow || r.RulesVerdict != gate.Allow {
				t.Fatalf("nothing asks: %+v", r)
			}
		}
	}
}
