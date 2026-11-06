package assistant_test

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/assistant"
)

func TestPreset(t *testing.T) {
	const agy, cur = "agy-client-id", "cursor-client-id"
	registered := assistant.Registered{assistant.Antigravity: agy, assistant.Cursor: " " + cur + " "}
	for client, want := range map[string]struct {
		key string
		ok  bool
	}{
		"mcp:https://claude.ai/oauth/claude-code-client-metadata": {assistant.ClaudeCode, true},
		"mcp:https://chatgpt.com/oauth/codex/client.json":         {assistant.Codex, true},
		"mcp:https://vscode.dev/oauth/client-metadata.json":       {assistant.VSCode, true},
		"mcp:" + agy:                  {assistant.Antigravity, true},
		"mcp:" + cur:                  {assistant.Cursor, true},
		"mcp:something-else":          {assistant.Other, true},
		"mcp:":                        {assistant.Other, true},
		"web-console-client":          {"", false},
		"":                            {"", false},
		"https://claude.ai/oauth/x":   {"", false},
		"mcp:https://claude.ai/other": {assistant.Other, true},
	} {
		if key, ok := assistant.Preset(client, registered); key != want.key || ok != want.ok {
			t.Errorf("Preset(%q) = %q, %v; want %q, %v", client, key, ok, want.key, want.ok)
		}
	}
	// Without a registered id nothing matches a template, not even "".
	if key, _ := assistant.Preset("mcp:", nil); key != assistant.Other {
		t.Errorf("empty id matched %q", key)
	}
	if key, _ := assistant.Preset("mcp:", assistant.Registered{assistant.Cursor: ""}); key != assistant.Other {
		t.Errorf("empty cursor id matched %q", key)
	}
	for _, k := range append(assistant.Keys(), assistant.Other) {
		if assistant.Names[k] == "" {
			t.Errorf("%s has no name", k)
		}
	}
}

