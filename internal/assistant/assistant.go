// Package assistant is what the console shows to connect an AI assistant to
// Odicea ("Conecta tu asistente"): the install commands and one-click links
// per client, the mapping from the OAuth client a request came through to the
// assistant's name, and what an assistant may do with a given set of
// permissions, in plain Spanish. It is foundation: pure, no I/O, no IAMKit.
package assistant

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
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
	Cursor      = "cursor"
	VSCode      = "vscode"
	Antigravity = "antigravity"
	// Other is an assistant admitted by IAMKit that Odicea has no preset for.
	Other = "other"
)

// Keys lists the supported assistants in display order.
func Keys() []string { return []string{ClaudeCode, Codex, Cursor, VSCode, Antigravity} }

// Names are the assistants as people know them, by key.
var Names = map[string]string{
	ClaudeCode: "Claude Code", Codex: "Codex", Cursor: "Cursor", VSCode: "VS Code", Antigravity: "Antigravity", Other: "Otro asistente",
}

// metadataClients are the presets that identify themselves with a client
// metadata document: the document's URL is the OAuth client id.
var metadataClients = map[string]string{
	"https://claude.ai/oauth/claude-code-client-metadata": ClaudeCode,
	"https://chatgpt.com/oauth/codex/client.json":         Codex,
	"https://vscode.dev/oauth/client-metadata.json":       VSCode,
}

// Templates are the presets with no metadata document and no dynamic
// registration: IAMKit registers a client for each (a "client template") and
// its config must carry that client id (IAMKIT_MCP_CLIENT_<KEY>_ID).
func Templates() []string { return []string{Cursor, Antigravity} }

// Registered maps a template preset's key to the OAuth client id IAMKit
// registered for it. A preset missing here cannot sign in, so it is not
// offered, and its requests are not told apart from Other.
type Registered map[string]string

// key names the template preset whose registered client is id ("" if none).
func (r Registered) key(id string) string {
	for _, k := range Templates() {
		if v := strings.TrimSpace(r[k]); v != "" && v == id {
			return k
		}
	}
	return ""
}

func (r Registered) id(key string) string { return strings.TrimSpace(r[key]) }

// Clients are the OAuth client ids PresetOf knows, with their assistant's
// key: the metadata documents and the registered templates. Every other id
// is Other.
func Clients(registered Registered) map[string]string {
	out := make(map[string]string, len(metadataClients)+len(registered))
	for id, key := range metadataClients {
		out[id] = key
	}
	for _, key := range Templates() {
		if id := registered.id(key); id != "" {
			out[id] = key
		}
	}
	return out
}

// PresetOf names the assistant behind an OAuth client id: a metadata
// document's URL, a registered template's client, else Other.
func PresetOf(clientID string, registered Registered) string {
	if key, known := metadataClients[clientID]; known {
		return key
	}
	if key := registered.key(clientID); key != "" {
		return key
	}
	return Other
}

// Preset names the assistant behind an event's client ("mcp:<client id>"):
// ok is false for any other client (the console, none).
func Preset(client string, registered Registered) (key string, ok bool) {
	id, found := strings.CutPrefix(client, MCPClientPrefix)
	if !found {
		return "", false
	}
	return PresetOf(id, registered), true
}

