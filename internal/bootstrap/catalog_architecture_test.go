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
//
//	The agent's resource controller tells kinds apart only in its engine registry (agent/controllers/resource/engines.go)
//	| `TestResourceEnginesAreTheOnlyKindSwitch`
//
//	A recipe version is written only in catalog/recipes.go and never changes once released; catalog imports no domain but the project kernel
//	| `TestRecipeVersionsAreWrittenOnlyInTheFactory`, `TestReleasedVersionsNeverChange`

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
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

// engineRegistryViolations: the agent's resource controller tells kinds
// apart in one place, its engine registry (engines.go): a kind constant
// (v1alpha1.ResourceX or resourcekind.X) named anywhere else in the package
// is a switch the next kind would have to find. Tests may name kinds.
func engineRegistryViolations(files []goFile) []string {
	const dir = "agent/controllers/resource/"
	agentKinds := map[string]bool{"ResourcePostgres": true, "ResourceRedis": true, "ResourceFiles": true, "ResourceObjectStorage": true}
	var out []string
	for _, f := range files {
		if !strings.HasPrefix(f.rel, dir) || strings.Contains(strings.TrimPrefix(f.rel, dir), "/") ||
			strings.HasSuffix(f.rel, "_test.go") || f.rel == dir+"engines.go" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && (agentKinds[sel.Sel.Name] || (x.Name == "resourcekind" && kindConstants[sel.Sel.Name])) {
				out = append(out, f.fset.Position(sel.Pos()).String()+": "+x.Name+"."+sel.Sel.Name+
					" names a kind outside the engine registry (engines.go); add what the kind needs to its engine")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestResourceEnginesAreTheOnlyKindSwitch(t *testing.T) {
	dir := filepath.Join("..", "..", "agent", "controllers", "resource")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []goFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, goFile{rel: "agent/controllers/resource/" + e.Name(), file: f, fset: fset})
	}
	if len(files) == 0 {
		t.Fatal("no sources in agent/controllers/resource")
	}
	report(t, engineRegistryViolations(files))
}

func TestEngineRegistryRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"agent/controllers/resource/engines.go":  `package resource; var e = map[v1alpha1.ResourceKind]int{v1alpha1.ResourcePostgres: 1}`,
		"agent/controllers/resource/x_test.go":   `package resource; var k = v1alpha1.ResourceRedis`,
		"agent/controllers/app/a.go":             `package app; var k = v1alpha1.ResourceFiles`,
		"agent/controllers/resource/postgres.go": `package resource; func f(o *v1alpha1.OdysseyResource) bool { return engines[o.Spec.Kind].apply != nil }`,
	})
	if v := engineRegistryViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"agent/controllers/resource/teardown.go": `package resource; func f(k v1alpha1.ResourceKind) { switch k { case v1alpha1.ResourceRedis: } }`,
		"agent/controllers/resource/names.go":    `package resource; func g(k resourcekind.Kind) bool { return k == resourcekind.Postgres }`,
	})
	expectViolations(t, engineRegistryViolations(files),
		"agent/controllers/resource/names.go: resourcekind.Postgres names a kind outside the engine registry",
		"agent/controllers/resource/teardown.go: v1alpha1.ResourceRedis names a kind outside the engine registry",
	)
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

// esoKinds are ESO's kinds and resources, as a string names them.
var esoKinds = map[string]bool{
	"ExternalSecret": true, "ExternalSecretList": true, "externalsecrets": true,
	"ClusterSecretStore": true, "ClusterSecretStoreList": true, "clustersecretstores": true,
	"SecretStore": true, "secretstores": true,
}

// externalSecretsViolations: the agent names the External Secrets
// Operator's API (its group, its store kind) only in agent/externalsecret,
// so the version it speaks (external-secrets.io/v1) and how a value goes
// into its template safely are decided once for apps and resources alike.
// Tests may name it.
func externalSecretsViolations(files []goFile) []string {
	const own = "agent/externalsecret/"
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f.rel, own) || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && (strings.Contains(s, "external-secrets.io") || esoKinds[s]) {
				out = append(out, f.fset.Position(lit.Pos()).String()+": "+strconv.Quote(s)+
					" names ESO's API outside agent/externalsecret; use that package")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestOnlyExternalSecretNamesESO(t *testing.T) {
	root := filepath.Join("..", "..", "agent")
	fset := token.NewFileSet()
	var files []goFile
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("..", ".."), p)
		files = append(files, goFile{rel: filepath.ToSlash(rel), file: f, fset: fset})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no sources in agent/")
	}
	report(t, externalSecretsViolations(files))
}

func TestExternalSecretsRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"agent/externalsecret/e.go":       `package externalsecret; const Group = "external-secrets.io"; var k = "ClusterSecretStore"`,
		"agent/controllers/app/x_test.go": `package app; var g = "external-secrets.io/v1"`,
		"agent/controllers/app/ok.go":     `package app; var m = "a ExternalSecret named x already exists"`,
	})
	if v := externalSecretsViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"agent/desired/a.go":              `package desired; var g = "external-secrets.io"`,
		"agent/controllers/resource/b.go": `package resource; var s = map[string]any{"kind": "ClusterSecretStore"}`,
		"agent/controllers/resource/c.go": `package resource; var k = "ExternalSecret"`,
	})
	expectViolations(t, externalSecretsViolations(files),
		`agent/desired/a.go: "external-secrets.io" names ESO's API`,
		`agent/controllers/resource/b.go: "ClusterSecretStore" names ESO's API`,
		`agent/controllers/resource/c.go: "ExternalSecret" names ESO's API`,
	)
}

// recipeVersionViolations: a recipe version is a released, immutable thing
// (frozen by testdata/versions.json), so it is written only in
// catalog/recipes.go: a catalog.RecipeVersion literal anywhere else
// (another domain making up a recipe, a service tweaking one) is how a
// released version stops meaning one thing. The catalog root imports no
// other domain except the project shared kernel (tiers); who asks the
// catalog does it through its ports.
func recipeVersionViolations(files []goFile) []string {
	const catalogPkg = modulePath + "catalog"
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == "catalog" {
			for _, imp := range f.file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if !strings.HasPrefix(p, modulePath) {
					continue
				}
				to := roleOf(strings.TrimPrefix(p, modulePath))
				if to.kind == "domain" && to.domain != "catalog" && to.dir != "project" {
					out = append(out, f.fset.Position(imp.Pos()).String()+": catalog must not import "+to.dir+" (only the project kernel)")
				}
			}
		}
		if f.rel == "catalog/recipes.go" {
			continue
		}
		local, alias := dir == "catalog", ""
		for _, imp := range f.file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == catalogPkg {
				alias = "catalog"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			switch t := lit.Type.(type) {
			case *ast.Ident:
				if local && t.Name == "RecipeVersion" {
					out = append(out, f.fset.Position(lit.Pos()).String()+": a RecipeVersion is written only in catalog/recipes.go")
				}
			case *ast.SelectorExpr:
				if x, ok := t.X.(*ast.Ident); ok && alias != "" && x.Name == alias && t.Sel.Name == "RecipeVersion" {
					out = append(out, f.fset.Position(lit.Pos()).String()+": a RecipeVersion is written only in catalog/recipes.go")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestRecipeVersionsAreWrittenOnlyInTheFactory(t *testing.T) {
	report(t, recipeVersionViolations(sources(t, false)))
}

// factoryLookupViolations: catalog.Factory is only the versions that ship
// in the binary, not the ones an organization published. Whoever reads a
// resource's pinned version outside the catalog asks Queries.Version
// (which knows both), or an organization's own recipe would be "unknown"
// to it: only the catalog root and its service call Factory.
func factoryLookupViolations(files []goFile) []string {
	const catalogPkg = modulePath + "catalog"
	var out []string
	for _, f := range files {
		if dir := path.Dir(f.rel); dir == "catalog" || dir == "catalog/catalogsvc" {
			continue
		}
		alias := ""
		for _, imp := range f.file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == catalogPkg {
				alias = "catalog"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				if alias == "." {
					// Factory() would be a bare identifier no selector check sees
					out = append(out, f.fset.Position(imp.Pos()).String()+": catalog is never dot-imported (its lookups must stay visible)")
				}
			}
		}
		if alias == "" || alias == "." {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Factory" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == alias {
					out = append(out, f.fset.Position(sel.Pos()).String()+
						": catalog.Factory misses the organization's own recipes; ask catalog.Queries.Version")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestOnlyTheCatalogReadsTheFactoryAlone(t *testing.T) {
	report(t, factoryLookupViolations(sources(t, false)))
}

// resourcespecPkg describes how a resource is prepared, including the
// connection of one the company already has (host, store, key).
const resourcespecPkg = "github.com/Abraxas-365/odyssey/pkg/resourcespec"

// eventSpecViolations: an event is the trail (and the webhooks); a recipe's
// connection never goes in it, so no events.go imports resourcespec nor
// declares a field of a type that carries a spec (a desired resource, a
// recipe version), which needs no import from inside its own domain. An
// event names the recipe@version, which is enough to read the rest.
func eventSpecViolations(files []goFile) []string {
	carriers := map[string]bool{"DesiredResource": true, "Desired": true, "RecipeVersion": true, "OrgRecipe": true, "Spec": true, "Existing": true}
	var out []string
	for _, f := range files {
		if path.Base(f.rel) != "events.go" {
			continue
		}
		for _, imp := range f.file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == resourcespecPkg {
				out = append(out, f.fset.Position(imp.Pos()).String()+
					": events carry the recipe@version, never its spec (an existing connection would reach the trail)")
			}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				t := field.Type
				for {
					if s, ok := t.(*ast.StarExpr); ok {
						t = s.X
						continue
					}
					if a, ok := t.(*ast.ArrayType); ok {
						t = a.Elt
						continue
					}
					break
				}
				name := ""
				switch x := t.(type) {
				case *ast.Ident:
					name = x.Name
				case *ast.SelectorExpr:
					name = x.Sel.Name
				}
				if carriers[name] {
					out = append(out, f.fset.Position(field.Pos()).String()+": an event field of type "+name+
						" carries a spec; copy the fields the event needs")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestEventsCarryNoResourceSpec(t *testing.T) {
	report(t, eventSpecViolations(sources(t, false)))
}

func TestEventSpecRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"widget/events.go": `package widget; import _ "` + resourcekindPkg + `"; type Made struct{ Recipe string; Version int }`,
		"widget/widget.go": `package widget; import _ "` + resourcespecPkg + `"; type W struct{ D DesiredResource }`,
	})
	if v := eventSpecViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"order/events.go": `package order; import _ "` + resourcespecPkg + `"`,
		"fleet/events.go": `package fleet; type Applied struct{ R *DesiredResource; V []catalog.RecipeVersion }`,
	})
	expectViolations(t, eventSpecViolations(files),
		"fleet/events.go: an event field of type DesiredResource carries a spec",
		"fleet/events.go: an event field of type RecipeVersion carries a spec",
		"order/events.go: events carry the recipe@version, never its spec")
}

func TestFactoryLookupRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"catalog/catalogsvc/s.go": `package catalogsvc; import "` + modulePath + `catalog"; var k = catalog.Factory()`,
		"widget/widgetsvc/ok.go":  `package widgetsvc; import "` + modulePath + `catalog"; var r catalog.Resolved`,
	})
	if v := factoryLookupViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"widget/widgetsvc/s.go":  `package widgetsvc; import c "` + modulePath + `catalog"; var v, _ = c.Factory().Version("x", 1)`,
		"widget/widgethttp/h.go": `package widgethttp; import . "` + modulePath + `catalog"; var v = Factory()`,
	})
	expectViolations(t, factoryLookupViolations(files),
		"widget/widgethttp/h.go: catalog is never dot-imported",
		"widget/widgetsvc/s.go: catalog.Factory misses the organization's own recipes",
	)
}

