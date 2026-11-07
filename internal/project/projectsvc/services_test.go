package projectsvc_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// runtime answers CanRunServices with ok and records what it was asked.
type runtime struct {
	ok    bool
	asked [][]project.EnvironmentID
}

func (r *runtime) CanRunServices(_ context.Context, _ identity.OrganizationID, envs []project.EnvironmentID) (bool, error) {
	r.asked = append(r.asked, envs)
	return r.ok, nil
}

// rules answers a fixed effect (allow by default) and records the requests.
type rules struct {
	effect gate.Effect
	asked  []gate.Request
}

func (r *rules) Check(_ context.Context, _ identity.OrganizationID, req gate.Request) (gate.Verdict, error) {
	r.asked = append(r.asked, req)
	v := gate.Verdict{Effect: r.effect}
	if v.Effect == "" {
		v.Effect = gate.Allow
	}
	if v.Effect != gate.Allow {
		v.Rules = []gate.Rule{{ID: "r1", Name: "Servicios de producción se aprueban"}}
		v.Approvers = []string{"odyssey:approvals:decide"}
	}
	return v, nil
}

// approvals records what is held and redeemed.
type approvals struct {
	held     []gate.Hold
	redeemed []gate.Grant
}

func (a *approvals) Hold(_ context.Context, _ identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	a.held = append(a.held, h)
	return gate.NewPending("req-1"), nil
}

func (a *approvals) Redeem(_ context.Context, _ identity.OrganizationID, request string, g gate.Grant) error {
	if request != "req-1" {
		return errx.Forbidden("not usable")
	}
	a.redeemed = append(a.redeemed, g)
	return nil
}

var monorepo = []project.ServiceSpec{
	{Name: "web", Root: "apps/web", Route: "/"},
	{Name: "api", Root: "apps/api", Route: "/api", StripPrefix: true},
	{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"},
}

func newApp(t *testing.T, rt project.Runtime) (*projectsvc.Service, *eventtest.Recorder, identity.OrganizationID, project.ID) {
	t.Helper()
	svc, events, org, id, _ := newGovernedApp(t, rt, &rules{})
	return svc, events, org, id
}

func newGovernedApp(t *testing.T, rt project.Runtime, r *rules) (*projectsvc.Service, *eventtest.Recorder, identity.OrganizationID, project.ID, *approvals) {
	t.Helper()
	events := &eventtest.Recorder{}
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, events)
	if rt != nil {
		svc.UseRuntime(rt)
	}
	a := &approvals{}
	svc.UsePolicy(r)
	svc.UseApprovals(a)
	org := identity.NewOrganizationID()
	id, err := svc.Create(context.Background(), org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	return svc, events, org, id, a
}

func TestEveryAppIsBornWithOneWebService(t *testing.T) {
	svc, _, org, id := newApp(t, nil)
	got, err := svc.Services(context.Background(), org, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "web" || got[0].Kind != project.ServiceWeb || got[0].Route != "/" || got[0].Root != "" {
		t.Fatalf("services of a new app = %+v", got)
	}
	if _, err := svc.Services(context.Background(), identity.NewOrganizationID(), id); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant read the services: %v", err)
	}
}

func TestSetServicesNeedsClustersThatRunThem(t *testing.T) {
	ctx := context.Background()
	for name, rt := range map[string]project.Runtime{"no runtime": nil, "old agent": &runtime{ok: false}} {
		svc, events, org, id := newApp(t, rt)
		events.Reset()
		_, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
		var e *errx.Error
		if !errx.As(err, &e) || e.Type != errx.TypeBusiness || e.Code != project.CodeServicesUnavailable {
			t.Fatalf("%s: SetServices = %v, want %s", name, err, project.CodeServicesUnavailable)
		}
		if len(events.Events()) != 0 {
			t.Fatalf("%s: a refused change published %v", name, events.Types())
		}
		got, _ := svc.Services(ctx, org, id)
		if len(got) != 1 || got[0].Name != "web" {
			t.Fatalf("%s: a refused change stored %+v", name, got)
		}
		// Renaming the one service at "/" with all of the code needs nothing.
		if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: []project.ServiceSpec{{Name: "site", Route: "/"}}, UpdatedBy: user}); err != nil {
			t.Fatalf("%s: a simple list was refused: %v", name, err)
		}
	}
}