// Client is one assistant's connection steps.
type Client struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Install registers the MCP server (a command, or the JSON to add to the
	// client's MCP config).
	Install string `json:"install"`
	// InstallLink, when the client has one, registers the server in one
	// click (a deeplink the client handles); Install stays the fallback.
	InstallLink string `json:"install_link,omitempty"`
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
// skipped, repeats collapse). A template preset (Templates) is offered only
// when registered knows its client id, because its config must carry it.
func Build(enabled bool, mcpURL, skillURL, skillVersion string, registered Registered, keys []string) Info {
	info := Info{MCPURL: mcpURL, Skill: Skill{URL: skillURL, Version: skillVersion, Name: SkillName}, Clients: []Client{}}
	if !enabled {
		return info
	}
	info.Available = true
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if c, ok := client(k, mcpURL, skillURL, registered); ok && !seen[k] {
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

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func client(key, mcpURL, skillURL string, registered Registered) (Client, bool) {
	switch key {
	case ClaudeCode:
		return Client{Key: key, Name: Names[key],
			Install:      "claude mcp add --transport http " + ServerName + " " + mcpURL,
			SkillInstall: skillInto("~/.claude/skills", skillURL),
			Notes:        "Abre Claude Code, escribe /mcp, elige odicea y entra con la cuenta de tu empresa en el navegador."}, true
	case Codex:
		return Client{Key: key, Name: Names[key],
			Install:      "codex mcp add " + ServerName + " --url " + mcpURL,
			SkillInstall: skillInto("~/.agents/skills", skillURL),
			Notes:        "Después ejecuta: codex mcp login odicea. Se abrirá el navegador para entrar con la cuenta de tu empresa."}, true
	case VSCode:
		server := mustJSON(map[string]string{"name": ServerName, "type": "http", "url": mcpURL})
		return Client{Key: key, Name: Names[key],
			Install: `code --add-mcp '` + server + `'`,
			// VS Code's install URL: the server's JSON, URL-encoded.
			InstallLink:  "vscode:mcp/install?" + url.QueryEscape(server),
			SkillInstall: skillInto("~/.copilot/skills", skillURL),
			Notes:        "En VS Code abre la lista de servidores MCP (Ctrl/Cmd+Shift+P → «MCP: List Servers»), inicia odicea y entra con la cuenta de tu empresa."}, true
	case Cursor:
		id := registered.id(key)
		if id == "" {
			return Client{}, false
		}
		server := map[string]any{"url": mcpURL, "auth": map[string]string{"CLIENT_ID": id}}
		// Cursor's install link: the server's config, JSON then base64.
		link := "cursor://anysphere.cursor-deeplink/mcp/install?name=" + ServerName + "&config=" +
			url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(mustJSON(server))))
		return Client{Key: key, Name: Names[key],
			Install:      mustJSON(map[string]any{"mcpServers": map[string]any{ServerName: server}}),
			InstallLink:  link,
			SkillInstall: skillInto("~/.cursor/skills", skillURL),
			Notes: "Con el botón, Cursor te pide confirmar. Sin él, agrega esto a ~/.cursor/mcp.json. Luego en Cursor Settings → MCP " +
				"pulsa Connect junto a «odicea» y entra con la cuenta de tu empresa en el navegador."}, true
	case Antigravity:
		id := registered.id(key)
		if id == "" {
			return Client{}, false
		}
		return Client{Key: key, Name: Names[key],
			Install: mustJSON(map[string]any{"mcpServers": map[string]any{ServerName: map[string]any{
				"serverUrl": mcpURL, "oauth": map[string]string{"clientId": id}}}}),
			SkillInstall: skillInto("~/.gemini/config/skills", skillURL),
			Notes: "Agrega esto a ~/.gemini/config/mcp_config.json y reinicia Antigravity. Luego: Settings (Cmd+,) → Customizations → " +
				"pulsa Authenticate junto a «odicea» → entra con la cuenta de tu empresa en el navegador → copia el código que te muestra, " +
				"pégalo ahí y pulsa Submit."}, true
	}
	return Client{}, false
}

// Capability is one thing an assistant can do on Odicea, as the MCP tools
// that do it (by name): with the permissions of every one of them, the user's
// assistant can.
type Capability struct {
	// Can is the sentence, in plain Spanish ("Ver tus apps y su estado").
	Can   string
	Tools []string
}

// Capabilities word every MCP tool (TestEveryMCPToolHasACapability holds them
// to it: a new tool without a sentence fails), in the order the console
// lists them.
var Capabilities = []Capability{
	{Can: "Ver tus apps, su estado y lo que necesita atención", Tools: []string{"whoami", "list_apps", "get_app", "get_attention"}},
	{Can: "Ver las versiones publicadas y sus registros de construcción", Tools: []string{"list_versions", "get_deployment", "get_build_logs"}},
	{Can: "Ver los datos que usan tus apps y tus solicitudes", Tools: []string{"list_resources", "list_requests", "get_request"}},
	{Can: "Crear apps y cambiar su configuración, acceso y secretos", Tools: []string{"create_app", "set_env", "set_access", "request_secret"}},
	{Can: "Publicar versiones nuevas", Tools: []string{"deploy_folder", "deploy_image"}},
	{Can: "Volver a una versión anterior", Tools: []string{"rollback"}},
	{Can: "Conectar repositorios de GitHub y publicar desde ellos", Tools: []string{"list_repositories", "connect_repository", "publish_now"}},
	{Can: "Crear bases de datos y activar IA en tus apps", Tools: []string{"create_database", "enable_ai"}},
}

// Abilities is what an assistant may and may not do with a set of
// permissions, in plain Spanish (UX v4 §5.7): Can and Cannot are disjoint and
// in the order of Capabilities. Tools are the MCP tools it may call (the
// console shows them in technical mode), sorted.
type Abilities struct {
	Can    []string `json:"can"`
	Cannot []string `json:"cannot"`
	Tools  []string `json:"tools"`
}

// AbilitiesFor words Capabilities against the permissions held. tools are
// the permissions each MCP tool requires, by name; a capability none of
// whose tools is served here (GitHub off) is left out.
func AbilitiesFor(tools map[string][]string, held []string) Abilities {
	out := Abilities{Can: []string{}, Cannot: []string{}, Tools: []string{}}
	for name, perms := range tools {
		if !slices.ContainsFunc(perms, func(p string) bool { return !slices.Contains(held, p) }) {
			out.Tools = append(out.Tools, name)
		}
	}
	slices.Sort(out.Tools)
	for _, c := range Capabilities {
		served, allowed := false, true
		for _, name := range c.Tools {
			perms, ok := tools[name]
			if !ok {
				continue
			}
			served = true
			for _, p := range perms {
				if !slices.Contains(held, p) {
					allowed = false
				}
			}
		}
		switch {
		case !served:
		case allowed:
			out.Can = append(out.Can, c.Can)
		default:
			out.Cannot = append(out.Cannot, c.Can)
		}
	}
	return out
}
