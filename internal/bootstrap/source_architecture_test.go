package bootstrap_test

// Source rules (AGENTS.md "Rules"):
//
//  1. The GitHub SDK (google/go-github) and the JWT library (golang-jwt)
//     stay inside source/adapters/sourcegithub: the domain speaks only the
//     GitProvider port, so another git host is another adapter.
//  2. Unauthenticated routes are few and declared: only sourcehttp
//     implements RegisterPublic, it mounts exactly publicRoutes there, and
//     only the server mounts them (the composition root hands them over).
//  3. release never depends on source (a deployment does not know where its
//     source came from), and source reaches release's operations only
//     through its Deployer port: release.Commands is named only in the
//     sourcerelease adapter and in sourcemodule.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	sourceGitHubAdapter = "source/adapters/sourcegithub"
	sourceHTTPAdapter   = "source/adapters/sourcehttp"
	sourceReleaseAdapt  = "source/adapters/sourcerelease"
	sourceModule        = "source/sourcemodule"
	serverPkg           = "server"
)

// confinedToGitHub are the libraries only the GitHub adapter may import.
var confinedToGitHub = []string{"github.com/google/go-github/", "github.com/golang-jwt/jwt"}

// publicRoutes are every unauthenticated route of the API besides /health
// and /api/config (each documented with `security: []`).
var publicRoutes = map[string]bool{
	"GET /github/setup":     true,
	"POST /webhooks/github": true,
}

func TestGitHubStaysInItsAdapter(t *testing.T) {
	report(t, gitHubSDKViolations(append(sources(t, true), repoRootSources(t)...)))
}
func TestPublicRoutesAreDeclared(t *testing.T) { report(t, publicRouteViolations(sources(t, false))) }
func TestReleaseDoesNotKnowSources(t *testing.T) {
	report(t, sourceReleaseViolations(sources(t, false)))
}

