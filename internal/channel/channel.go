// Package channel is where an action came from, in the words the console
// shows ("Consola", "GitHub", "Automático", "Claude Code"): the one mapping
// from an event's client (event.Meta.Client) to a channel, which the
// deployments (release.Deployment.RequestedVia) and the activity trail (the
// audit entries' channel and its filter) share. Nothing else spells a
// channel (architecture test). It is foundation: pure, no I/O.
package channel

import (
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/event"
)

// Channels. An assistant's is AssistantPrefix + its key (assistant.Keys, or
// assistant.Other).
const (
	Console  = "console"  // someone in the console (or the REST API)
	GitHub   = "github"   // a push nobody in Odicea asked for (the webhook, the poll)
	Platform = "platform" // the platform on its own (the automatic restore, a schedule, a cluster's agent)
	// AssistantPrefix + a key: an AI assistant through the MCP endpoint.
	AssistantPrefix = "assistant:"
	// Assistant, as a filter, is every assistant.
	Assistant = "assistant"
	// Unrecorded is a filter for the entries recorded before the channel was
	// (their client is empty); as a channel it is "".
	Unrecorded = "unrecorded"
)

// Of names the channel of an event's client. Empty stays empty: it was not
// recorded, and the console says so instead of guessing. Any other client
// (the console's OAuth client, ClientAPI) is the console.
func Of(client string, registered assistant.Registered) string {
	switch client {
	case "":
		return ""
	case event.ClientGitHub:
		return GitHub
	case event.ClientPlatform:
		return Platform
	}
	if key, ok := assistant.Preset(client, registered); ok {
		return AssistantPrefix + key
	}
	return Console
}

// Match is the set of event clients a channel filter selects: any of
// Clients or starting with Prefix, and none of Except nor starting with
// ExceptPrefix. Zero fields do not narrow.
type Match struct {
	Clients      []string
	Prefix       string
	Except       []string
	ExceptPrefix string
}

// Filter turns a channel filter (a channel, Assistant or Unrecorded) into the
// clients it selects; ok is false for a value that is none of them.
func Filter(ch string, registered assistant.Registered) (m Match, ok bool) {
	switch ch {
	case Console:
		return Match{Except: []string{"", event.ClientGitHub, event.ClientPlatform}, ExceptPrefix: assistant.MCPClientPrefix}, true
	case GitHub:
		return Match{Clients: []string{event.ClientGitHub}}, true
	case Platform:
		return Match{Clients: []string{event.ClientPlatform}}, true
	case Unrecorded:
		return Match{Clients: []string{""}}, true
	case Assistant:
		return Match{Prefix: assistant.MCPClientPrefix}, true
	}
	key, found := strings.CutPrefix(ch, AssistantPrefix)
	if !found {
		return Match{}, false
	}
	known := assistant.Clients(registered)
	if key == assistant.Other {
		except := make([]string, 0, len(known))
		for id := range known {
			except = append(except, assistant.MCPClientPrefix+id)
		}
		slices.Sort(except)
		return Match{Prefix: assistant.MCPClientPrefix, Except: except}, true
	}
	var clients []string
	for id, k := range known {
		if k == key {
			clients = append(clients, assistant.MCPClientPrefix+id)
		}
	}
	slices.Sort(clients)
	if clients == nil {
		if _, valid := assistant.Names[key]; !valid {
			return Match{}, false
		}
		clients = []string{} // a supported assistant with no client here: matches nothing
	}
	return Match{Clients: clients}, true
}

// Matches reports whether client is one m selects (the repository's SQL
// says the same; tests hold them together).
func (m Match) Matches(client string) bool {
	if m.Clients != nil && !slices.Contains(m.Clients, client) {
		return false
	}
	if m.Prefix != "" && !strings.HasPrefix(client, m.Prefix) {
		return false
	}
	if slices.Contains(m.Except, client) {
		return false
	}
	return m.ExceptPrefix == "" || !strings.HasPrefix(client, m.ExceptPrefix)
}