func TestSetServicesReplacesTheList(t *testing.T) {
	ctx := event.WithMeta(context.Background(), event.Meta{CorrelationID: "req-9", Client: "mcp"})
	rt := &runtime{ok: true}
	svc, events, org, id := newApp(t, rt)
	envs, err := svc.ListEnvironments(ctx, org, id, project.EnvironmentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	events.Reset()

	out, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Items
	if out.Held() || len(got) != 3 || got[0].Name != "web" || got[1].Name != "api" || got[2].Name != "jobs" ||
		got[1].Route != "/api" || !got[1].StripPrefix || got[2].Kind != project.ServiceWorker || got[0].Root != "apps/web" {
		t.Fatalf("stored %+v", got)
	}
	if len(rt.asked) != 1 || len(rt.asked[0]) != len(envs) || rt.asked[0][0] != envs[0].ID {
		t.Fatalf("asked the runtime about %v, want the app's environments %v", rt.asked, envs)
	}
	published := events.Events()
	if len(published) != 1 || published[0].Type != project.EventServicesChanged || published[0].Actor != user ||
		published[0].Client != "mcp" || published[0].Subject != "project/"+id.String() {
		t.Fatalf("published %+v", published)
	}
	payload, _ := event.Decode[project.ServicesChanged](published[0])
	if len(payload.From) != 1 || payload.From[0].Name != "web" || len(payload.To) != 3 {
		t.Fatalf("payload %+v", payload)
	}

	// The same list again (spelled differently) is a no-op.
	events.Reset()
	again := []project.ServiceSpec{
		{Name: "web", Root: "/apps/web/", Route: "/"},
		{Name: "api", Kind: project.ServiceWeb, Root: "apps/api", Route: "/api/", StripPrefix: true},
		{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"},
	}
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: again, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if len(events.Events()) != 0 {
		t.Fatalf("a no-op published %v", events.Types())
	}
}

func TestSetServicesGuards(t *testing.T) {
	ctx := context.Background()
	svc, _, org, id := newApp(t, &runtime{ok: true})
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("no editor: %v", err)
	}
	if _, err := svc.SetServices(ctx, identity.NewOrganizationID(), id, project.SetServices{Items: monorepo, UpdatedBy: user}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant changed the services: %v", err)
	}
	if err := svc.Archive(ctx, org, id, project.Archive{ArchivedBy: user}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	var e *errx.Error
	if !errx.As(err, &e) || e.Code != project.CodeArchived {
		t.Fatalf("archived app: %v", err)
	}
}

// IT's rules judge a change of services (MS-3c): with the new list, what it
// adds, removes or modifies, and a change of topology; never a no-op.
func TestSetServicesIsJudgedByITsRules(t *testing.T) {
	ctx := context.Background()
	r := &rules{}
	svc, events, org, id, _ := newGovernedApp(t, &runtime{ok: true}, r)
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if len(r.asked) != 1 {
		t.Fatalf("asked %d times", len(r.asked))
	}
	got := r.asked[0]
	if got.Action != gate.ActionSetServices || got.Environment != "" || !got.TopologyChanged || got.ProjectSlug == "" ||
		!slices.Equal(got.Changed, []string{"web", "api", "jobs"}) || len(got.Services) != 3 {
		t.Fatalf("judged %+v", got)
	}
	if s := got.Services; s[0] != (gate.Service{Name: "web", Kind: "web", Route: "/", Public: true}) ||
		s[1] != (gate.Service{Name: "api", Kind: "web", Route: "/api", Public: true}) ||
		s[2] != (gate.Service{Name: "jobs", Kind: "worker"}) {
		t.Fatalf("services as the rules see them %+v", s)
	}

	// The same list: a no-op, not judged.
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user}); err != nil || len(r.asked) != 1 {
		t.Fatalf("a no-op was judged (%d) %v", len(r.asked), err)
	}

	// Dropping jobs and moving api: changed names the moved and the removed.
	events.Reset()
	next := []project.ServiceSpec{monorepo[0], {Name: "api", Root: "apps/api", Route: "/v1"}}
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: next, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if got := r.asked[1]; !slices.Equal(got.Changed, []string{"api", "jobs"}) || len(got.Services) != 2 {
		t.Fatalf("second change judged %+v", got)
	}
}

