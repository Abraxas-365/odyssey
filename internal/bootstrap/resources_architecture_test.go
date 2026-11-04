package bootstrap_test

// Resource rules (AGENTS.md "Rules"):
//
// Deleting a resource and connecting one to the app race: a connection made
// while the delete checks "is it in use?" would leave an app wired to a
// resource being torn down. Both serialise on the environment's settings
// lock, through a chain this test pins link by link, so no link can be
// bypassed unnoticed:
//
//  1. resource/resourcesvc: Service.Delete calls WithConnectionsLocked (it
//     never decides "in use" outside the lock);
//  2. resource/adapters/resourcerelease: Usage.WithConnectionsLocked calls
//     WithResourceUseLocked (the release domain's port);
//  3. release/releasesvc: WithResourceUseLocked, ConnectResource and
//     DisconnectResource all call lockSettings, the lock every settings
//     change takes (SetEnv, secrets, …), so they queue behind one another.
//
// AGENTS.md row:
//
//	Deleting a resource and connecting one serialise on the settings lock: `resourcesvc` `Delete` → `WithConnectionsLocked` → `resourcerelease` → `releasesvc.WithResourceUseLocked`, and it, `ConnectResource` and `DisconnectResource` call `lockSettings`
//	| `TestResourceUseIsSerialised`

import (
	"go/ast"
	"path"
	"sort"
	"strings"
	"testing"
)

// mustCall: the function (or method) named fn in package dir has to call a
// function or method named callee.
type mustCall struct{ dir, fn, callee string }

var resourceLockChain = []mustCall{
	{"resource/resourcesvc", "Delete", "WithConnectionsLocked"},
	{"resource/adapters/resourcerelease", "WithConnectionsLocked", "WithResourceUseLocked"},
	{"release/releasesvc", "WithResourceUseLocked", "lockSettings"},
	{"release/releasesvc", "ConnectResource", "lockSettings"},
	{"release/releasesvc", "DisconnectResource", "lockSettings"},
}

func calls(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			found = found || f.Sel.Name == name
		case *ast.Ident:
			found = found || f.Name == name
		}
		return !found
	})
	return found
}

func resourceLockViolations(files []goFile, chain []mustCall) []string {
	var out []string
	for _, link := range chain {
		seen := false
		for _, f := range files {
			if path.Dir(f.rel) != link.dir || strings.HasSuffix(f.rel, "_test.go") {
				continue
			}
			for _, decl := range f.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name != link.fn {
					continue
				}
				seen = true
				if !calls(fn.Body, link.callee) {
					out = append(out, f.fset.Position(fn.Pos()).String()+": "+link.fn+" does not call "+link.callee+
						"; deleting a resource and connecting one must serialise on the settings lock")
				}
			}
		}
		if !seen {
			out = append(out, link.dir+": "+link.fn+" not found; update resourceLockChain (the resource lock rule) with its new home")
		}
	}
	sort.Strings(out)
	return out
}

func TestResourceUseIsSerialised(t *testing.T) {
	report(t, resourceLockViolations(sources(t, false), resourceLockChain))
}

func TestResourceLockRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		"resource/resourcesvc/s.go":              `package resourcesvc; func (s *S) Delete() { s.usage.WithConnectionsLocked(nil, nil) }`,
		"resource/adapters/resourcerelease/u.go": `package resourcerelease; func (u *U) WithConnectionsLocked() { return u.usage.WithResourceUseLocked(nil) }`,
		"release/releasesvc/r.go":                `package releasesvc; func (s *S) WithResourceUseLocked() { s.lockSettings(nil) }; func (s *S) ConnectResource() { s.lockSettings(nil) }`,
		"release/releasesvc/r_test.go":           `package releasesvc; func (s *S) DisconnectResource() {}`,
		"release/releasesvc/d.go":                `package releasesvc; func (s *S) DisconnectResource() { lockSettings(nil) }`,
		"resource/resourcesvc/other.go":          `package resourcesvc; func (s *S) Create() {}`,
	})
	if v := resourceLockViolations(files, resourceLockChain); len(v) != 0 {
		t.Fatalf("a correct chain was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"resource/resourcesvc/s.go":              `package resourcesvc; func (s *S) Delete() { s.repo.MarkDeleting(nil) }`,
		"resource/adapters/resourcerelease/u.go": `package resourcerelease; func (u *U) WithConnectionsLocked() { fn(nil) }`,
		"release/releasesvc/r.go": `package releasesvc
func (s *S) WithResourceUseLocked() { s.tx.Do(nil) }
func (s *S) ConnectResource()       { s.lockState(nil) }`,
	})
	expectViolations(t, resourceLockViolations(files, resourceLockChain),
		"resource/resourcesvc/s.go: Delete does not call WithConnectionsLocked",
		"resource/adapters/resourcerelease/u.go: WithConnectionsLocked does not call WithResourceUseLocked",
		"release/releasesvc/r.go: WithResourceUseLocked does not call lockSettings",
		"release/releasesvc/r.go: ConnectResource does not call lockSettings",
		"release/releasesvc: DisconnectResource not found",
	)
}
