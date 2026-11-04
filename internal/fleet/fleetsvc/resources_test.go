package fleetsvc_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func TestResourcesFailClosedWithoutTheKind(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	envID, proj := project.NewEnvironmentID(), project.NewID()
	db := fleet.DesiredResource{Organization: e.org, Project: proj, Environment: envID, Name: "db", Kind: resourcekind.Postgres, Generation: 1, StorageGi: 1}
	connect := func(kinds ...string) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{ResourceKinds: kinds}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect() // an agent older than 2b
	err := e.svc.ApplyResource(ctx, db)
	if code(err) != fleet.CodeResourcesUnavailable || !errx.IsType(err, errx.TypeBusiness) ||
		err.(*errx.Error).Message != "Este clúster no tiene bases de datos administradas. Pide a TI que instale CloudNativePG." {
		t.Fatalf("resource without the kind: %v", err)
	}
	if got, _ := e.svc.DesiredResources(ctx, agent); len(got) != 0 {
		t.Fatalf("a refused resource was stored: %v", got)
	}
	app := fleet.App{Organization: e.org, Project: proj, Environment: envID, Name: "web", Revision: 1, Image: "r.local/web@sha256:00",
		Port: 8080, HealthPath: "/health", Replicas: 1, Resources: []fleet.ResourceRef{{Name: "db", Kind: resourcekind.Postgres}}}
	if err := e.svc.Apply(ctx, app); code(err) != fleet.CodeResourcesUnavailable {
		t.Fatalf("app using a missing kind: %v", err)
	}
	if kinds, err := e.svc.ResourceKinds(ctx, e.org, envID); err != nil || len(kinds) != 0 {
		t.Fatalf("kinds %v %v", kinds, err)
	}

	connect("redis", "files")
	if err := e.svc.ApplyResource(ctx, db); code(err) != fleet.CodeResourcesUnavailable {
		t.Fatalf("postgres without postgres: %v", err)
	}
	connect("postgres", "redis", "files")
	if err := e.svc.ApplyResource(ctx, db); err != nil {
		t.Fatal(err)
	}
	if kinds, _ := e.svc.ResourceKinds(ctx, e.org, envID); len(kinds) != 3 {
		t.Fatalf("kinds %v", kinds)
	}
	if err := e.svc.Apply(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := e.svc.DesiredApp(ctx, agent, envID, "web"); !ok || len(got.Resources) != 1 || got.Resources[0].Name != "db" {
		t.Fatalf("app resources %+v", got.Resources)
	}
	if got, ok, err := e.svc.DesiredResource(ctx, agent, envID, "db"); !ok || err != nil || got.StorageGi != 1 || got.Cluster != agent.Cluster {
		t.Fatalf("desired resource %+v %v %v", got, ok, err)
	}

	// a deletion is sent even when the cluster lost the kind
	connect()
	del := db
	del.Generation, del.Deleted = 2, true
	if err := e.svc.ApplyResource(ctx, del); err != nil {
		t.Fatalf("delete on a cluster without the kind: %v", err)
	}
	if got, _, _ := e.svc.DesiredResource(ctx, agent, envID, "db"); !got.Deleted {
		t.Fatalf("not marked deleted: %+v", got)
	}
}

func TestResourceReportedGoneIsForgottenAndAnnounced(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	session := fleet.NewSession()
	if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{ResourceKinds: []string{"redis"}}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	envID := project.NewEnvironmentID()
	r := fleet.DesiredResource{Organization: e.org, Project: project.NewID(), Environment: envID, Name: "cache", Kind: resourcekind.Redis, Generation: 1, StorageGi: 1}
	if err := e.svc.ApplyResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	report := func(gen int64, phase fleet.ResourcePhase) fleet.ResourceReport {
		return fleet.ResourceReport{Resources: []fleet.ResourceStatus{{Environment: envID, Name: "cache", ObservedGeneration: gen, Phase: phase}}}
	}
	e.events.Reset()
	if err := e.svc.ReportResources(ctx, agent, session, report(1, fleet.ResourceReady)); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.ResourceStatus(ctx, e.org, envID, "cache"); st.Phase != fleet.ResourceReady {
		t.Fatalf("status %+v", st)
	}
	if st, _ := e.svc.ResourceStatus(ctx, e.org, envID, "nope"); st.Phase != "" || st.ObservedGeneration != 0 {
		t.Fatalf("unknown status %+v", st)
	}
	r.Generation, r.Deleted = 2, true
	if err := e.svc.ApplyResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := e.events.Types(); len(got) == 0 || got[len(got)-1] != fleet.EventResourceApplied {
		t.Fatalf("every applied generation is announced: %v", got)
	}
	e.events.Reset()
	// a report with an invalid phase is dropped, the rest kept
	bad := report(2, fleet.ResourcePhase("weird"))
	if err := e.svc.ReportResources(ctx, agent, session, bad); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("invalid phase: %v", err)
	}
	if err := e.svc.ReportResources(ctx, agent, fleet.NewSession(), report(2, fleet.ResourceDeleted)); err == nil {
		t.Fatal("a tunnel that is not the cluster's reported")
	}
	if len(e.events.Types()) != 0 {
		t.Fatalf("events %v", e.events.Types())
	}
	if err := e.svc.ReportResources(ctx, agent, session, report(2, fleet.ResourceDeleted)); err != nil {
		t.Fatal(err)
	}
	if got := e.events.Types(); len(got) != 1 || got[0] != fleet.EventResourceRemoved {
		t.Fatalf("events %v", got)
	}
	if got, _ := e.svc.DesiredResources(ctx, agent); len(got) != 0 {
		t.Fatalf("still desired: %v", got)
	}
}
