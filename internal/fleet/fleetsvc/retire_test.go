package fleetsvc_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestRetiredAppIsSentOnlyToAnAgentThatRetiresAndForgottenWhenGone(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	session := fleet.NewSession()
	connect := func(apis ...string) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{APIs: apis}}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect() // an agent older than the retirement
	envID, proj := project.NewEnvironmentID(), project.NewID()
	retire := fleet.Retirement{Organization: e.org, Project: proj, Environment: envID, Name: "web", Revision: 2}

	// never deployed: nothing to retire, nothing placed
	if ok, err := e.svc.Retire(ctx, retire); err != nil || ok {
		t.Fatalf("retire of an app never deployed: %v %v", ok, err)
	}
	if p, _ := e.svc.Placements(ctx, e.org, []project.EnvironmentID{envID}); len(p) != 0 {
		t.Fatalf("a retirement placed the environment: %v", p)
	}
	if _, err := e.svc.Retire(ctx, fleet.Retirement{Organization: e.org, Project: proj, Environment: envID, Name: "web"}); err == nil {
		t.Fatal("a retirement without a revision was accepted")
	}

	app := fleet.App{Organization: e.org, Project: proj, Environment: envID, Name: "web", Revision: 1, Image: "r.local/web@sha256:00",
		Port: 8080, HealthPath: "/health", Replicas: 1, Access: fleet.AccessPublic}
	if err := e.svc.Apply(ctx, app); err != nil {
		t.Fatal(err)
	}
	e.events.Reset()
	if ok, err := e.svc.Retire(ctx, retire); err != nil || !ok {
		t.Fatalf("retire %v %v", ok, err)
	}
	if got := e.events.Types(); len(got) != 1 || got[0] != fleet.EventAppRetired {
		t.Fatalf("events %v", got)
	}
	if ok, _ := e.svc.Retire(ctx, retire); ok {
		t.Fatal("a redelivered retirement counted twice")
	}

	// held back from an agent that would run it again
	if got, _ := e.svc.Desired(ctx, agent); len(got) != 0 {
		t.Fatalf("a retirement went to an agent without %s: %+v", fleet.CapabilityRetire, got)
	}
	if _, ok, _ := e.svc.DesiredApp(ctx, agent, envID, "web"); ok {
		t.Fatal("a retirement went to an agent that cannot retire")
	}
	connect(fleet.CapabilityRetire)
	got, _ := e.svc.Desired(ctx, agent)
	if len(got) != 1 || !got[0].Deleted || got[0].Revision != 2 {
		t.Fatalf("desired %+v", got)
	}
	if a, ok, _ := e.svc.DesiredApp(ctx, agent, envID, "web"); !ok || !a.Deleted {
		t.Fatalf("desired app %+v %v", a, ok)
	}

	// gone: forgotten in the same transaction as its event
	e.events.Reset()
	gone := fleet.Report{Apps: []fleet.AppStatus{{Environment: envID, Name: "web", ObservedRevision: 2, Phase: fleet.PhaseDeleted}}}
	if err := e.svc.Report(ctx, agent, fleet.NewSession(), gone); err == nil {
		t.Fatal("a tunnel that is not the cluster's reported")
	}
	if len(e.events.Types()) != 0 {
		t.Fatalf("events %v", e.events.Types())
	}
	if err := e.svc.Report(ctx, agent, session, gone); err != nil {
		t.Fatal(err)
	}
	if got := e.events.Types(); len(got) != 1 || got[0] != fleet.EventAppRemoved {
		t.Fatalf("events %v", got)
	}
	if got, _ := e.svc.Desired(ctx, agent); len(got) != 0 {
		t.Fatalf("still desired: %+v", got)
	}
	if st, err := e.svc.Status(ctx, e.org, envID, "web"); err != nil || st.ObservedRevision != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
}