// repoRootSources parses the Go files (tests included) under the repo-root
// cmd/ and pkg/, which sources() does not reach. Their rel keeps the "cmd/"
// or "pkg/" prefix so messages never collide with internal/ paths.
func repoRootSources(t *testing.T) []goFile {
	t.Helper()
	fset := token.NewFileSet()
	var out []goFile
	for _, root := range []string{"cmd", "pkg"} {
		base := filepath.Join("..", "..", root)
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(filepath.Join("..", ".."), p)
			if err != nil {
				return err
			}
			out = append(out, goFile{rel: filepath.ToSlash(rel), file: f, fset: fset})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func gitHubSDKViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == sourceGitHubAdapter {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, lib := range confinedToGitHub {
				if strings.HasPrefix(p, lib) {
					out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+p+
						"; it stays inside "+sourceGitHubAdapter+" (depend on source.GitProvider)")
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func publicRouteViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "RegisterPublic" || fn.Body == nil {
				continue
			}
			if dir != sourceHTTPAdapter {
				out = append(out, f.fset.Position(fn.Pos()).String()+": "+dir+
					" declares RegisterPublic; unauthenticated routes live only in "+sourceHTTPAdapter)
				continue
			}
			out = append(out, undeclaredRoutes(f, fn)...)
		}
		if dir == serverPkg {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RegisterPublic" {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+dir+
					" calls RegisterPublic; only the server mounts public routes (pass them in server.Options.Public)")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// undeclaredRoutes are the statements of RegisterPublic that are not a
// literal, declared public route. The body is read strictly: every statement
// must be `<x>.<Get|Post|...>("<literal path>", ...)` so no helper, loop or
// computed path can register a route the rule cannot see.
func undeclaredRoutes(f goFile, fn *ast.FuncDecl) []string {
	var out []string
	for _, stmt := range fn.Body.List {
		pos := f.fset.Position(stmt.Pos()).String()
		method, route, ok := literalRoute(stmt)
		if !ok {
			out = append(out, pos+": RegisterPublic may only register literal routes (<x>.Get(\"/path\", handler)); "+
				"no helpers, loops, groups, middleware or computed paths")
			continue
		}
		if !publicRoutes[method+" "+route] {
			out = append(out, pos+": RegisterPublic mounts "+method+" "+route+", which is not a declared public route")
		}
	}
	return out
}

// literalRoute reports the upper-case method and path of a statement of the
// form `<x>.<Get|Post|Put|Patch|Delete|Head|Options|All|Add>("<literal>", ...)`
// where <x> is a plain identifier or selector chain (no calls).
func literalRoute(stmt ast.Stmt) (method, route string, ok bool) {
	es, isExpr := stmt.(*ast.ExprStmt)
	if !isExpr {
		return "", "", false
	}
	call, isCall := es.X.(*ast.CallExpr)
	if !isCall || len(call.Args) == 0 {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || !plainReceiver(sel.X) {
		return "", "", false
	}
	switch sel.Sel.Name {
	case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "All", "Add":
	default:
		return "", "", false
	}
	lit, isLit := call.Args[0].(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", "", false
	}
	route, err := strconv.Unquote(lit.Value)
	if err != nil || route == "" {
		return "", "", false
	}
	return strings.ToUpper(sel.Sel.Name), route, true
}

// plainReceiver is an identifier or a chain of field selectors (r, h.app).
func plainReceiver(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return plainReceiver(x.X)
	}
	return false
}

func sourceReleaseViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		switch {
		case dir == "release" || strings.HasPrefix(dir, "release/"):
			for _, imp := range f.file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == modulePath+"source" || strings.HasPrefix(p, modulePath+"source/") {
					out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
						strings.TrimPrefix(p, modulePath)+"; release must not depend on source")
				}
			}
		case dir == "source" || strings.HasPrefix(dir, "source/"):
			// A dot import hides the package qualifier, so release.Commands
			// could be named unqualified: forbid it for every internal package.
			for _, imp := range f.file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if imp.Name != nil && imp.Name.Name == "." && strings.HasPrefix(p, modulePath) {
					out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" dot-imports "+
						strings.TrimPrefix(p, modulePath)+"; name internal packages qualified (a dot import hides release.Commands)")
				}
			}
			if dir == sourceReleaseAdapt || dir == sourceModule {
				continue
			}
			name := importName(f.file, modulePath+"release")
			if name == "" {
				continue
			}
			ast.Inspect(f.file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == name && (sel.Sel.Name == "Commands" || sel.Sel.Name == "Queries") {
					out = append(out, f.fset.Position(sel.Pos()).String()+": "+dir+" names release."+sel.Sel.Name+
						"; source reaches release only through its Deployer port ("+sourceReleaseAdapt+")")
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestSourceRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		// correct
		"source/adapters/sourcegithub/p.go": `package sourcegithub; import (_ "github.com/google/go-github/v93/github"; _ "github.com/golang-jwt/jwt/v5")`,
		"source/adapters/sourcehttp/h.go": `package sourcehttp
func (h *Handler) RegisterPublic(r fiber.Router) { r.Get("/github/setup", h.setup); r.Post("/webhooks/github", h.hook) }`,
		"server/server.go":                   `package server; func New(p PublicRoutes, app *fiber.App) { p.RegisterPublic(app) }`,
		"source/adapters/sourcerelease/d.go": `package sourcerelease; import "` + m + `release"; type D struct{ c release.Commands }`,
		"source/sourcemodule/m.go":           `package sourcemodule; import "` + m + `release"; type Deps struct{ R release.Commands }`,
		"source/sourcesvc/s.go":              `package sourcesvc; import "` + m + `release"; var _ release.DeployUploaded`,
		"release/release.go":                 `package release; import _ "` + m + `build"`,

		// violations
		"build/adapters/buildgit/g.go": `package buildgit; import _ "github.com/google/go-github/v93/github"`,
		"source/sourcesvc/jwt.go":      `package sourcesvc; import _ "github.com/golang-jwt/jwt/v5"`,
		"build/adapters/buildhttp/h.go": `package buildhttp
func (h *Handler) RegisterPublic(r fiber.Router) { r.Get("/hook", h.hook) }`,
		"source/adapters/sourcehttp/more.go": `package sourcehttp
func (x *Other) RegisterPublic(r fiber.Router) { r.Get("/github/debug", x.d) }`,
		"source/adapters/sourcehttp/use.go": `package sourcehttp
func (x *Use) RegisterPublic(r fiber.Router) { r.Use(x.mw) }`,
		"source/adapters/sourcehttp/computed.go": `package sourcehttp
func (x *Computed) RegisterPublic(r fiber.Router) { r.Post(prefix+"/x", x.d) }`,
		"source/adapters/sourcehttp/helper.go": `package sourcehttp
func (x *Helper) RegisterPublic(r fiber.Router) { r.Get("/github/setup", x.setup); x.mount(r) }`,
		"source/adapters/sourcehttp/loop.go": `package sourcehttp
func (x *Loop) RegisterPublic(r fiber.Router) { for _, p := range paths { r.Get(p, x.d) } }`,
		"source/adapters/sourcehttp/chain.go": `package sourcehttp
func (x *Chain) RegisterPublic(r fiber.Router) { r.Group("/x").Get("/github/setup", x.d) }`,
		"source/adapters/sourcehttp/all.go": `package sourcehttp
func (x *All) RegisterPublic(r fiber.Router) { r.All("/github/setup", x.d) }`,
		"bootstrap/b.go":                  `package bootstrap; func wire(h *sourcehttp.Handler, app *fiber.App) { h.RegisterPublic(app) }`,
		"release/releasesvc/s.go":         `package releasesvc; import _ "` + m + `source"`,
		"source/sourcesvc/cross.go":       `package sourcesvc; import rel "` + m + `release"; type S struct{ r rel.Commands }`,
		"source/sourcesvc/dot.go":         `package sourcesvc; import . "` + m + `release"; type S struct{ r Commands }`,
		"source/adapters/sourcepg/dot.go": `package sourcepg; import . "` + m + `errx"`,
		"cmd/odyssey-api/main.go":         `package main; import _ "github.com/google/go-github/v93/github"`,
		"pkg/srcarchive/jwt.go":           `package srcarchive; import _ "github.com/golang-jwt/jwt/v5"`,
	})
	expectViolations(t, gitHubSDKViolations(files),
		"build/adapters/buildgit/g.go: build/adapters/buildgit imports github.com/google/go-github/v93/github",
		"source/sourcesvc/jwt.go: source/sourcesvc imports github.com/golang-jwt/jwt/v5",
		"cmd/odyssey-api/main.go: cmd/odyssey-api imports github.com/google/go-github/v93/github",
		"pkg/srcarchive/jwt.go: pkg/srcarchive imports github.com/golang-jwt/jwt/v5",
	)
	expectViolations(t, publicRouteViolations(files),
		"build/adapters/buildhttp/h.go: build/adapters/buildhttp declares RegisterPublic",
		"source/adapters/sourcehttp/more.go: RegisterPublic mounts GET /github/debug",
		"source/adapters/sourcehttp/use.go: RegisterPublic may only register literal routes",
		"source/adapters/sourcehttp/computed.go: RegisterPublic may only register literal routes",
		"source/adapters/sourcehttp/helper.go: RegisterPublic may only register literal routes",
		"source/adapters/sourcehttp/loop.go: RegisterPublic may only register literal routes",
		"source/adapters/sourcehttp/chain.go: RegisterPublic may only register literal routes",
		"source/adapters/sourcehttp/all.go: RegisterPublic mounts ALL /github/setup",
		"bootstrap/b.go: bootstrap calls RegisterPublic",
	)
	expectViolations(t, sourceReleaseViolations(files),
		"release/releasesvc/s.go: release/releasesvc imports source; release must not depend on source",
		"source/sourcesvc/cross.go: source/sourcesvc names release.Commands",
		"source/sourcesvc/dot.go: source/sourcesvc dot-imports release",
		"source/adapters/sourcepg/dot.go: source/adapters/sourcepg dot-imports errx",
	)
}
