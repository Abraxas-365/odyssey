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

func TestVariableToolsNeedWrite(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"set_env", "request_secret"} {
		has := func(perms ...string) bool {
			res, err := f.connect(t, f.org, perms...).ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			return slices.ContainsFunc(res.Tools, func(x *mcp.Tool) bool { return x.Name == name })
		}
		if !has(accessWriter...) || has(permission.ProjectsRead) || has(reader...) {
			t.Fatalf("%s must need projects:read and projects:write", name)
		}
	}
	// no tool takes a secret's value
	res, err := f.connect(t, f.org, permission.All...).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if strings.Contains(tool.Name, "load") || strings.Contains(tool.Name, "secret_value") {
			t.Errorf("an assistant tool may take a secret value: %s", tool.Name)
		}
	}
}

func TestSetEnvChangesVariables(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, accessWriter...)
	out, res := call[releasemcp.SetEnvOut](t, cs, "set_env", map[string]any{"app": "cotizador", "set": map[string]string{"LOG_LEVEL": "debug"}, "unset": []string{"OLD"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.env; len(got) != 1 || got[0].Set["LOG_LEVEL"] != "debug" || got[0].Unset[0] != "OLD" || got[0].RequestedBy != user {
		t.Fatalf("command %+v", got)
	}
	if out.App != "cotizador" || !slices.Equal(out.Variables, []string{"LOG_LEVEL"}) || out.NextStep == "" {
		t.Fatalf("out %+v", out)
	}
	// something that looks like a secret is sent to request_secret, never stored
	_, res = call[releasemcp.SetEnvOut](t, cs, "set_env", map[string]any{"app": "cotizador", "set": map[string]string{"STRIPE_API_KEY": "sk_live"}})
	if !res.IsError || !strings.Contains(text(res), "request_secret") || len(f.commands.env) != 1 {
		t.Fatalf("secret-looking variable: %+v", res)
	}
}

func TestRequestSecretGivesTheConsoleLink(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, accessWriter...)
	out, res := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "STRIPE_API_KEY", "note": "pagos"})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if out.Status != "waiting_for_value" || out.ConsoleURL != "https://odicea.test/apps/"+f.app.String()+"?secret=STRIPE_API_KEY" ||
		!strings.Contains(out.NextStep, "console_url") {
		t.Fatalf("out %+v", out)
	}
	if got := f.commands.requests; len(got) != 1 || got[0].Name != "STRIPE_API_KEY" || got[0].Note != "pagos" || got[0].RequestedBy != user {
		t.Fatalf("command %+v", got)
	}
	// an existing secret reports its state
	f.commands.requestFn = func(in release.RequestSecret) (release.SecretView, error) {
		return release.SecretView{SecretRef: release.SecretRef{Name: in.Name, Source: release.SecretManaged, Version: 2}, State: release.SecretStateReload}, nil
	}
	if out, _ := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "STRIPE_API_KEY"}); out.Status != "reload" {
		t.Fatalf("reload: %+v", out)
	}
	// a bad name and another organization's app are refused
	f.commands.requestFn = nil
	if _, res := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "1BAD"}); !res.IsError {
		t.Fatal("bad name accepted")
	}
	other := f.connect(t, newFixture(t).org, accessWriter...)
	if _, res := call[releasemcp.RequestSecretOut](t, other, "request_secret", map[string]any{"app": "cotizador", "name": "X"}); !res.IsError {
		t.Fatal("another organization's app")
	}
}
