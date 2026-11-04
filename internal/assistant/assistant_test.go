package assistant_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/assistant"
)

func TestPreset(t *testing.T) {
	const agy = "agy-client-id"
	for client, want := range map[string]struct {
		key string
		ok  bool
	}{
		"mcp:https://claude.ai/oauth/claude-code-client-metadata": {assistant.ClaudeCode, true},
		"mcp:https://chatgpt.com/oauth/codex/client.json":         {assistant.Codex, true},
		"mcp:https://vscode.dev/oauth/client-metadata.json":       {assistant.VSCode, true},
		"mcp:" + agy:                  {assistant.Antigravity, true},
		"mcp:something-else":          {assistant.Other, true},
		"mcp:":                        {assistant.Other, true},
		"web-console-client":          {"", false},
		"":                            {"", false},
		"https://claude.ai/oauth/x":   {"", false},
		"mcp:https://claude.ai/other": {assistant.Other, true},
	} {
		if key, ok := assistant.Preset(client, agy); key != want.key || ok != want.ok {
			t.Errorf("Preset(%q) = %q, %v; want %q, %v", client, key, ok, want.key, want.ok)
		}
	}
	// Without a configured Antigravity id nothing matches it, not even "".
	if key, _ := assistant.Preset("mcp:", ""); key != assistant.Other {
		t.Errorf("empty antigravity id matched %q", key)
	}
}

func TestBuild(t *testing.T) {
	off := assistant.Build(false, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", "agy-id", assistant.Keys())
	if off.Available || len(off.Clients) != 0 || off.Clients == nil || off.MCPURL != "https://x/mcp" {
		t.Fatalf("disabled: %+v", off)
	}
	on := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", "agy-id", []string{"codex", " Claude_Code ", "nope", "codex", "antigravity", "vscode"})
	var keys []string
	for _, c := range on.Clients {
		keys = append(keys, c.Key)
		if !strings.Contains(c.Install, "https://x/mcp") || !strings.Contains(c.SkillInstall, "https://x/skills/odicea.tar.gz") ||
			c.Name == "" || c.Notes == "" {
			t.Errorf("incomplete client %+v", c)
		}
	}
	if !on.Available || !slices.Equal(keys, []string{"codex", "claude_code", "antigravity", "vscode"}) || on.Skill.Name != "odicea" || on.Skill.Version != "abc" {
		t.Fatalf("enabled: %+v", on)
	}
	if got := assistant.Build(true, "u", "s", "v", "", nil); !got.Available || len(got.Clients) != 0 {
		t.Fatalf("no keys: %+v", got)
	}
}

func TestAntigravityNeedsItsClientID(t *testing.T) {
	keys := []string{"claude_code", "antigravity"}
	// without the id IAMKit registered for it, Antigravity cannot sign in: not offered
	without := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", "", keys)
	if len(without.Clients) != 1 || without.Clients[0].Key != "claude_code" {
		t.Fatalf("without the id: %+v", without.Clients)
	}
	with := assistant.Build(true, "https://x/mcp", "https://x/skills/odicea.tar.gz", "abc", " agy-id ", keys)
	if len(with.Clients) != 2 {
		t.Fatalf("with the id: %+v", with.Clients)
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
}
