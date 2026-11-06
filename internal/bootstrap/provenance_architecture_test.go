package bootstrap_test

// Provenance rules (AGENTS.md "Rules"): where an action came from is set at
// the edges and named in one place.
//
//   - event.WithMeta / event.WithCause (who and through what an event was
//     caused) are called only by the edges that know: the HTTP and MCP
//     middleware (httpx, mcpx), the workflow engine (a run's steps), the
//     outbox (a reaction), the fleet gateway (an agent), the GitHub
//     webhook and poll (sourcesvc), and release's own restore and upload
//     ticket. A domain service setting it elsewhere would misattribute.
//   - The channel words are spelled only in internal/channel (and the MCP
//     client prefix in internal/assistant): a "mcp:" or "assistant:" literal
//     anywhere else is a second, drifting definition.

import (
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// provenanceSetters: the directories allowed to call event.WithMeta or
// event.WithCause (the event package itself defines them).
var provenanceSetters = map[string]bool{
	"event":                        true,
	"event/adapters/outboxpg":      true,
	"httpx":                        true,
	"mcpx":                         true,
	"workflow/adapters/workflowpg": true,
	"fleet/adapters/fleetgrpc":     true,
	"source/sourcesvc":             true,
	"release/releasesvc":           true,
}

// channelWords: the literals only their owner may spell.
var channelWords = map[string]string{
	`"mcp:"`:       "assistant",
	`"assistant:"`: "channel",
}

func provenanceViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		dir := path.Dir(f.rel)
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "event" || (sel.Sel.Name != "WithMeta" && sel.Sel.Name != "WithCause") {
					return true
				}
				if !provenanceSetters[dir] {
					out = append(out, f.fset.Position(n.Pos()).String()+": "+dir+" calls event."+sel.Sel.Name+
						"; provenance is set only at the edges (httpx, mcpx, workflowpg, outboxpg, fleetgrpc, sourcesvc, releasesvc)")
				}
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				owner, ok := channelWords[n.Value]
				if !ok || dir == owner {
					return true
				}
				word, _ := strconv.Unquote(n.Value)
				out = append(out, f.fset.Position(n.Pos()).String()+": "+dir+" spells "+strconv.Quote(word)+
					"; the channels are named only in internal/"+owner)
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestProvenanceIsSetAtTheEdges(t *testing.T) { report(t, provenanceViolations(sources(t, false))) }

func TestProvenanceRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"httpx/auth.go":                     `package httpx; func p() { event.WithMeta(nil, event.Meta{}) }`,
		"workflow/adapters/workflowpg/e.go": `package workflowpg; func s() { event.WithMeta(nil, r.meta()) }`,
		"event/adapters/outboxpg/d.go":      `package outboxpg; func d() { event.WithCause(nil, e) }`,
		"channel/channel.go":                `package channel; const AssistantPrefix = "assistant:"`,
		"assistant/assistant.go":            `package assistant; const MCPClientPrefix = "mcp:"`,
		"httpx/mcp.go":                      `package httpx; func m() { _ = assistant.MCPClientPrefix + c }`,
		"project/projectsvc/ok_test.go":     `package projectsvc; func T() { event.WithMeta(nil, event.Meta{Client: "mcp:x"}) }`,
		"release/release.go":                `package release; const ViaAssistantPrefix = channel.AssistantPrefix`,
		"project/projectsvc/meta.go":        `package projectsvc; func m() { _ = event.MetaFrom(nil) }`,

		// violations
		"project/projectsvc/s.go":     `package projectsvc; func s() { event.WithMeta(nil, event.Meta{Client: c}) }`,
		"build/adapters/buildpg/r.go": `package buildpg; func r() { event.WithCause(nil, e) }`,
		"release/deployment.go":       `package release; const via = "assistant:"`,
		"audit/auditsvc/s.go":         `package auditsvc; func s() bool { return strings.HasPrefix(c, "mcp:") }`,
	})
	expectViolations(t, provenanceViolations(files),
		"audit/auditsvc/s.go: audit/auditsvc spells \"mcp:\"",
		"build/adapters/buildpg/r.go: build/adapters/buildpg calls event.WithCause",
		"project/projectsvc/s.go: project/projectsvc calls event.WithMeta",
		"release/deployment.go: release spells \"assistant:\"",
	)
}