func TestBuild(t *testing.T) {
	reg := assistant.Registered{assistant.Antigravity: "agy-id", assistant.Cursor: "cur-id"}
	off := assistant.Build(false, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", reg, assistant.Keys())
	if off.Available || len(off.Clients) != 0 || off.Clients == nil || off.MCPURL != "https://x/mcp" {
		t.Fatalf("disabled: %+v", off)
	}
	on := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", reg, []string{"codex", " Claude_Code ", "nope", "codex", "antigravity", "vscode", "cursor"})
	var keys []string
	for _, c := range on.Clients {
		keys = append(keys, c.Key)
		if !strings.Contains(c.Install, "https://x/mcp") || !strings.Contains(c.SkillInstall, "https://x/skills/odicea.tar.gz") ||
			c.Name == "" || c.Notes == "" || c.Name != assistant.Names[c.Key] {
			t.Errorf("incomplete client %+v", c)
		}
	}
	if !on.Available || !slices.Equal(keys, []string{"codex", "claude_code", "antigravity", "vscode", "cursor"}) || on.Skill.Name != "odicea" || on.Skill.Version != "abc" {
		t.Fatalf("enabled: %+v", on)
	}
	if got := assistant.Build(true, "u", "s", "v", nil, nil); !got.Available || len(got.Clients) != 0 {
		t.Fatalf("no keys: %+v", got)
	}
}

func TestTemplatesNeedTheirClientID(t *testing.T) {
	keys := []string{"claude_code", "antigravity", "cursor"}
	// without the id IAMKit registered for it, a template cannot sign in: not offered
	without := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", nil, keys)
	if len(without.Clients) != 1 || without.Clients[0].Key != "claude_code" {
		t.Fatalf("without the ids: %+v", without.Clients)
	}
	with := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", assistant.Registered{"antigravity": " agy-id ", "cursor": "cur-id"}, keys)
	if len(with.Clients) != 3 {
		t.Fatalf("with the ids: %+v", with.Clients)
	}
	agy := with.Clients[1]
	var cfg struct {
		MCPServers map[string]struct {
			ServerURL string `json:"serverUrl"`
			OAuth     struct {
				ClientID string `json:"clientId"`
			} `json:"oauth"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(agy.Install), &cfg); err != nil {
		t.Fatalf("snippet is not JSON: %v\n%s", err, agy.Install)
	}
	if s := cfg.MCPServers["odicea"]; s.ServerURL != "https://x/mcp" || s.OAuth.ClientID != "agy-id" {
		t.Fatalf("snippet %+v", cfg)
	}
	for _, want := range []string{"Settings (Cmd+,)", "Customizations", "Authenticate", "odicea", "Submit", "mcp_config.json"} {
		if !strings.Contains(agy.Notes, want) {
			t.Errorf("notes lack %q: %s", want, agy.Notes)
		}
	}
	if agy.InstallLink != "" {
		t.Errorf("antigravity has no install link: %q", agy.InstallLink)
	}
}

// cursorServer is Cursor's mcp.json entry.
type cursorServer struct {
	URL  string `json:"url"`
	Auth struct {
		ClientID string `json:"CLIENT_ID"`
	} `json:"auth"`
}

func TestInstallLinks(t *testing.T) {
	info := assistant.Build(true, "https://odicea.example.com/mcp", "https://x/s.tar.gz", "v", assistant.Registered{"cursor": "cur-id"}, assistant.Keys())
	byKey := map[string]assistant.Client{}
	for _, c := range info.Clients {
		byKey[c.Key] = c
	}
	// Claude Code and Codex have no install link: their command is the way.
	for _, k := range []string{"claude_code", "codex"} {
		if byKey[k].InstallLink != "" {
			t.Errorf("%s: unexpected link %q", k, byKey[k].InstallLink)
		}
	}

	// VS Code: vscode:mcp/install?<url-encoded server JSON>
	vs := byKey["vscode"].InstallLink
	raw, ok := strings.CutPrefix(vs, "vscode:mcp/install?")
	if !ok {
		t.Fatalf("vscode link %q", vs)
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	var server struct{ Name, Type, URL string }
	if err := json.Unmarshal([]byte(decoded), &server); err != nil || server.Name != "odicea" || server.Type != "http" || server.URL != "https://odicea.example.com/mcp" {
		t.Fatalf("vscode server %+v %v", server, err)
	}

	// Cursor: cursor://anysphere.cursor-deeplink/mcp/install?name=odicea&config=<base64 JSON>
	cur := byKey["cursor"]
	u, err := url.Parse(cur.InstallLink)
	if err != nil || u.Scheme != "cursor" || u.Host != "anysphere.cursor-deeplink" || u.Path != "/mcp/install" || u.Query().Get("name") != "odicea" {
		t.Fatalf("cursor link %q %v", cur.InstallLink, err)
	}
	cfg, err := base64.StdEncoding.DecodeString(u.Query().Get("config"))
	if err != nil {
		t.Fatal(err)
	}
	var cs cursorServer
	if err := json.Unmarshal(cfg, &cs); err != nil || cs.URL != "https://odicea.example.com/mcp" || cs.Auth.ClientID != "cur-id" {
		t.Fatalf("cursor config %s %v", cfg, err)
	}
	// and the manual fallback is the same server under mcpServers.odicea
	var manual struct {
		MCPServers map[string]cursorServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cur.Install), &manual); err != nil || manual.MCPServers["odicea"] != cs {
		t.Fatalf("cursor install %s %v", cur.Install, err)
	}
	if !strings.Contains(cur.Notes, "~/.cursor/mcp.json") || !strings.Contains(cur.SkillInstall, "~/.cursor/skills") {
		t.Errorf("cursor notes/skill: %+v", cur)
	}
}

func TestAbilitiesFor(t *testing.T) {
	const read, write, deploy = "p:read", "p:write", "d:create"
	tools := map[string][]string{
		"list_apps": {read}, "get_app": {read}, "create_app": {write}, "set_env": {write, read},
		"deploy_folder": {deploy, read}, "deploy_image": {deploy, read},
	}
	got := assistant.AbilitiesFor(tools, []string{read})
	want := assistant.Abilities{
		Can:    []string{assistant.Capabilities[0].Can},
		Cannot: []string{assistant.Capabilities[3].Can, assistant.Capabilities[4].Can},
	}
	if !slices.Equal(got.Can, want.Can) || !slices.Equal(got.Cannot, want.Cannot) || !slices.Equal(got.Tools, []string{"get_app", "list_apps"}) {
		t.Fatalf("reader: %+v", got)
	}
	// a capability with one of its tools denied is not offered: "Crear apps y
	// cambiar su configuración" needs set_env too
	got = assistant.AbilitiesFor(tools, []string{write})
	if slices.Contains(got.Can, assistant.Capabilities[3].Can) {
		t.Fatalf("write without read: %+v", got)
	}
	// nothing served, nothing said (never an empty "no podrá")
	if got := assistant.AbilitiesFor(nil, []string{read}); got.Can == nil || got.Cannot == nil || got.Tools == nil || len(got.Can)+len(got.Cannot)+len(got.Tools) != 0 {
		t.Fatalf("no tools: %+v", got)
	}
	// every tool belongs to exactly one capability
	seen := map[string]bool{}
	for _, c := range assistant.Capabilities {
		if c.Can == "" || len(c.Tools) == 0 {
			t.Errorf("empty capability %+v", c)
		}
		for _, name := range c.Tools {
			if seen[name] {
				t.Errorf("%s in two capabilities", name)
			}
			seen[name] = true
		}
	}
}
