package bootstrap_test

// Resource catalog rules (AGENTS.md "Rules"):
//
// A kind is a contract registered in pkg/resourcekind (its words, its
// variables, whether it is mounted or holds data). The control plane reads
// what it needs about a kind from that registry (resourcekind.Of / Lookup)
// and never branches on a particular kind: a `case resourcekind.Postgres:`
// or `k == resourcekind.Files` is how a new kind ends up half supported.
// Naming a kind as data (a default, a factory recipe) is fine.
//
// AGENTS.md row:
//
//	The control plane never branches on a resource kind: `case resourcekind.X` and `== / != resourcekind.X` are only in pkg/resourcekind; read the contract (`resourcekind.Of`)
//	| `TestNoBranchOnResourceKind`

import (
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const resourcekindPkg = "github.com/Abraxas-365/odyssey/pkg/resourcekind"

// kindConstants are the contract names a branch must not test.
var kindConstants = map[string]bool{"Postgres": true, "Redis": true, "Files": true, "ObjectStorage": true}

func kindBranchViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		alias := ""
		for _, imp := range f.file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == resourcekindPkg {
				alias = "resourcekind"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		if alias == "" {
			continue
		}
		isKind := func(e ast.Expr) bool {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			x, ok := sel.X.(*ast.Ident)
			return ok && x.Name == alias && kindConstants[sel.Sel.Name]
		}
		flag := func(n ast.Node, what string) {
			out = append(out, f.fset.Position(n.Pos()).String()+": "+what+
				" branches on a resource kind; read its contract (resourcekind.Of) instead")
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CaseClause:
				for _, e := range x.List {
					if isKind(e) {
						flag(e, "case")
					}
				}
			case *ast.BinaryExpr:
				if (x.Op == token.EQL || x.Op == token.NEQ) && (isKind(x.X) || isKind(x.Y)) {
					flag(x, "comparison")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestNoBranchOnResourceKind(t *testing.T) {
	report(t, kindBranchViolations(sources(t, false)))
}

func TestKindBranchRuleCatchesViolations(t *testing.T) {
	imp := `import "` + resourcekindPkg + `"; `
	files := parseFixtures(t, map[string]string{
		"widget/ok.go": `package widget; ` + imp + `var def = resourcekind.Postgres; func f(k resourcekind.Kind) bool { return resourcekind.Of(k).Mounted }`,
	})
	if v := kindBranchViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"widget/a.go": `package widget; ` + imp + `func f(k resourcekind.Kind) string { switch k { case resourcekind.Redis: return "x" }; return "" }`,
		"widget/b.go": `package widget; import rk "` + resourcekindPkg + `"; func g(k rk.Kind) bool { return k != rk.Files }`,
	})
	expectViolations(t, kindBranchViolations(files),
		"widget/a.go: case branches on a resource kind",
		"widget/b.go: comparison branches on a resource kind",
	)
}
