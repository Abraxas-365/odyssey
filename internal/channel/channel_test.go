package channel_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/channel"
)

const (
	claude = "mcp:https://claude.ai/oauth/claude-code-client-metadata"
	codex  = "mcp:https://chatgpt.com/oauth/codex/client.json"
)

var registered = assistant.Registered{assistant.Cursor: "cursor-client"}

func TestOf(t *testing.T) {
	for client, want := range map[string]string{
		"":                  "",
		"github":            "github",
		"platform":          "platform",
		"api":               "console",
		"web-client":        "console",
		claude:              "assistant:claude_code",
		codex:               "assistant:codex",
		"mcp:cursor-client": "assistant:cursor",
		"mcp:someone-else":  "assistant:other",
	} {
		if got := channel.Of(client, registered); got != want {
			t.Errorf("Of(%q) = %q, want %q", client, got, want)
		}
	}
}

// TestFilterAgreesWithOf: a filter selects exactly the clients whose channel
// it names, so the feed's filter and its chips never disagree.
func TestFilterAgreesWithOf(t *testing.T) {
	clients := []string{"", "github", "platform", "api", "web-client", claude, codex, "mcp:cursor-client", "mcp:someone-else"}
	filters := map[string]func(ch string) bool{
		"console":               func(ch string) bool { return ch == "console" },
		"github":                func(ch string) bool { return ch == "github" },
		"platform":              func(ch string) bool { return ch == "platform" },
		"unrecorded":            func(ch string) bool { return ch == "" },
		"assistant":             func(ch string) bool { return len(ch) > 10 && ch[:10] == "assistant:" },
		"assistant:claude_code": func(ch string) bool { return ch == "assistant:claude_code" },
		"assistant:cursor":      func(ch string) bool { return ch == "assistant:cursor" },
		"assistant:other":       func(ch string) bool { return ch == "assistant:other" },
		"assistant:antigravity": func(ch string) bool { return ch == "assistant:antigravity" }, // not registered here: nothing
	}
	for f, want := range filters {
		m, ok := channel.Filter(f, registered)
		if !ok {
			t.Fatalf("Filter(%q) rejected", f)
		}
		for _, c := range clients {
			if got := m.Matches(c); got != want(channel.Of(c, registered)) {
				t.Errorf("Filter(%q).Matches(%q) = %v, but its channel is %q", f, c, got, channel.Of(c, registered))
			}
		}
	}
}

func TestFilterRejectsUnknownChannels(t *testing.T) {
	for _, f := range []string{"", "web", "assistant:", "assistant:nope", "mcp:x"} {
		if _, ok := channel.Filter(f, registered); ok {
			t.Errorf("Filter(%q) must be rejected", f)
		}
	}
}
