package projectsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/projectsvc"
	"github.com/Abraxas-365/odyssey/internal/project/projecttest"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

var user = identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")

func TestEveryWritePublishesItsEvent(t *testing.T) {
	events := &eventtest.Recorder{}
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, events)
	ctx := event.WithMeta(context.Background(), event.Meta{CorrelationID: "req-1", Client: "console"})
	org := identity.NewOrganizationID()

	id, err := svc.Create(ctx, org, project.Create{Name: " Visitas a clientes ", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	name, same := "Visitas", "Visitas"
	if err := svc.Update(ctx, org, id, project.Update{Name: &name, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(ctx, org, id, project.Update{Name: &same, UpdatedBy: user}); err != nil { // no-op
		t.Fatal(err)
	}
	if err := svc.Archive(ctx, org, id, project.Archive{ArchivedBy: user}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(ctx, org, id, project.Archive{ArchivedBy: user}); err != nil { // no-op
		t.Fatal(err)
	}

	got := events.Events()
	want := []string{project.EventCreated, project.EventEnvironmentCreated, project.EventUpdated, project.EventArchived}
	if len(got) != len(want) {
		t.Fatalf("published %v, want %v (no-ops publish nothing)", events.Types(), want)
	}
	for i, e := range got {
		subject := "project/" + id.String()
		if e.Type == project.EventEnvironmentCreated {
			env, _ := event.Decode[project.EnvironmentCreated](e)
			subject = "environment/" + env.Environment.String()
		}
		if e.Type != want[i] || e.Organization != org || e.Actor != user || e.Subject != subject ||
			e.CorrelationID != "req-1" || e.Client != "console" {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	created, _ := event.Decode[project.Created](got[0])
	if created.Name != "Visitas a clientes" || created.Slug != "visitas-a-clientes" || created.Project != id {
		t.Fatalf("created payload %+v", created)
	}
	if got[0].Summary != "Creó la app Visitas a clientes" || got[3].Summary != "Archivó la app Visitas" {
		t.Fatalf("summaries %q / %q", got[0].Summary, got[3].Summary)
	}
}

func TestEveryProjectIsBornWithProduction(t *testing.T) {
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	ctx := context.Background()
	org := identity.NewOrganizationID()
	id, err := svc.Create(ctx, org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := svc.ListEnvironments(ctx, org, id, project.EnvironmentFilter{})
	if err != nil || len(envs) != 1 || envs[0].Slug != project.ProductionSlug || envs[0].Kind != project.KindProduction {
		t.Fatalf("environments %+v %v", envs, err)
	}
	if prod, err := svc.FindEnvironmentBySlug(ctx, org, id, project.ProductionSlug); err != nil || prod.ID != envs[0].ID {
		t.Fatalf("production %+v %v", prod, err)
	}
	if _, err := svc.ListEnvironments(ctx, identity.NewOrganizationID(), id, project.EnvironmentFilter{}); err == nil {
		t.Fatal("another organization lists the environments")
	}
	if _, err := svc.ListEnvironments(ctx, org, id, project.EnvironmentFilter{Kind: []project.EnvironmentKind{"qa"}}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestAFailedPublishFailsTheWrite(t *testing.T) {
	boom := errors.New("outbox down")
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{Err: boom})
	if _, err := svc.Create(context.Background(), identity.NewOrganizationID(), project.Create{Name: "Portal", CreatedBy: user}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
