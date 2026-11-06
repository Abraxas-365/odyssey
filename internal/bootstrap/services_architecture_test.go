package bootstrap_test

// Multi-service apps (MS-1, AGENTS.md "Rules"): an app's services are one
// list with one writer. Only project/projectsvc calls ReplaceServices (in its
// transaction, after validation, publishing project.services_changed); every
// other domain reads the list through project.Queries.Services. A version's
// snapshot of them is made in one place, release/releasesvc, through
// release.ServicesOf (which refuses what the version cannot run).

import (
	"go/ast"
	"path"
	"sort"
	"strings"
	"testing"
)

// serviceWriters: the call → the one package that may make it.
var serviceWriters = map[string]string{
	"ReplaceServices": "project/projectsvc",
	"ServicesOf":      "release/releasesvc",
}

func serviceWriteViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			case *ast.Ident:
				name = fn.Name
			}
			if owner, ok := serviceWriters[name]; ok && dir != owner {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+dir+" calls "+name+
					"; an app's services are written by project/projectsvc and snapshotted by release/releasesvc only ("+owner+")")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestServicesHaveOneWriter(t *testing.T) { report(t, serviceWriteViolations(sources(t, false))) }

func TestServicesRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"project/projectsvc/s.go":                `package projectsvc; func (s *S) f() { s.repo.ReplaceServices(nil, org, id, list) }`,
		"project/adapters/projectpg/r.go":        `package projectpg; func (r *R) ReplaceServices() { r.replaceServices() }`,
		"project/adapters/projectpg/r_test.go":   `package projectpg_test; func T() { repo.ReplaceServices(nil, org, id, list) }`,
		"release/releasesvc/s.go":                `package releasesvc; func (s *S) f() { release.ServicesOf(topology, image, c) }`,
		"release/adapters/releasepg/r_test.go":   `package releasepg_test; func T() { release.ServicesOf(topology, image, c) }`,
		"release/adapters/releasemcp/read_ok.go": `package releasemcp; func (t *T) f() { t.projects.Services(nil, org, id) }`,
		// violations
		"project/adapters/projecthttp/h.go":  `package projecthttp; func (h *H) f() { h.repo.ReplaceServices(nil, org, id, list) }`,
		"project/projectmodule/m.go":         `package projectmodule; func New() { repo.ReplaceServices(nil, org, id, list) }`,
		"source/sourcesvc/s.go":              `package sourcesvc; func (s *S) f() { s.projects.ReplaceServices(nil, org, id, list) }`,
		"release/adapters/releasefleet/c.go": `package releasefleet; func (c *C) f() { release.ServicesOf(topology, image, cfg) }`,
		"release/release.go":                 `package release; func f() { ServicesOf(topology, image, c) }`,
		"build/buildsvc/s.go":                `package buildsvc; func (s *S) f() { release.ServicesOf(topology, image, c) }`,
	})
	expectViolations(t, serviceWriteViolations(files),
		"project/adapters/projecthttp/h.go: project/adapters/projecthttp calls ReplaceServices",
		"project/projectmodule/m.go: project/projectmodule calls ReplaceServices",
		"source/sourcesvc/s.go: source/sourcesvc calls ReplaceServices",
		"release/adapters/releasefleet/c.go: release/adapters/releasefleet calls ServicesOf",
		"release/release.go: release calls ServicesOf",
		"build/buildsvc/s.go: build/buildsvc calls ServicesOf",
	)
}
