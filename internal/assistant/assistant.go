// Package assistant is what the console shows to connect an AI assistant to
// Odicea ("Conecta tu asistente"): the install commands per client, and the
// mapping from the OAuth client a request came through to the assistant's
// name. It is foundation: pure, no I/O, no IAMKit.
package assistant

import (
	"encoding/json"
	"strings"
)

// ServerName is the name the assistant registers Odicea's MCP server under.
const ServerName = "odicea"

// SkillName is the skill's name (skills.Name).
const SkillName = "odicea"

// MCPClientPrefix is how the MCP endpoint marks the events of an assistant
// (event.Meta.Client = "mcp:<oauth client id>").
const MCPClientPrefix = "mcp:"

// Keys of the supported assistants (ODYSSEY_MCP_CLIENTS).
const (
	ClaudeCode  = "claude_code"
	Codex       = "codex"
	VSCode      = "vscode"
	Antigravity = "antigravity"
	// Other is an assistant admitted by IAMKit that Odicea has no preset for.
	Other = "other"
)

// Keys lists the supported assistants in display order.
func Keys() []string { return []string{ClaudeCode, Codex, VSCode, Antigravity} }

// metadataClients are the presets that identify themselves with a client
// metadata document: the document's URL is the OAuth client id.
var metadataClients = map[string]string{
	"https://claude.ai/oauth/claude-code-client-metadata": ClaudeCode,
	"https://chatgpt.com/oauth/codex/client.json":         Codex,
	"https://vscode.dev/oauth/client-metadata.json":       VSCode,
}

// Preset names the assistant behind an event's client ("mcp:<client id>"):
// ok is false for any other client (the console, none). Antigravity has no
// metadata document; IAMKit registered it a client, whose id is
// antigravityID. An MCP client that matches no preset is Other.
func Preset(client, antigravityID string) (key string, ok bool) {
	id, found := strings.CutPrefix(client, MCPClientPrefix)
	if !found {
		return "", false
	}
	if key, known := metadataClients[id]; known {
		return key, true
	}
	if antigravityID != "" && id == antigravityID {
		return Antigravity, true
	}
	return Other, true
}

// Client is one assistant's connection steps.
type Client struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Install registers the MCP server (a command, or the JSON to add to the
	// client's MCP config).
	Install string `json:"install"`
	// SkillInstall downloads the odicea skill into the client's skills folder.
	SkillInstall string `json:"skill_install"`
	// Notes is how to sign in and where the config lives, in plain Spanish.
	Notes string `json:"notes"`
}

// Skill is the downloadable skill.
type Skill struct {
	URL     string `json:"url"`
	Version string `json:"version"`
	Name    string `json:"name"`
}

// Info is GET /api/v1/assistant.
type Info struct {
	Available bool     `json:"available"`
	MCPURL    string   `json:"mcp_url"`
	Skill     Skill    `json:"skill"`
	Clients   []Client `json:"clients"`
}

// Build assembles the answer. enabled is whether /mcp is served; without it
// nothing is offered. keys picks and orders the clients (unknown keys are
// skipped, repeats collapse). Antigravity has no dynamic client registration:
// it is offered only when antigravityID (the client IAMKit registered for it)
// is known, because its config must carry that id.
func Build(enabled bool, mcpURL, skillURL, skillVersion, antigravityID string, keys []string) Info {
	info := Info{MCPURL: mcpURL, Skill: Skill{URL: skillURL, Version: skillVersion, Name: SkillName}, Clients: []Client{}}
	if !enabled {
		return info
	}
	info.Available = true
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if c, ok := client(k, mcpURL, skillURL, strings.TrimSpace(antigravityID)); ok && !seen[k] {
			seen[k] = true
			info.Clients = append(info.Clients, c)
		}
	}
	return info
}

// skillInto downloads and unpacks the skill (its archive has one top
// directory, odicea/) into dir.
func skillInto(dir, skillURL string) string {
	return "mkdir -p " + dir + " && curl -fsSL " + skillURL + " | tar -xz -C " + dir
}

func client(key, mcpURL, skillURL, antigravityID string) (Client, bool) {
	switch key {
	case ClaudeCode:
		return Client{Key: key, Name: "Claude Code",
			Install:      "claude mcp add --transport http " + ServerName + " " + mcpURL,
			SkillInstall: skillInto("~/.claude/skills", skillURL),
			Notes:        "Abre Claude Code, escribe /mcp, elige odicea y entra con la cuenta de tu empresa en el navegador."}, true
	case Codex:
		return Client{Key: key, Name: "Codex",
			Install:      "codex mcp add " + ServerName + " --url " + mcpURL,
			SkillInstall: skillInto("~/.agents/skills", skillURL),
			Notes:        "Después ejecuta: codex mcp login odicea. Se abrirá el navegador para entrar con la cuenta de tu empresa."}, true
	case VSCode:
		return Client{Key: key, Name: "VS Code",
			Install:      `code --add-mcp '{"name":"` + ServerName + `","type":"http","url":"` + mcpURL + `"}'`,
			SkillInstall: skillInto("~/.copilot/skills", skillURL),
			Notes:        "En VS Code abre la lista de servidores MCP (Ctrl/Cmd+Shift+P → «MCP: List Servers»), inicia odicea y entra con la cuenta de tu empresa."}, true
	case Antigravity:
		if antigravityID == "" {
			return Client{}, false
		}
		server, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{ServerName: map[string]any{
			"serverUrl": mcpURL, "oauth": map[string]string{"clientId": antigravityID}}}})
		return Client{Key: key, Name: "Antigravity",
			Install:      string(server),
			SkillInstall: skillInto("~/.gemini/config/skills", skillURL),
			Notes: "Agrega esto a ~/.gemini/config/mcp_config.json y reinicia Antigravity. Luego: Settings (Cmd+,) → Customizations → " +
				"pulsa Authenticate junto a «odicea» → entra con la cuenta de tu empresa en el navegador → copia el código que te muestra, " +
				"pégalo ahí y pulsa Submit."}, true
	}
	return Client{}, false
}
