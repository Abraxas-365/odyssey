package permission_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/permission"
)

// usage scans the backend (non-test Go files outside this package) for
// permission.X references and "odyssey:…" string literals.
func usage(t *testing.T) (refs map[string]bool, literals []string) {
	t.Helper()
	refs = map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == "../../internal/permission" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && x.Name == "permission" {
						refs[n.Sel.Name] = true
					}
				case *ast.BasicLit:
					if s, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING && strings.HasPrefix(s, permission.Prefix+":") {
						literals = append(literals, fset.Position(n.Pos()).String()+" "+s)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return refs, literals
}

// TestNoOrphanPermissions: every permission of the manifest guards something
// in the backend. Remove unused ones from iam/permissions.json (and prune
// them in IAMKit after deploying: make iam-sync PRUNE=1).
func TestNoOrphanPermissions(t *testing.T) {
	refs, _ := usage(t)
	var orphans []string
	for name, p := range permission.Names {
		if !refs[name] {
			orphans = append(orphans, p)
		}
	}
	slices.Sort(orphans)
	if len(orphans) > 0 {
		t.Errorf("permissions no backend code uses: %v", orphans)
	}
}

// TestNoPermissionLiterals: routes name permissions through the generated
// constants, so a typo or a permission missing from the manifest does not
// compile.
func TestNoPermissionLiterals(t *testing.T) {
	_, literals := usage(t)
	if len(literals) > 0 {
		t.Errorf("use permission.* instead of string literals:\n%s", strings.Join(literals, "\n"))
	}
}

func TestRolesStayInTheCatalog(t *testing.T) {
	if len(permission.Roles) == 0 {
		t.Fatal("the manifest declares no roles")
	}
	for _, r := range permission.Roles {
		for _, p := range r.Permissions {
			if !slices.Contains(permission.All, p) {
				t.Errorf("role %s: %s is not in the catalog", r.Name, p)
			}
		}
	}
	for _, p := range permission.All {
		if permission.Labels[p] == "" {
			t.Errorf("%s has no label", p)
		}
	}
}
