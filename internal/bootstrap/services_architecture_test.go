package bootstrap_test

// Multi-service apps (MS-1, AGENTS.md "Rules"): an app's services are one
// list with one writer. Only project/projectsvc calls ReplaceServices (in its
// transaction, after validation, publishing project.services_changed); every
// other domain reads the list through project.Queries.Services. A version's
// snapshot of them is made in one place, release/releasesvc, through
// release.ServicesOf / ImageServices (which refuses an image for several
// services), and so is a deployment's (release.BuildsFor).

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
	"ImageServices":   "release/releasesvc",
	"BuildsFor":       "release/releasesvc",
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

// Detection (MS-5): pkg/detect is reached only through project.Detect (the
// root project package), whose proposal is shaped as the app's services
// (it passes SetServices.Validate) — so the console, GitHub and the MCP get
// the same proposal, and detecting never writes: the proposal is saved only
// by the person, through set services.
const detectPkg = repoModule + "pkg/detect"

func detectViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		dir := path.Dir(f.rel)
		for _, imp := range f.file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == detectPkg && dir != "internal/project" {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports pkg/detect; services are proposed only by project.Detect")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestServicesAreProposedInOnePlace(t *testing.T) {
	report(t, detectViolations(repoSources(t, "agent", "cmd", "internal", "pkg")))
}

func TestDetectRuleCatchesViolations(t *testing.T) {
	m := repoModule
	files := parseFixtures(t, map[string]string{
		// correct
		"internal/project/detect.go":     `package project; import _ "` + m + `pkg/detect"`,
		"internal/source/sourcesvc/s.go": `package sourcesvc; import _ "` + m + `internal/project"`,
		"internal/bootstrap/x_test.go":   `package bootstrap_test; import _ "` + m + `pkg/detect"`,
		// violations
		"internal/source/sourcesvc/bad.go":          `package sourcesvc; import _ "` + m + `pkg/detect"`,
		"internal/project/projectsvc/p.go":          `package projectsvc; import _ "` + m + `pkg/detect"`,
		"internal/project/adapters/projectmcp/t.go": `package projectmcp; import _ "` + m + `pkg/detect"`,
	})
	expectViolations(t, detectViolations(files),
		"internal/source/sourcesvc/bad.go: internal/source/sourcesvc imports pkg/detect",
		"internal/project/projectsvc/p.go: internal/project/projectsvc imports pkg/detect",
		"internal/project/adapters/projectmcp/t.go: internal/project/adapters/projectmcp imports pkg/detect",
	)
}

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
		"release/adapters/releasemcp/p.go":   `package releasemcp; func (t *T) f() { release.ImageServices(topology, image, c) }`,
		"release/adapters/releasehttp/h.go":  `package releasehttp; func (h *H) f() { release.BuildsFor(topology) }`,
	})
	expectViolations(t, serviceWriteViolations(files),
		"project/adapters/projecthttp/h.go: project/adapters/projecthttp calls ReplaceServices",
		"project/projectmodule/m.go: project/projectmodule calls ReplaceServices",
		"source/sourcesvc/s.go: source/sourcesvc calls ReplaceServices",
		"release/adapters/releasefleet/c.go: release/adapters/releasefleet calls ServicesOf",
		"release/release.go: release calls ServicesOf",
		"build/buildsvc/s.go: build/buildsvc calls ServicesOf",
		"release/adapters/releasemcp/p.go: release/adapters/releasemcp calls ImageServices",
		"release/adapters/releasehttp/h.go: release/adapters/releasehttp calls BuildsFor",
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

// Multi-service builds (MS-3a): a deployment from source has one build per
// service, so stopping it stops all of them. In release/releasesvc only
// cancelBuilds (over Deployment.BuildIDs) calls Builder.Cancel: a direct
// Cancel(*d.Build) would stop the first service's and leave the others
// running.
const (
	releaseServiceDir = "release/releasesvc"
	buildCancelOwner  = "cancelBuilds"
)

func buildCancelViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) != releaseServiceDir || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, d := range f.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == buildCancelOwner {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Cancel" || !mentions(sel.X, "builder") {
					return true
				}
				out = append(out, f.fset.Position(call.Pos()).String()+": "+fn.Name.Name+" cancels a build directly"+
					"; a deployment's builds (one per service) are cancelled together by "+buildCancelOwner)
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestDeploymentBuildsAreCancelledTogether(t *testing.T) {
	report(t, buildCancelViolations(sources(t, false)))
}

func TestBuildCancelRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"release/releasesvc/ok.go": `package releasesvc
func (s *Service) cancelBuilds(d Deployment) error { for _, id := range d.BuildIDs() { s.builder.Cancel(ctx, d.Organization, id, c) }; return nil }
func (s *Service) retire(d Deployment) error { return s.cancelBuilds(d) }
func (s *Service) stop(d *Deployment) { s.engine.Cancel(ctx, d.Organization, d.Run, "superseded") }`,
		"release/releasesvc/x_test.go":       `package releasesvc; func fixture(s *Service) { s.builder.Cancel(ctx, org, id, c) }`,
		"build/buildsvc/s.go":                `package buildsvc; func (s *Service) f() { s.builder.Cancel(ctx, org, id, c) }`,
		"release/adapters/releasebuild/b.go": `package releasebuild; func (b *Builder) Cancel() error { return b.commands.Cancel(ctx, org, id, in) }`,
		// violations
		"release/releasesvc/bad.go": `package releasesvc
func (s *Service) deployFailed(d Deployment) error { return s.builder.Cancel(ctx, d.Organization, *d.Build, c) }`,
	})
	expectViolations(t, buildCancelViolations(files),
		"release/releasesvc/bad.go: deployFailed cancels a build directly",
	)
}

