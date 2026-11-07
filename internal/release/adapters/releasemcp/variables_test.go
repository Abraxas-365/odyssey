package releasemcp_test

import (
	"net/http"
	"net/http/httptest"

	"context"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/project"
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

// scopedCommands answers set_env as its fn says (warnings, a refusal).
type scopedCommands struct {
	*commands
	setEnv func(release.SetEnv) (release.SettingsChange, error)
}

func (c scopedCommands) SetEnv(ctx context.Context, org identity.OrganizationID, p project.ID, env project.EnvironmentID, in release.SetEnv) (release.SettingsChange, error) {
	if c.setEnv == nil {
		return c.commands.SetEnv(ctx, org, p, env, in)
	}
	c.commands.env = append(c.commands.env, in)
	return c.setEnv(in)
}

func (f *fixture) connectWith(t *testing.T, cmds release.Commands, perms ...string) *mcp.ClientSession {
	t.Helper()
	reg, err := mcpx.NewRegistry(releasemcp.Tools(f.projects, cmds, f.releases, f.builds, f.options))
	if err != nil {
		t.Fatal(err)
	}
	h := mcpx.NewHandler(reg, mcpx.Options{})
	p := mcpx.Principal{Organization: f.org, User: user, Client: "https://claude.ai/cimd", Permissions: perms}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestSetEnvScopesToServices(t *testing.T) {
	f := newFixture(t)
	cmds := scopedCommands{commands: f.commands}
	cs := f.connectWith(t, cmds, accessWriter...)
	// whole app when services is omitted
	if _, res := call[releasemcp.SetEnvOut](t, cs, "set_env", map[string]any{"app": "cotizador", "set": map[string]string{"A": "1"}}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.env; len(got) != 1 || !got[0].Services.Whole() {
		t.Fatalf("omitted services must be the whole app: %+v", got)
	}
	// named services and the warnings come through
	cmds.setEnv = func(in release.SetEnv) (release.SettingsChange, error) {
		s := release.DefaultSettings(f.org, f.env)
		s.ServiceEnv = map[string]map[string]string{"api": {"LOG_LEVEL": in.Set["LOG_LEVEL"]}, "web": {"LOG_LEVEL": in.Set["LOG_LEVEL"], "B": "2"}}
		return release.SettingsChange{Settings: s, Warnings: []string{"LOG_LEVEL de api gana a la de toda la app."}}, nil
	}
	cs = f.connectWith(t, cmds, accessWriter...)
	out, res := call[releasemcp.SetEnvOut](t, cs, "set_env", map[string]any{"app": "cotizador", "set": map[string]string{"LOG_LEVEL": "debug"}, "services": []string{"api", "web"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.env; len(got) != 2 || !slices.Equal(got[1].Services, release.Scope{"api", "web"}) {
		t.Fatalf("services not passed: %+v", got)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "gana") {
		t.Fatalf("warnings %+v", out.Warnings)
	}
	// a part's own variables are shown by part, and the whole app's list is
	// empty, never null
	if out.Variables == nil || len(out.Variables) != 0 || !slices.Equal(out.ServiceVariables["api"], []string{"LOG_LEVEL"}) ||
		!slices.Equal(out.ServiceVariables["web"], []string{"B", "LOG_LEVEL"}) {
		t.Fatalf("variables %+v by part %+v", out.Variables, out.ServiceVariables)
	}
}

func TestScopeUnavailableGivesNextStep(t *testing.T) {
	f := newFixture(t)
	cmds := scopedCommands{commands: f.commands, setEnv: func(release.SetEnv) (release.SettingsChange, error) {
		return release.SettingsChange{}, release.ErrServiceSettingsUnavailable()
	}}
	f.commands.requestFn = func(release.RequestSecret) (release.SecretView, error) {
		return release.SecretView{}, release.ErrServiceSettingsUnavailable()
	}
	cs := f.connectWith(t, cmds, accessWriter...)
	for name, args := range map[string]map[string]any{
		"set_env":        {"app": "cotizador", "set": map[string]string{"A": "1"}, "services": []string{"api"}},
		"request_secret": {"app": "cotizador", "name": "STRIPE_API_KEY", "services": []string{"api"}},
	} {
		_, res := call[releasemcp.SetEnvOut](t, cs, name, args)
		if !res.IsError || !strings.Contains(text(res), release.CodeServiceSettingsUnavailable) || !strings.Contains(text(res), "sin services") {
			t.Fatalf("%s: %+v", name, text(res))
		}
	}
}

func TestRequestSecretScope(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, f.org, accessWriter...)
	// omitted keeps the current scope
	if _, res := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "STRIPE_API_KEY"}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.requests; len(got) != 1 || got[0].Services != nil {
		t.Fatalf("omitted services must be nil: %+v", got)
	}
	// given: passed, and the view's scope is reported
	f.commands.requestFn = func(in release.RequestSecret) (release.SecretView, error) {
		return release.SecretView{SecretRef: release.SecretRef{Name: in.Name, Source: release.SecretManaged, Services: *in.Services}, State: release.SecretStateMissing}, nil
	}
	out, res := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "STRIPE_API_KEY", "services": []string{"api"}})
	if res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.requests; len(got) != 2 || got[1].Services == nil || !slices.Equal(*got[1].Services, release.Scope{"api"}) {
		t.Fatalf("services not passed: %+v", got)
	}
	if !slices.Equal(out.Services, []string{"api"}) {
		t.Fatalf("out %+v", out)
	}
	// an empty list means the whole app (not "keep")
	if _, res := call[releasemcp.RequestSecretOut](t, cs, "request_secret", map[string]any{"app": "cotizador", "name": "STRIPE_API_KEY", "services": []string{}}); res.IsError {
		t.Fatalf("%+v", res.Content)
	}
	if got := f.commands.requests; len(got) != 3 || got[2].Services == nil || len(*got[2].Services) != 0 {
		t.Fatalf("empty services must be &[]: %+v", got[2])
	}
}
