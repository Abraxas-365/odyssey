package bootstrap_test

// Release rules (AGENTS.md "Rules"):
//
//  1. What an environment serves changes in one place: the deploy workflow's
//     promote step, after the new version answered its health check. In
//     release/releasesvc only the function promote calls the repository's
//     Promote; in release/adapters/releasepg only Promote writes
//     current_release_id. Every other path (deploy, rollback, failures) goes
//     through a deployment and its health check.

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"sort"
	"testing"
)

const (
	releaseSvc     = "release/releasesvc"
	releasePg      = "release/adapters/releasepg"
	promoteStep    = "promote"
	promoteRepoFn  = "Promote"
	currentRelease = "current_release_id"
)

var writesCurrent = regexp.MustCompile(`(?i)` + currentRelease + `\s*=`)

func promoteViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != releaseSvc && dir != releasePg {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if dir == releaseSvc && ok && sel.Sel.Name == promoteRepoFn && fn.Name.Name != promoteStep {
						out = append(out, f.fset.Position(x.Pos()).String()+": "+fn.Name.Name+
							" calls Promote; only the deploy workflow's promote step changes what an environment serves")
					}
				case *ast.BasicLit:
					if dir == releasePg && x.Kind == token.STRING && writesCurrent.MatchString(x.Value) && fn.Name.Name != promoteRepoFn {
						out = append(out, f.fset.Position(x.Pos()).String()+": "+fn.Name.Name+
							" writes "+currentRelease+"; only Repository.Promote may")
					}
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestOnlyThePromoteStepMovesTheCurrentRelease(t *testing.T) {
	report(t, promoteViolations(sources(t, false)))
}

func TestReleaseRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/w.go": `package releasesvc
func (s *S) promote() { s.repo.Promote(nil) }`,
		releasePg + "/r.go": "package releasepg\nfunc (r *R) Promote() { r.db.Exec(`UPDATE release.environment_states SET current_release_id = $3`) }\n" +
			"func (r *R) Find() { r.db.Get(`SELECT current_release_id FROM release.environment_states`) }",
		// violations
		releaseSvc + "/s.go": `package releasesvc
func (s *S) Rollback() { s.repo.Promote(nil) }`,
		releasePg + "/x.go": "package releasepg\nfunc (r *R) Begin() { r.db.Exec(`UPDATE release.environment_states SET Current_Release_ID=$1`) }",
	})
	expectViolations(t, promoteViolations(files),
		releasePg+"/x.go: Begin writes current_release_id",
		releaseSvc+"/s.go: Rollback calls Promote")
}
