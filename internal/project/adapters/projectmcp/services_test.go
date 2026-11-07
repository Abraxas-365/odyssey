package projectmcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/project/adapters/projectmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runtime answers CanRunServices with ok.
type runtime struct{ ok bool }

func (r runtime) CanRunServices(context.Context, identity.OrganizationID, []project.EnvironmentID) (bool, error) {
	return r.ok, nil
}

// rules answers a fixed effect (allow when empty).
type rules struct{ effect gate.Effect }

func (r rules) Check(context.Context, identity.OrganizationID, gate.Request) (gate.Verdict, error) {
	if r.effect == "" || r.effect == gate.Allow {
		return gate.Verdict{Effect: gate.Allow}, nil
	}
	return gate.Verdict{Effect: r.effect, Rules: []gate.Rule{{ID: "r1", Name: "Partes de producción se aprueban"}},
		Approvers: []string{"odyssey:approvals:decide"}}, nil
}

// approvals holds every change as req-1.
type approvals struct{ held int }

func (a *approvals) Hold(context.Context, identity.OrganizationID, gate.Hold) (gate.Pending, error) {
	a.held++
	return gate.NewPending("req-1"), nil
}

func (a *approvals) Redeem(context.Context, identity.OrganizationID, string, gate.Grant) error {
	return errx.Forbidden("not usable")
}

// governed wires the cluster capability and IT's rules into the fixture.
func (f fixture) governed(ok bool, effect gate.Effect) *approvals {
	a := &approvals{}
	f.svc.UseRuntime(runtime{ok: ok})
	f.svc.UsePolicy(rules{effect: effect})
	f.svc.UseApprovals(a)
	return a
}

func text(res *mcp.CallToolResult) string { return res.Content[0].(*mcp.TextContent).Text }

var monorepo = []any{
	map[string]any{"name": "web", "root": "apps/web", "route": "/"},
	map[string]any{"name": "api", "root": "/apps/api/", "route": "/api/", "strip_prefix": true},
	map[string]any{"name": "jobs", "kind": "worker", "root": "apps/jobs"},
}

func TestListServicesOfANewApp(t *testing.T) {
	f := newFixture(t)
	f.create(t, f.org, "Portal", "portal")
	var out projectmcp.ListServicesOut
	if res := call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsRead), "list_services", map[string]any{"app": "portal"}, &out); res.IsError {
		t.Fatalf("list_services: %+v", res.Content)
	}
	if out.App != "portal" || !out.Single || len(out.Items) != 1 || out.Items[0] != (projectmcp.ServiceOut{Name: "web", Kind: "web", Route: "/"}) ||
		!strings.Contains(out.NextStep, "detect_services") {
		t.Fatalf("list_services: %+v", out)
	}
}

func TestSetServicesSavesTheListAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	id := f.create(t, f.org, "Portal", "portal")
	f.governed(true, "")
	cs := f.connect(t, projectmcp.Options{}, permission.ProjectsRead, permission.ProjectsWrite)
	var out projectmcp.SetServicesOut
	if res := call(t, cs, "set_services", map[string]any{"app": "portal", "items": monorepo}, &out); res.IsError {
		t.Fatalf("set_services: %+v", res.Content)
	}
	want := []projectmcp.ServiceOut{{Name: "web", Kind: "web", Root: "apps/web", Route: "/"}, {Name: "api", Kind: "web", Root: "apps/api", Route: "/api", StripPrefix: true},
		{Name: "jobs", Kind: "worker", Root: "apps/jobs"}}
	if out.Status != "" || len(out.Items) != 3 || out.Items[0] != want[0] || out.Items[1] != want[1] || out.Items[2] != want[2] || !strings.Contains(out.NextStep, "deploy_folder") {
		t.Fatalf("set_services: %+v", out)
	}
	stored, err := f.svc.Services(context.Background(), f.org, id)
	if err != nil || len(stored) != 3 || stored[1].Root != "apps/api" {
		t.Fatalf("stored %+v %v", stored, err)
	}
	published := len(f.events.Events())
	if res := call(t, cs, "set_services", map[string]any{"app": "portal", "items": monorepo}, &out); res.IsError || len(f.events.Events()) != published {
		t.Fatalf("the same list must change nothing: %+v, %d events", res.Content, len(f.events.Events())-published)
	}
	var list projectmcp.ListServicesOut
	call(t, cs, "list_services", map[string]any{"app": id.String()}, &list)
	if list.Single || len(list.Items) != 3 || !strings.Contains(list.NextStep, "services") {
		t.Fatalf("list_services: %+v", list)
	}
}

func TestSetServicesHeldForApproval(t *testing.T) {
	f := newFixture(t)
	id := f.create(t, f.org, "Portal", "portal")
	a := f.governed(true, gate.Approval)
	var out projectmcp.SetServicesOut
	if res := call(t, f.connect(t, projectmcp.Options{}, permission.ProjectsRead, permission.ProjectsWrite), "set_services",
		map[string]any{"app": "portal", "items": monorepo}, &out); res.IsError {
		t.Fatalf("set_services: %+v", res.Content)
	}
	if out.Status != "pending_approval" || out.RequestID != "req-1" || a.held != 1 || out.NextStep == "" {
		t.Fatalf("held: %+v (held %d)", out, a.held)
	}
	if stored, _ := f.svc.Services(context.Background(), f.org, id); len(stored) != 1 {
		t.Fatalf("a held change was stored: %+v", stored)
	}
}

func TestSetServicesSaysWhatIsWrong(t *testing.T) {
	f := newFixture(t)
	f.create(t, f.org, "Portal", "portal")
	f.governed(false, "")
	cs := f.connect(t, projectmcp.Options{}, permission.ProjectsRead, permission.ProjectsWrite)
	cases := map[string]struct {
		items []any
		want  string
	}{
		"the cluster cannot run several": {items: monorepo, want: project.CodeServicesUnavailable},
		"a bad name":                     {items: []any{map[string]any{"name": "Web App", "route": "/"}}, want: "list_services"},
		"two parts at one route": {items: []any{map[string]any{"name": "a", "route": "/"}, map[string]any{"name": "b", "route": "/"}},
			want: "list_services"},
		"nothing": {items: []any{}, want: "list_services"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res := call(t, cs, "set_services", map[string]any{"app": "portal", "items": c.items}, nil)
			if !res.IsError || !strings.Contains(text(res), c.want) {
				t.Fatalf("got %+v", res.Content)
			}
		})
	}
	if res := call(t, cs, "list_services", map[string]any{"app": "nadie"}, nil); !res.IsError || !strings.Contains(text(res), "list_apps") {
		t.Fatalf("unknown app: %+v", res.Content)
	}
}
