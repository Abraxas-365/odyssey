package fleetsvc_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
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

// production is what postgres-produccion@1 resolves to.
var production = resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17, CPU: "1", Memory: "2Gi"}}

var specCaps = []string{resourcespec.SpecV1, resourcespec.PostgresInstances, resourcespec.PostgresVersion, resourcespec.PostgresResources}

func TestARecipeGoesOnlyToAnAgentThatHonoursIt(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	envID := project.NewEnvironmentID()
	connect := func(caps ...string) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "v1",
			Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres"}, ResourceCapabilities: caps}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	db := fleet.DesiredResource{Organization: e.org, Project: project.NewID(), Environment: envID, Name: "db", Kind: resourcekind.Postgres,
		Generation: 1, StorageGi: 10, Recipe: "postgres-produccion@1", Spec: production}

	// an agent older than the catalog would make a basic database instead
	connect()
	err := e.svc.ApplyResource(ctx, db)
	if code(err) != fleet.CodeResourcesUnsupported || !errx.IsType(err, errx.TypeBusiness) || !strings.Contains(err.Error(), "actualización") {
		t.Fatalf("recipe on an old agent: %v", err)
	}
	if d := err.(*errx.Error).Details["missing"]; d != "builtin:postgres:instances,builtin:postgres:resources,builtin:postgres:version,spec/v1" {
		t.Fatalf("missing %v", d)
	}
	if caps, _ := e.svc.ResourceCapabilities(ctx, e.org, envID); !slices.Equal(caps, []string{"builtin:postgres"}) {
		t.Fatalf("an old agent's capabilities %v", caps)
	}
	connect(resourcespec.SpecV1, resourcespec.PostgresInstances) // half of what it needs
	if err := e.svc.ApplyResource(ctx, db); code(err) != fleet.CodeResourcesUnsupported {
		t.Fatalf("recipe on an agent lacking some: %v", err)
	}

	connect(specCaps...)
	if err := e.svc.ApplyResource(ctx, db); err != nil {
		t.Fatal(err)
	}
	got, ok, err := e.svc.DesiredResource(ctx, agent, envID, "db")
	if err != nil || !ok || got.Recipe != "postgres-produccion@1" || !reflect.DeepEqual(got.Spec, production) {
		t.Fatalf("stored %+v %v %v", got, ok, err)
	}
	if list, _ := e.svc.DesiredResources(ctx, agent); len(list) != 1 || !reflect.DeepEqual(list[0].Spec, production) {
		t.Fatalf("listed %+v", list)
	}
	// a basic resource stays basic
	basic := fleet.DesiredResource{Organization: e.org, Project: db.Project, Environment: envID, Name: "plain", Kind: resourcekind.Postgres,
		Generation: 1, StorageGi: 1, Recipe: "postgres-basico@1"}
	if err := e.svc.ApplyResource(ctx, basic); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := e.svc.DesiredResource(ctx, agent, envID, "plain"); !got.Spec.IsZero() || got.Spec.Postgres != nil {
		t.Fatalf("basic spec %+v", got.Spec)
	}
	// an invalid spec never reaches the cluster
	bad := db
	bad.Name, bad.Spec = "bad", resourcespec.Spec{Postgres: &resourcespec.Postgres{Major: 12}}
	if err := e.svc.ApplyResource(ctx, bad); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("invalid spec: %v", err)
	}
}

func TestAMoveRefusesAClusterThatCannotHonourARecipe(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	first, second := e.enrolled(t, "lima-1"), e.enrolled(t, "lima-2")
	connect := func(a fleet.AgentRef, caps ...string) {
		t.Helper()
		if err := e.svc.Connect(ctx, a, fleet.NewSession(), fleet.Agent{Version: "v1",
			Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres"}, ResourceCapabilities: caps}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect(first, specCaps...)
	connect(second) // older than the catalog
	envID := project.NewEnvironmentID()
	db := fleet.DesiredResource{Organization: e.org, Project: project.NewID(), Environment: envID, Name: "db", Kind: resourcekind.Postgres,
		Generation: 1, StorageGi: 10, Recipe: "postgres-produccion@1", Spec: production}
	if err := e.svc.ApplyResource(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := e.svc.DesiredResource(ctx, first, envID, "db"); got.Cluster != first.Cluster {
		t.Fatalf("placed on %v", got.Cluster)
	}
	to := second.Cluster
	err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &to, RevokedBy: e.user})
	if code(err) != fleet.CodeResourcesUnsupported {
		t.Fatalf("move to an agent that would make a basic database: %v", err)
	}
	var xe *errx.Error
	if !errx.As(err, &xe) || xe.Details["environment"] != envID.String() || xe.Details["resource"] != "db" {
		t.Errorf("the refusal says what holds the move back: %+v", xe)
	}
	if c, _ := e.svc.Find(ctx, e.org, first.Cluster); c.Status != fleet.StatusActive {
		t.Fatal("a refused move still revoked the cluster")
	}
	if _, ok, _ := e.svc.DesiredResource(ctx, first, envID, "db"); !ok {
		t.Fatal("a refused move moved the resource")
	}

	// once the target is updated, the move goes through
	connect(second, specCaps...)
	if err := e.svc.Revoke(ctx, e.org, first.Cluster, fleet.Revoke{MoveTo: &to, RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := e.svc.DesiredResource(ctx, second, envID, "db"); !ok || !reflect.DeepEqual(got.Spec, production) {
		t.Fatalf("moved %+v %v", got, ok)
	}
}
