package catalogsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogsvc"
	"github.com/Abraxas-365/odyssey/internal/catalog/catalogtest"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// teams is a fake IAMKit directory: the organization's groups and who is in
// which.
type teams struct {
	groups  []catalog.Team
	members map[identity.UserID][]string
	err     error
	reads   int
}

func (d *teams) Teams(context.Context, identity.OrganizationID) ([]catalog.Team, error) {
	return d.groups, d.err
}

func (d *teams) TeamsOf(_ context.Context, _ identity.OrganizationID, u identity.UserID) ([]string, error) {
	d.reads++
	return d.members[u], d.err
}

var (
	ana   = identity.MustParseUserID("11111111-1111-4111-8111-111111111111") // datos
	bruno = identity.MustParseUserID("22222222-2222-4222-8222-222222222222") // ventas
	carla = identity.MustParseUserID("33333333-3333-4333-8333-333333333333") // datos and ventas
	diego = identity.MustParseUserID("44444444-4444-4444-8444-444444444444") // no team
)

func withTeams() (*catalogsvc.Service, *teams, *catalogtest.Repo, *eventtest.Recorder) {
	events, repo := &eventtest.Recorder{}, catalogtest.NewRepo()
	d := &teams{groups: []catalog.Team{{ID: "grp_datos", Name: "Datos", Members: 2}, {ID: "grp_ventas", Name: "Ventas", Members: 2}},
		members: map[identity.UserID][]string{ana: {"grp_datos"}, bruno: {"grp_ventas"}, carla: {"grp_datos", "grp_ventas"}}}
	svc := catalogsvc.New(repo, txn.Passthrough{}, events)
	svc.UseDirectory(d)
	return svc, d, repo, events
}

var production = []project.Tier{project.TierProduction}

func offer(t *testing.T, svc *catalogsvc.Service, org identity.OrganizationID, recipe string, d catalog.OfferingDraft) {
	t.Helper()
	if _, err := svc.UpdateOffering(context.Background(), org, recipe, catalog.UpdateOffering{UpdatedBy: user, OfferingDraft: d}); err != nil {
		t.Fatalf("offer %s: %v", recipe, err)
	}
}

func postgresFor(t *testing.T, svc *catalogsvc.Service, org identity.OrganizationID, asker identity.UserID) (string, error) {
	t.Helper()
	r, err := svc.Resolve(context.Background(), org, catalog.Request{Kind: resourcekind.Postgres, Tier: project.TierProduction,
		Capabilities: modern, Asker: asker})
	return r.Recipe, err
}

func TestOfferingsForTeams(t *testing.T) {
	svc, _, _, events := withTeams()
	ctx, org := context.Background(), identity.NewOrganizationID()

	// production Postgres only for Datos, and their default; everyone else
	// keeps the basic one as theirs: a team's default over everyone's is no tie
	offer(t, svc, org, catalog.PostgresProduction, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Defaults: production, Teams: []string{"grp_datos", "grp_datos"}})
	if got, err := postgresFor(t, svc, org, ana); err != nil || got != catalog.PostgresProduction {
		t.Fatalf("Datos' default: %s %v", got, err)
	}
	if got, err := postgresFor(t, svc, org, bruno); err != nil || got != catalog.PostgresBasic {
		t.Fatalf("everyone's default: %s %v", got, err)
	}
	// the event says which teams (de-duplicated)
	var payload catalog.OfferingUpdated
	last := events.Events()[len(events.Events())-1]
	if err := json.Unmarshal(last.Payload, &payload); err != nil || !slices.Equal(payload.Teams, []string{"grp_datos"}) {
		t.Fatalf("event teams: %+v %v", payload, err)
	}

	// asked by name: not offered to someone outside the team, in words
	_, err := svc.Resolve(ctx, org, catalog.Request{Kind: resourcekind.Postgres, Recipe: catalog.PostgresProduction,
		Tier: project.TierProduction, Capabilities: modern, Asker: bruno})
	if code(err) != catalog.CodeUnavailable {
		t.Fatalf("outsider by name: %v", err)
	}
	// nobody (no asker: a system path) is in no team
	if got, err := postgresFor(t, svc, org, identity.UserID{}); err != nil || got != catalog.PostgresBasic {
		t.Fatalf("no asker: %s %v", got, err)
	}

	// what each one sees, the default first
	seen := func(u identity.UserID) []string {
		entries, err := svc.Available(ctx, org, project.TierProduction, modern, u)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			if e.Kind == resourcekind.Postgres {
				out = append(out, e.Recipe)
			}
		}
		return out
	}
	if got := seen(ana); !slices.Equal(got, []string{catalog.PostgresProduction, catalog.PostgresBasic}) {
		t.Fatalf("Datos sees %v", got)
	}
	if got := seen(bruno); !slices.Equal(got, []string{catalog.PostgresBasic}) {
		t.Fatalf("Ventas sees %v", got)
	}

	// a default for teams in common is a tie; for other teams it is not
	_, err = svc.UpdateOffering(ctx, org, catalog.PostgresBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: production, Defaults: production,
			Teams: []string{"grp_ventas", "grp_datos"}}})
	if code(err) != catalog.CodeDefaultTaken {
		t.Fatalf("tie on a common team: %v", err)
	}
	offer(t, svc, org, catalog.PostgresBasic, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Defaults: production, Teams: []string{"grp_ventas"}})
	// in both teams, each with its default: the person chooses
	if _, err := postgresFor(t, svc, org, carla); code(err) != catalog.CodeChooseRecipe {
		t.Fatalf("two teams' defaults: %v", err)
	}
	// in no team: nothing is offered to them
	if _, err := postgresFor(t, svc, org, diego); code(err) != catalog.CodeUnavailable {
		t.Fatalf("no team: %v", err)
	}

	// teams narrow who creates, never what exists: changing ignores them
	if _, err := svc.ResolveChange(ctx, org, catalog.Change{Recipe: catalog.PostgresProduction, Version: 1, Tier: project.TierProduction,
		Current: map[string]int{catalog.ParamStorage: 10}, Params: map[string]int{catalog.ParamStorage: 20}}); err != nil {
		t.Fatalf("change by anyone: %v", err)
	}
}

