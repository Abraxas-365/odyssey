package bootstrap_test

// "Lo que usa esta app" (2e) rule (AGENTS.md "Rules"): release assembles the
// Uses read model from ports, so it must not know the ai domain. Only the
// releaseai adapter (the release.AIStatus implementation) imports ai.

import (
	"path"
	"strconv"
	"strings"
	"testing"
)

const releaseAIAdapter = "release/adapters/releaseai"

func TestReleaseDoesNotKnowAI(t *testing.T) { report(t, releaseAIViolations(sources(t, true))) }

func releaseAIViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != "release" && !strings.HasPrefix(dir, "release/") || dir == releaseAIAdapter {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == modulePath+"ai" || strings.HasPrefix(p, modulePath+"ai/") {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
					strings.TrimPrefix(p, modulePath)+"; release reaches ai only through its AIStatus port ("+releaseAIAdapter+")")
			}
		}
	}
	return out
}

func TestReleaseAIRuleCatchesViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		"release/uses.go":                   `package release`,
		"release/adapters/releaseai/s.go":   `package releaseai; import _ "` + m + `ai"`,
		"release/releasesvc/uses.go":        `package releasesvc; import _ "` + m + `ai"`,
		"release/adapters/releasemcp/t.go":  `package releasemcp; import _ "` + m + `ai/aisvc"`,
		"ai/adapters/airelease/release.go":  `package airelease; import _ "` + m + `release"`,
		"resource/adapters/resourcepg/r.go": `package resourcepg; import _ "` + m + `ai"`,
	})
	expectViolations(t, releaseAIViolations(files),
		"release/releasesvc/uses.go: release/releasesvc imports ai",
		"release/adapters/releasemcp/t.go: release/adapters/releasemcp imports ai/aisvc",
	)
}