func TestRecipeVersionRuleCatchesViolations(t *testing.T) {
	cat := `import "` + modulePath + `catalog"; `
	files := parseFixtures(t, map[string]string{
		"catalog/recipes.go": `package catalog; var f = []RecipeVersion{{Recipe: "x"}}; var g = RecipeVersion{}`,
		"catalog/catalog.go": `package catalog; import _ "` + modulePath + `project"; func (v RecipeVersion) Ref() string { return "" }`,
		"widget/ok.go":       `package widget; ` + cat + `var v, _ = catalog.Factory().Version("x", 1)`,
	})
	if v := recipeVersionViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"catalog/extra.go":      `package catalog; import _ "` + modulePath + `resource"; var x = RecipeVersion{Recipe: "y"}`,
		"widget/widgetsvc/s.go": `package widgetsvc; import c "` + modulePath + `catalog"; var v = c.RecipeVersion{Recipe: "z"}`,
	})
	expectViolations(t, recipeVersionViolations(files),
		"catalog/extra.go: catalog must not import resource",
		"catalog/extra.go: a RecipeVersion is written only",
		"widget/widgetsvc/s.go: a RecipeVersion is written only",
	)
}

// machineTokenViolations: Odicea's IAMKit service-account secret is held by
// one adapter, release/adapters/releaseiam, the only caller of IAMKit's
// MachineToken. Other readers of the IAMKit directory (catalogiam's teams)
// get the token from it through injected functions (catalogiam.Token), so
// the secret has one owner and one cache to drop when IAMKit refuses it.
func machineTokenViolations(files []goFile) []string {
	const owner = "release/adapters/releaseiam"
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) == owner || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "MachineToken" {
					out = append(out, f.fset.Position(call.Pos()).String()+": only "+owner+
						" calls MachineToken (it owns the service-account secret); take its Token instead")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestOneOwnerOfTheServiceAccountSecret(t *testing.T) {
	report(t, machineTokenViolations(sources(t, false)))
}

func TestMachineTokenRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"release/adapters/releaseiam/directory.go": `package releaseiam; func (d *D) t() { d.auth.MachineToken(ctx, d.secret) }`,
		"catalog/adapters/catalogiam/directory.go": `package catalogiam; func (d *D) t() { d.token.Get(ctx) }`,
		"catalog/adapters/catalogiam/x_test.go":    `package catalogiam; func f() { auth.MachineToken(ctx, "s") }`,
	})
	if v := machineTokenViolations(files); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	files = parseFixtures(t, map[string]string{
		"catalog/adapters/catalogiam/directory.go": `package catalogiam; func (d *D) t() { d.auth.MachineToken(ctx, d.secret) }`,
	})
	expectViolations(t, machineTokenViolations(files),
		"catalog/adapters/catalogiam/directory.go: only release/adapters/releaseiam calls MachineToken",
	)
}
