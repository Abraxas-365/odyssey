package bootstrap_test

// Source rules (AGENTS.md "Rules"):
//
//  1. The GitHub SDK (google/go-github) and the JWT library (golang-jwt)
//     stay inside source/adapters/sourcegithub: the domain speaks only the
//     GitProvider port, so another git host is another adapter. The JWT
//     library has one more home, access/adapters/accessjwt (it signs the
//     access tickets).
//  2. Unauthenticated routes are few and declared: only sourcehttp,
//     releasehttp, accesshttp and the server's own Assistant implement RegisterPublic,
//     each mounts exactly its own publicRoutes entry, and only the server mounts them (the composition
//     root hands them over).
//  3. release never depends on source (a deployment does not know where its
//     source came from), and source reaches release's operations only
//     through its Deployer port: release.Commands is named only in the
//     sourcerelease adapter and in sourcemodule.
//  4. The repository's cross-org queries (webhooks and the poll know only
//     the provider's ids) are called only by the service, and the function
//     that resolves a repository's connections compares their organization
//     with the installation's: a connection of another tenant is never
//     acted on.

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
	releaseHTTPAdapter  = "release/adapters/releasehttp"
	sourceReleaseAdapt  = "source/adapters/sourcerelease"
	sourceModule        = "source/sourcemodule"
	serverPkg           = "server"
	accessHTTPAdapter   = "access/adapters/accesshttp"
	accessJWTAdapter    = "access/adapters/accessjwt"
)

// confinedToGitHub are the libraries only the GitHub adapter may import.
var confinedToGitHub = []string{"github.com/google/go-github/", "github.com/golang-jwt/jwt"}

// jwtPrefix is also used, and only there besides sourcegithub, by the
// access adapter that signs tickets (EdDSA).
const jwtPrefix = "github.com/golang-jwt/jwt"

// publicRoutes are every unauthenticated route of the API besides /health
// and /api/config (each documented with `security: []`), per adapter that
// may mount them: GitHub's setup redirect and webhooks, the upload ticket
// (the ticket in the path is the only credential) and the server's skill
// download.
var publicRoutes = map[string]map[string]bool{
	sourceHTTPAdapter: {
		"GET /github/setup":     true,
		"POST /webhooks/github": true,
	},
	releaseHTTPAdapter: {
		"PUT /uploads/:ticket": true,
	},
	// the access sign-in of protected apps: the key set the gates verify
	// tickets with and the two browser steps (the OAuth flow is the proof)
	accessHTTPAdapter: {
		"GET /.well-known/odicea-access-keys": true,
		"GET /access/authorize":               true,
		"GET /access/callback":                true,
	},
	// the odicea skill's archive (no secrets in it; assistants fetch it with curl)
	serverPkg: {
		"GET /skills/odicea.tar.gz": true,
	},
}

func TestGitHubStaysInItsAdapter(t *testing.T) {
	report(t, gitHubSDKViolations(append(sources(t, true), repoRootSources(t)...)))
}
func TestPublicRoutesAreDeclared(t *testing.T) { report(t, publicRouteViolations(sources(t, false))) }
func TestReleaseDoesNotKnowSources(t *testing.T) {
	report(t, sourceReleaseViolations(sources(t, false)))
}
func TestCrossOrgQueriesStayChecked(t *testing.T) { report(t, crossOrgViolations(sources(t, false))) }

// crossOrgQueries are the source.Repository methods that are not scoped by
// organization. Only sourcesvc calls them; the second set must be followed by
// an organization comparison in the same function.
var crossOrgQueries = map[string]bool{
	"FindInstallationByExternal":  true,
	"ListConnectionsByRepository": true,
	"ListConnectionsToReconcile":  true,
}
var needsOrgComparison = map[string]bool{"ListConnectionsByRepository": true}

func crossOrgViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if !strings.HasPrefix(dir, "source/") && dir != "source" || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var calls []*ast.CallExpr
			compares := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && crossOrgQueries[sel.Sel.Name] {
						calls = append(calls, x)
					}
				case *ast.BinaryExpr:
					if x.Op != token.EQL && x.Op != token.NEQ {
						break
					}
					l, lok := x.X.(*ast.SelectorExpr)
					r, rok := x.Y.(*ast.SelectorExpr)
					compares = compares || lok && rok && l.Sel.Name == "Organization" && r.Sel.Name == "Organization"
				}
				return true
			})
			for _, call := range calls {
				name := call.Fun.(*ast.SelectorExpr).Sel.Name
				pos := f.fset.Position(call.Pos()).String()
				switch {
				case dir == "source/adapters/sourcepg" || dir == "source":
					// the repository and its port declare them
				case dir != "source/sourcesvc":
					out = append(out, pos+": "+dir+" calls "+name+", which is not scoped by organization; only source/sourcesvc may")
				case needsOrgComparison[name] && !compares:
					out = append(out, pos+": "+fn.Name.Name+" calls "+name+
						" without comparing the connection's Organization with the installation's")
				}
			}
		}
	}
	sort.Strings(out)
	return out
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
				if dir == accessJWTAdapter && strings.HasPrefix(lib, jwtPrefix) {
					continue // the ticket signer is the second home of the JWT library
				}
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
			if publicRoutes[dir] == nil {
				out = append(out, f.fset.Position(fn.Pos()).String()+": "+dir+
					" declares RegisterPublic; unauthenticated routes live only in "+sourceHTTPAdapter+", "+releaseHTTPAdapter+", "+accessHTTPAdapter+" and "+serverPkg)
				continue
			}
			out = append(out, undeclaredRoutes(f, fn, publicRoutes[dir])...)
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
func undeclaredRoutes(f goFile, fn *ast.FuncDecl, declared map[string]bool) []string {
	var out []string
	for _, stmt := range fn.Body.List {
		pos := f.fset.Position(stmt.Pos()).String()
		method, route, ok := literalRoute(stmt)
		if !ok {
			out = append(out, pos+": RegisterPublic may only register literal routes (<x>.Get(\"/path\", handler)); "+
				"no helpers, loops, groups, middleware or computed paths")
			continue
		}
		if !declared[method+" "+route] {
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
		"release/adapters/releasehttp/h.go": `package releasehttp
func (h *Handler) RegisterPublic(r fiber.Router) { r.Put("/uploads/:ticket", h.upload) }`,
		"access/adapters/accesshttp/h.go": `package accesshttp
func (h *Handler) RegisterPublic(r fiber.Router) { r.Get("/.well-known/odicea-access-keys", h.keys); r.Get("/access/authorize", h.a); r.Get("/access/callback", h.c) }`,
		"access/adapters/accessjwt/s.go": `package accessjwt; import _ "github.com/golang-jwt/jwt/v5"`,
		"server/assistant.go": `package server
func (a Assistant) RegisterPublic(r fiber.Router) { r.Get("/skills/odicea.tar.gz", a.skill) }`,
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
		"release/adapters/releasehttp/more.go": `package releasehttp
func (x *More) RegisterPublic(r fiber.Router) { r.Get("/uploads/:ticket", x.d) }`,
		"release/adapters/releasehttp/webhook.go": `package releasehttp
func (x *Hook) RegisterPublic(r fiber.Router) { r.Post("/webhooks/github", x.d) }`,
		"source/adapters/sourcehttp/upload.go": `package sourcehttp
func (x *Upload) RegisterPublic(r fiber.Router) { r.Put("/uploads/:ticket", x.d) }`,
		"release/adapters/releasehttp/group.go": `package releasehttp
func (x *Group) RegisterPublic(r fiber.Router) { r.Group("/uploads").Put("/:ticket", x.d) }`,
		"access/adapters/accesshttp/more.go": `package accesshttp
func (x *More) RegisterPublic(r fiber.Router) { r.Post("/access/callback", x.d) }`,
		"access/adapters/accessiam/jwt.go": `package accessiam; import _ "github.com/golang-jwt/jwt/v5"`,
		"access/accesssvc/gh.go":           `package accesssvc; import _ "github.com/google/go-github/v93/github"`,
		"server/extra.go": `package server
func (a Extra) RegisterPublic(r fiber.Router) { r.Get("/skills/other.tar.gz", a.skill) }`,
		"bootstrap/b.go":                  `package bootstrap; func wire(h *sourcehttp.Handler, app *fiber.App) { h.RegisterPublic(app) }`,
		"release/releasesvc/s.go":         `package releasesvc; import _ "` + m + `source"`,
		"source/sourcesvc/cross.go":       `package sourcesvc; import rel "` + m + `release"; type S struct{ r rel.Commands }`,
		"source/sourcesvc/dot.go":         `package sourcesvc; import . "` + m + `release"; type S struct{ r Commands }`,
		"source/adapters/sourcepg/dot.go": `package sourcepg; import . "` + m + `errx"`,
		"cmd/odyssey-api/main.go":         `package main; import _ "github.com/google/go-github/v93/github"`,
		"pkg/srcarchive/jwt.go":           `package srcarchive; import _ "github.com/golang-jwt/jwt/v5"`,

		// cross-org queries: correct
		"source/sourcesvc/hook_ok.go": `package sourcesvc
func (s *S) push(ctx C) { inst, _ := s.repo.FindInstallationByExternal(ctx, 1); conns, _ := s.repo.ListConnectionsByRepository(ctx, inst.ID, 2)
	for _, c := range conns { if c.Organization != inst.Organization { continue } } }
func (s *S) poll(ctx C) { s.repo.ListConnectionsToReconcile(ctx, t, 200) }`,
		"source/adapters/sourcepg/repo.go": `package sourcepg
func (r *R) FindInstallationByExternal(ctx C, id int64) {}`,

		// cross-org queries: violations
		"source/sourcesvc/hook_bad.go": `package sourcesvc
func (s *S) push(ctx C) { inst, _ := s.repo.FindInstallationByExternal(ctx, 1); s.repo.ListConnectionsByRepository(ctx, inst.ID, 2) }`,
		"source/sourcemodule/peek.go": `package sourcemodule
func (m M) peek(ctx C) { m.repo.ListConnectionsToReconcile(ctx, t, 1) }`,
	})
	expectViolations(t, crossOrgViolations(files),
		"source/sourcesvc/hook_bad.go: push calls ListConnectionsByRepository without comparing",
		"source/sourcemodule/peek.go: source/sourcemodule calls ListConnectionsToReconcile",
	)
	expectViolations(t, gitHubSDKViolations(files),
		"build/adapters/buildgit/g.go: build/adapters/buildgit imports github.com/google/go-github/v93/github",
		"source/sourcesvc/jwt.go: source/sourcesvc imports github.com/golang-jwt/jwt/v5",
		"cmd/odyssey-api/main.go: cmd/odyssey-api imports github.com/google/go-github/v93/github",
		"access/adapters/accessiam/jwt.go: access/adapters/accessiam imports github.com/golang-jwt/jwt/v5",
		"access/accesssvc/gh.go: access/accesssvc imports github.com/google/go-github/v93/github",
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
		"access/adapters/accesshttp/more.go: RegisterPublic mounts POST /access/callback",
		"release/adapters/releasehttp/more.go: RegisterPublic mounts GET /uploads/:ticket",
		"release/adapters/releasehttp/webhook.go: RegisterPublic mounts POST /webhooks/github",
		"source/adapters/sourcehttp/upload.go: RegisterPublic mounts PUT /uploads/:ticket",
		"release/adapters/releasehttp/group.go: RegisterPublic may only register literal routes",
		"server/extra.go: RegisterPublic mounts GET /skills/other.tar.gz",
		"bootstrap/b.go: bootstrap calls RegisterPublic",
	)
	expectViolations(t, sourceReleaseViolations(files),
		"release/releasesvc/s.go: release/releasesvc imports source; release must not depend on source",
		"source/sourcesvc/cross.go: source/sourcesvc names release.Commands",
		"source/sourcesvc/dot.go: source/sourcesvc dot-imports release",
		"source/adapters/sourcepg/dot.go: source/adapters/sourcepg dot-imports errx",
	)
}
