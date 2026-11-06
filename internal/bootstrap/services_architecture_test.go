package bootstrap_test

// Multi-service apps (MS-1, AGENTS.md "Rules"): an app's services are one
// list with one writer. Only project/projectsvc calls ReplaceServices (in its
// transaction, after validation, publishing project.services_changed); every
// other domain reads the list through project.Queries.Services. A version's
// snapshot of them is made in one place, release/releasesvc, through
// release.ServicesOf (which refuses what the version cannot run).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
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

// Multi-service apps in the agent (MS-2): the app's label (LabelApp) is
// membership — the egress fence, secrets, observing the app's pods — never
// what a Deployment or a Service selects, or two services of one app would
// share pods. Workloads select by LabelWorkload. The one exception is the
// hot migration of an app from before services: mutateService keeps the
// Service on the app's label until the old Deployment is gone.
const (
	appControllerDir    = "agent/controllers/app"
	legacySelectorOwner = "mutateService"
)

func workloadSelectorViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) != appControllerDir || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, d := range f.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range as.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Selector" || i >= len(as.Rhs) || !mentions(as.Rhs[i], "LabelApp") {
						continue
					}
					if fn.Name.Name == legacySelectorOwner {
						continue
					}
					out = append(out, f.fset.Position(as.Pos()).String()+": "+fn.Name.Name+" selects by LabelApp"+
						"; a workload selects by LabelWorkload (only "+legacySelectorOwner+" keeps the app's label, while a legacy app migrates)")
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

// mentions reports whether e refers to an identifier (or a selector) named name.
func mentions(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			found = found || x.Name == name
		case *ast.SelectorExpr:
			found = found || x.Sel.Name == name
		}
		return !found
	})
	return found
}

// agentAppSources parses the app controller with bodies (repoSources reads
// only imports).
func agentAppSources(t *testing.T) []goFile {
	t.Helper()
	fset := token.NewFileSet()
	dir := filepath.Join("..", "..", filepath.FromSlash(appControllerDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []goFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, goFile{rel: appControllerDir + "/" + e.Name(), file: f, fset: fset})
	}
	if len(out) == 0 {
		t.Fatalf("no sources in %s", dir)
	}
	return out
}

func TestWorkloadsSelectByWorkload(t *testing.T) {
	report(t, workloadSelectorViolations(agentAppSources(t)))
}

func TestWorkloadSelectorRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"agent/controllers/app/ok.go": `package app
func mutateDeployment(d *Deployment, w workload) { d.Spec.Selector = &LabelSelector{MatchLabels: map[string]string{LabelWorkload: w.Deployment}} }
func mutateService(s *Service, a *App, legacy bool) { if legacy { s.Spec.Selector = map[string]string{LabelApp: a.Name} } }
func observe(c Client, a *App) { c.List(ctx, &pods, MatchingLabels{LabelApp: a.Name}) }`,
		"agent/controllers/app/x_test.go": `package app
func fixture(d *Deployment) { d.Spec.Selector = &LabelSelector{MatchLabels: map[string]string{LabelApp: "portal"}} }`,
		"agent/egress/netpol.go": `package egress
func apps(p *Policy) { p.Spec.Selector = sel(LabelApp) }`,
		// violations
		"agent/controllers/app/bad.go": `package app
func mutateDeployment(d *Deployment, a *App) { d.Spec.Selector = &LabelSelector{MatchLabels: map[string]string{LabelApp: a.Name}} }
func mutateWorker(s *Service, a *App) { s.Spec.Selector = map[string]string{desired.LabelApp: a.Name} }`,
	})
	expectViolations(t, workloadSelectorViolations(files),
		"agent/controllers/app/bad.go: mutateDeployment selects by LabelApp",
		"agent/controllers/app/bad.go: mutateWorker selects by LabelApp",
	)
}
