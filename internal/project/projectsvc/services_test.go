package projectsvc_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
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

var monorepo = []project.ServiceSpec{
	{Name: "web", Root: "apps/web", Route: "/"},
	{Name: "api", Root: "apps/api", Route: "/api", StripPrefix: true},
	{Name: "jobs", Kind: project.ServiceWorker, Root: "apps/jobs"},
}

func newApp(t *testing.T, rt project.Runtime) (*projectsvc.Service, *eventtest.Recorder, identity.OrganizationID, project.ID) {
	t.Helper()
	events := &eventtest.Recorder{}
	svc := projectsvc.New(projecttest.NewRepo(), txn.Passthrough{}, events)
	if rt != nil {
		svc.UseRuntime(rt)
	}
	org := identity.NewOrganizationID()
	id, err := svc.Create(context.Background(), org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	return svc, events, org, id
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

	got, err := svc.SetServices(ctx, org, id, project.SetServices{Items: monorepo, UpdatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "web" || got[1].Name != "api" || got[2].Name != "jobs" ||
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