func TestOfferingTeamsMustExist(t *testing.T) {
	svc, d, _, _ := withTeams()
	ctx, org := context.Background(), identity.NewOrganizationID()
	_, err := svc.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: production, Teams: []string{"grp_finanzas"}}})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("unknown team: %v", err)
	}
	for _, bad := range []string{"", "a b", "../x"} {
		_, err := svc.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
			OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: production, Teams: []string{bad}}})
		if !errx.IsType(err, errx.TypeValidation) {
			t.Fatalf("malformed team %q: %v", bad, err)
		}
	}
	offer(t, svc, org, catalog.PostgresProduction, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Teams: []string{"grp_datos"}})

	// the team is deleted in IAMKit: editing the offering keeps working
	d.groups = d.groups[1:]
	offer(t, svc, org, catalog.PostgresProduction, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Teams: []string{"grp_datos"}, Limits: map[string]catalog.Range{catalog.ParamStorage: {Min: 5, Max: 40}}})

	// the console lists them
	got, err := svc.Teams(ctx, org)
	if err != nil || !got.Available || len(got.Items) != 1 || got.Items[0].ID != "grp_ventas" {
		t.Fatalf("teams: %+v %v", got, err)
	}
}

func TestTeamsAreReadOnlyWhenAnOfferingNamesThem(t *testing.T) {
	svc, d, _, _ := withTeams()
	org := identity.NewOrganizationID()
	if _, err := postgresFor(t, svc, org, ana); err != nil || d.reads != 0 {
		t.Fatalf("read teams with no offering for teams: %d %v", d.reads, err)
	}
	offer(t, svc, org, catalog.PostgresProduction, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Teams: []string{"grp_datos"}})
	if _, err := postgresFor(t, svc, org, ana); err != nil || d.reads != 1 {
		t.Fatalf("teams read %d times: %v", d.reads, err)
	}
	// IAMKit failing is an error, not "in no team" (a team's default would
	// silently become someone else's)
	d.err = errx.Wrap(errors.New("down"), "list the person's teams", errx.TypeExternal)
	if _, err := postgresFor(t, svc, org, ana); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("directory down: %v", err)
	}
}

func TestWithoutADirectoryOfferingsForTeamsAreNobodys(t *testing.T) {
	svc, _, repo, _ := withTeams()
	ctx, org := context.Background(), identity.NewOrganizationID()
	offer(t, svc, org, catalog.PostgresProduction, catalog.OfferingDraft{Version: 1, Active: true, Tiers: production,
		Defaults: production, Teams: []string{"grp_datos"}})

	// the same catalog, read by an Odicea without the IAMKit directory
	bare := catalogsvc.New(repo, txn.Passthrough{}, &eventtest.Recorder{})
	if got, err := postgresFor(t, bare, org, ana); err != nil || got != catalog.PostgresBasic {
		t.Fatalf("fail closed: %s %v", got, err)
	}
	_, err := bare.UpdateOffering(ctx, org, catalog.PostgresBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: production, Teams: []string{"grp_datos"}}})
	if code(err) != catalog.CodeTeamsUnavailable {
		t.Fatalf("naming teams without a directory: %v", err)
	}
	// the teams it already has are kept on edit
	if _, err := bare.UpdateOffering(ctx, org, catalog.PostgresProduction, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: production, Teams: []string{"grp_datos"}}}); err != nil {
		t.Fatalf("keeping teams without a directory: %v", err)
	}
	if got, err := bare.Teams(ctx, org); err != nil || got.Available || got.Items == nil {
		t.Fatalf("teams without a directory: %+v %v", got, err)
	}
}