// Reuse by content digest (MS-3b, docs/build.md «Reutilización»): an image
// made from the same files by the same builder is reused, so who decides it
// is fixed. Each call has one owner function: the candidates are listed only
// by buildsvc.reuse (which drops them all for a rebuild), the origin read
// only by buildsvc.origin, written only by buildsvc.settle; a scan is copied
// only by buildsvc.copiedScan (digest-checked, ≤ ScanReuseWithin); fleet
// keeps a candidate only through DesiredBuild.ApplyReuse in DispatchBuild
// (same builder identity, same repository). Registry access for it
// (go-containerregistry) lives only in the agent's builder.
var reuseOwners = map[string]struct{ dir, fn string }{
	"ListCandidates": {"build/buildsvc", "reuse"},
	"FindCandidate":  {"build/buildsvc", "origin"},
	"SetOrigin":      {"build/buildsvc", "settle"},
	"copiedScan":     {"build/buildsvc", "scan"},
	"ApplyReuse":     {"fleet/fleetsvc", "DispatchBuild"},
}

const registryClient = "github.com/google/go-containerregistry"

func reuseViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		dir := path.Dir(f.rel)
		for _, imp := range f.file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); strings.HasPrefix(p, registryClient) && dir != "agent/builder" {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+p+
					"; only the agent's builder talks to the registry to reuse an image")
			}
		}
		for _, d := range f.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch c := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = c.Sel.Name
				case *ast.Ident:
					name = c.Name
				}
				if owner, ok := reuseOwners[name]; ok && (dir != owner.dir || fn.Name.Name != owner.fn) {
					out = append(out, f.fset.Position(call.Pos()).String()+": "+fn.Name.Name+" in "+dir+" calls "+name+
						"; reuse is decided only by "+owner.dir+"."+owner.fn)
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestReuseIsDecidedInOnePlace(t *testing.T) {
	report(t, reuseViolations(append(sources(t, false), repoSources(t, "agent", "cmd")...)))
}

func TestReuseRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"build/buildsvc/ok.go": `package buildsvc
func (s *Service) reuse(b Build) { s.repo.ListCandidates(ctx, org, p, c, n) }
func (s *Service) origin(b Build) { s.repo.FindCandidate(ctx, org, p, c, i, img) }
func (s *Service) settle(b Build) { s.repo.SetOrigin(ctx, org, id, i, from) }
func (s *Service) scan(b Build) { s.copiedScan(ctx, b) }`,
		"fleet/fleetsvc/ok.go":                     `package fleetsvc; func (s *Service) DispatchBuild(b DesiredBuild) { b.ApplyReuse(bd) }`,
		"build/adapters/buildpg/r.go":              `package buildpg; func (r *Repository) ListCandidates() { r.list() }`,
		"build/adapters/buildpg/r_test.go":         `package buildpg_test; func T() { repo.SetOrigin(ctx, org, id, i, from) }`,
		"agent/builder/reuse.go":                   "package builder\nimport \"github.com/google/go-containerregistry/pkg/v1/remote\"",
		"agent/builder/reuse_test.go":              "package builder\nimport \"github.com/google/go-containerregistry/pkg/registry\"",
		"fleet/adapters/fleetgrpc/convert_test.go": `package fleetgrpc; func T() { b.ApplyReuse(bd) }`,
		// violations
		"build/buildsvc/bad.go": `package buildsvc
func (s *Service) dispatch(b Build) { s.repo.ListCandidates(ctx, org, p, c, n) }
func (s *Service) finish(b Build) { s.repo.SetOrigin(ctx, org, id, i, from) }`,
		"build/adapters/buildhttp/h.go":  `package buildhttp; func (h *H) f() { h.repo.FindCandidate(ctx, org, p, c, i, img) }`,
		"fleet/adapters/fleetpg/b.go":    `package fleetpg; func (r *R) PutBuild(b DesiredBuild) { b.ApplyReuse(bd) }`,
		"release/releasesvc/s.go":        `package releasesvc; func (s *Service) promote() { s.copiedScan(ctx, b) }`,
		"build/adapters/buildfleet/r.go": "package buildfleet\nimport \"github.com/google/go-containerregistry/pkg/name\"",
	})
	expectViolations(t, reuseViolations(files),
		"build/adapters/buildfleet/r.go: build/adapters/buildfleet imports",
		"build/adapters/buildhttp/h.go: f in build/adapters/buildhttp calls FindCandidate",
		"build/buildsvc/bad.go: dispatch in build/buildsvc calls ListCandidates",
		"build/buildsvc/bad.go: finish in build/buildsvc calls SetOrigin",
		"fleet/adapters/fleetpg/b.go: PutBuild in fleet/adapters/fleetpg calls ApplyReuse",
		"release/releasesvc/s.go: promote in release/releasesvc calls copiedScan",
	)
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