func TestSetServicesDeniedChangesNothing(t *testing.T) {
	ctx := context.Background()
	svc, events, org, id, a := newGovernedApp(t, &runtime{ok: true}, &rules{effect: gate.Deny})
	events.Reset()
	_, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	var e *errx.Error
	if !errx.As(err, &e) || !gate.Refusal(e) {
		t.Fatalf("denied: %v", err)
	}
	if got, _ := svc.Services(ctx, org, id); len(got) != 1 || len(events.Events()) != 0 || len(a.held) != 0 {
		t.Fatalf("a denied change stored %+v, published %v, held %v", got, events.Types(), a.held)
	}
}

// Held for approval, nothing changes; the approved change re-runs as the
// requester with the approval's mark and redeems exactly what was held.
func TestSetServicesHeldAndRedeemed(t *testing.T) {
	ctx := context.Background()
	svc, events, org, id, a := newGovernedApp(t, &runtime{ok: true}, &rules{effect: gate.Approval})
	events.Reset()
	out, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Held() || out.Pending.RequestID != "req-1" || len(out.Items) != 1 || out.Items[0].Name != "web" {
		t.Fatalf("held answer %+v", out)
	}
	if got, _ := svc.Services(ctx, org, id); len(got) != 1 || len(events.Events()) != 0 {
		t.Fatalf("a held change stored %+v, published %v", got, events.Types())
	}
	if len(a.held) != 1 {
		t.Fatalf("held %v", a.held)
	}
	h := a.held[0]
	if h.Action != gate.ActionSetServices || h.Project != id.String() || h.Environment != "" || h.Summary == "" {
		t.Fatalf("hold %+v", h)
	}
	payload, ok := h.Payload.(project.SetServicesApproval)
	if !ok || payload.Op != project.OpSetServices || len(payload.Items) != 3 || payload.Items[2].Kind != project.ServiceWorker {
		t.Fatalf("payload %#v", h.Payload)
	}

	// The executor's re-run: same items (as decoded from the stored payload).
	approved := gate.WithApproval(ctx, "req-1")
	out, err = svc.SetServices(approved, org, id, project.SetServices{Items: payload.Items, UpdatedBy: user})
	if err != nil || out.Held() || len(out.Items) != 3 {
		t.Fatalf("approved re-run %+v %v", out, err)
	}
	if len(a.redeemed) != 1 || a.redeemed[0].Action != gate.ActionSetServices || a.redeemed[0].Environment != "" {
		t.Fatalf("redeemed %+v", a.redeemed)
	}
	if !slices.Equal(events.Types(), []string{project.EventServicesChanged}) {
		t.Fatalf("published %v", events.Types())
	}
}

// Without IT's rules wired, a change is refused (fail closed); a no-op is not.
func TestSetServicesFailsClosedWithoutRules(t *testing.T) {
	ctx := context.Background()
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	svc.UseRuntime(&runtime{ok: true})
	org := identity.NewOrganizationID()
	id, err := svc.Create(ctx, org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user}); !errx.IsType(err, errx.TypeInternal) {
		t.Fatalf("no rules wired: %v", err)
	}
	same := []project.ServiceSpec{{Name: "web", Route: "/"}}
	if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: same, UpdatedBy: user}); err != nil {
		t.Fatalf("a no-op needs no rules: %v", err)
	}
}

// racer changes the services while the first change is being judged: the
// judged change is not stored over a list nobody judged it against.
type racer struct {
	rules
	race func()
}

func (r *racer) Check(ctx context.Context, org identity.OrganizationID, req gate.Request) (gate.Verdict, error) {
	if r.race != nil {
		race := r.race
		r.race = nil
		race()
	}
	return r.rules.Check(ctx, org, req)
}

func TestSetServicesRejudgesAChangeMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	r := &racer{}
	svc, _, org, id, _ := newGovernedApp(t, &runtime{ok: true}, &r.rules)
	svc.UsePolicy(r)
	r.race = func() {
		if _, err := svc.SetServices(ctx, org, id, project.SetServices{Items: []project.ServiceSpec{{Name: "site", Route: "/"}}, UpdatedBy: user}); err != nil {
			t.Error(err)
		}
	}
	out, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	if err != nil || len(out.Items) != 3 {
		t.Fatalf("change %+v %v", out, err)
	}
	// judged: the first change (from web), the racing one, then the first
	// again from what the racer stored (site goes away)
	if len(r.asked) != 3 || !slices.Contains(r.asked[2].Changed, "site") {
		t.Fatalf("judged %+v", r.asked)
	}
}
