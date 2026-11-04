package releasemcp_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasemcp"
)

var accessWriter = []string{permission.ProjectsRead, permission.ProjectsWrite}

func TestSetAccessIsListedOnlyWithBothPermissions(t *testing.T) {
	f := newFixture(t)
	has := func(perms ...string) bool {
		res, err := f.connect(t, f.org, perms...).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(res.Tools, func(x *mcp.Tool) bool { return x.Name == "set_access" })
	}
	if !has(accessWriter...) || has(permission.ProjectsWrite) || has(permission.ProjectsRead) || has(reader...) {
		t.Fatal("set_access must need projects:read and projects:write")
	}
}

func TestSetAccessChangesTheMode(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, accessWriter...)
	out, res := call[releasemcp.SetAccessOut](t, cs, "set_access", map[string]any{"app": "cotizador", "mode": " Organization "})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.access; len(got) != 1 || got[0].Mode != release.AccessOrganization || got[0].RequestedBy != user {
		t.Fatalf("command %+v", got)
	}
	if out.App != "cotizador" || out.Access.Mode != "organization" || out.Access.Pending || out.Version != nil || out.NextStep == "" {
		t.Fatalf("only stored: %+v", out)
	}

	// a config deployment on its way: the change is pending and the next step follows it
	f.commands.accessFn = func(in release.SetAccess) (release.AccessChange, error) {
		d := f.releases.deployments[1]
		return release.AccessChange{Settings: release.Settings{Revision: 2, Access: release.AccessSettings{Mode: in.Mode}}, Deployment: &d}, nil
	}
	out, _ = call[releasemcp.SetAccessOut](t, cs, "set_access", map[string]any{"app": "cotizador", "mode": "team"})
	if out.Version == nil || !out.Access.Pending || !strings.Contains(out.NextStep, "get_deployment") {
		t.Fatalf("applying: %+v", out)
	}
}

func TestSetAccessRefusals(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, accessWriter...)
	// public needs odyssey:access:public, which projects:write does not give
	_, res := call[releasemcp.SetAccessOut](t, cs, "set_access", map[string]any{"app": "cotizador", "mode": "public"})
	if !res.IsError || !strings.Contains(text(res), "TI") {
		t.Fatalf("public without its permission: %+v", res)
	}
	for _, args := range []map[string]any{
		{"app": "cotizador", "mode": "everyone"},
		{"app": "cotizador", "mode": ""},
		{"app": "no-existe", "mode": "team"},
		{"app": " ", "mode": "team"},
	} {
		if _, res := call[releasemcp.SetAccessOut](t, cs, "set_access", args); !res.IsError {
			t.Errorf("accepted %v", args)
		}
	}
	if len(f.commands.access) != 0 {
		t.Fatalf("a refused call reached the command: %+v", f.commands.access)
	}
	// with the permission, public goes through
	cs = f.connect(t, f.org, append([]string{permission.AccessPublic}, accessWriter...)...)
	if out, res := call[releasemcp.SetAccessOut](t, cs, "set_access", map[string]any{"app": "cotizador", "mode": "public"}); res.IsError || out.Access.Mode != "public" {
		t.Fatalf("public: %+v %+v", out, res)
	}
	// another organization's app is not found
	other := f.connect(t, newFixture(t).org, accessWriter...)
	if _, res := call[releasemcp.SetAccessOut](t, other, "set_access", map[string]any{"app": "cotizador", "mode": "team"}); !res.IsError {
		t.Fatal("another organization's app was changed")
	}
}

func TestGetAppShowsWhoCanOpenIt(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, reader...)
	out, _ := call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Access == nil || out.Access.Mode != "team" {
		t.Fatalf("defaults: %+v", out.Access)
	}
	// stored at revision 3 but the cluster was told none: pending
	rel := release.NewID()
	f.releases.release = &release.Release{ID: rel}
	f.releases.deployments[1].Release = &rel
	f.releases.current = &f.releases.deployments[1]
	f.releases.settings = &release.Settings{Environment: f.env, Organization: f.org, Revision: 3, Access: release.AccessSettings{Mode: release.AccessOrganization}}
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Access == nil || out.Access.Mode != "organization" || !out.Access.Pending {
		t.Fatalf("pending: %+v", out.Access)
	}
	// a deployment that applied revision 2 (even one that failed after) is not 3
	rev := int64(2)
	f.releases.appliedSettings = &rev
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Access == nil || !out.Access.Pending {
		t.Fatalf("applied 2 of 3: %+v", out.Access)
	}
	rev = 3
	out, _ = call[releasemcp.GetAppOut](t, cs, "get_app", map[string]any{"app": "cotizador"})
	if out.Access == nil || out.Access.Pending {
		t.Fatalf("applied: %+v", out.Access)
	}
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
