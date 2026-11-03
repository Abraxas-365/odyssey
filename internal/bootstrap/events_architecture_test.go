package bootstrap_test

// Event rules (AGENTS.md "Rules"; go-hex-architecture domain-events §12):
//
//  1. Only the composition root (bootstrap, *module) subscribes: a domain or
//     service never sees who reacts to it.
//  2. <domain>/events.go is the domain's public contract: it imports
//     foundation packages only, never another domain.
//  3. Repositories run every statement through txn.Q(ctx, r.db) so they join
//     the service's transaction (and its events): no r.db.ExecContext.
//  4. A service method that writes through its repository publishes an event
//     (that is also how the write reaches the audit trail).
//  5. Every Event* type constant in events.go has a golden payload contract
//     in <domain>/testdata/events/<type>.v<N>.json.

import (
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const eventPkg = modulePath + "event"

// importName is the local name of pkg in f ("" when not imported).
func importName(f *ast.File, pkg string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != pkg {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path.Base(p)
	}
	return ""
}

func subscribeViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		role := roleOf(dir)
		if role.kind == "wiring" || role.kind == "module" || role.kind == "foundation" {
			continue
		}
		name := importName(f.file, eventPkg)
		if name == "" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == name && (sel.Sel.Name == "Subscribe" || sel.Sel.Name == "Subscription") {
				out = append(out, f.fset.Position(sel.Pos()).String()+": "+role.kind+" "+dir+
					" must not subscribe (event."+sel.Sel.Name+"); reactions are wired in the composition root")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func eventsFileViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Base(f.rel) != "events.go" || roleOf(path.Dir(f.rel)).kind != "domain" {
			continue
		}
		self := roleOf(path.Dir(f.rel)).domain
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, modulePath) {
				continue
			}
			to := roleOf(strings.TrimPrefix(p, modulePath))
			if to.kind != "foundation" && to.domain != self {
				out = append(out, f.fset.Position(imp.Pos()).String()+": events.go of "+self+
					" is a public contract and must not import "+p+" (use IDs and primitives)")
			}
		}
	}
	sort.Strings(out)
	return out
}

// dbMethods are the sqlx calls a repository must make through txn.Q.
var dbMethods = map[string]bool{
	"ExecContext": true, "GetContext": true, "SelectContext": true, "QueryRowxContext": true,
	"QueryxContext": true, "QueryContext": true, "QueryRowContext": true, "NamedExecContext": true,
	"BeginTxx": true, "BeginTx": true, "Exec": true, "Get": true, "Select": true, "Query": true,
}

func repositoryTxViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if roleOf(dir).kind != "adapter" || !strings.HasSuffix(dir, "pg") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !dbMethods[sel.Sel.Name] {
				return true
			}
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "db" {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+dir+" calls db."+sel.Sel.Name+
					" directly; use txn.Q(ctx, r.db) so it joins the service's transaction")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// readPrefixes name repository methods that do not write.
var readPrefixes = []string{"Find", "List", "Get", "Count", "Exists", "Search", "Load", "Lock", "Walk"}

// eventSinks are the domains whose writes only record events (publishing
// would loop): the reason is part of the rule.
var eventSinks = map[string]string{
	"audit": "the trail records every event; an audit event would audit itself forever",
}

// observers are the domains whose repositories record observations
// (Observe* methods): what something outside reports, not a decision made
// here; the outcome that matters is published by whoever acts on it. Only
// these domains get the exemption, so renaming a write cannot escape the
// rule.
var observers = map[string]string{
	"fleet":  "heartbeats and app statuses reported by agents; deployment events carry their outcome",
	"source": "webhook deliveries seen and branch heads polled; the push events carry the outcome",
}

func isRead(name string) bool {
	for _, p := range readPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func publishViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		role := roleOf(dir)
		if role.kind != "svc" || eventSinks[role.domain] != "" {
			continue
		}
		observes := observers[role.domain] != ""
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || !fn.Name.IsExported() {
				continue
			}
			writes, publishes := "", false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Publish" {
					publishes = true
				}
				observation := observes && strings.HasPrefix(sel.Sel.Name, "Observe")
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "repo" && !isRead(sel.Sel.Name) && !observation && writes == "" {
					writes = sel.Sel.Name
				}
				return true
			})
			if writes != "" && !publishes {
				out = append(out, f.fset.Position(fn.Pos()).String()+": "+dir+"."+fn.Name.Name+
					" writes (repo."+writes+") without publishing an event")
			}
		}
	}
	sort.Strings(out)
	return out
}

// contractViolations: every Event* constant of an events.go has a golden
// file; exists answers for paths relative to internal/.
func contractViolations(files []goFile, exists func(rel string) bool) []string {
	var out []string
	for _, f := range files {
		if path.Base(f.rel) != "events.go" {
			continue
		}
		dir := path.Dir(f.rel)
		for _, decl := range f.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Event") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					typ, _ := strconv.Unquote(lit.Value)
					if !exists(path.Join(dir, "testdata", "events", typ+".v1.json")) {
						out = append(out, f.fset.Position(name.Pos()).String()+": "+typ+
							" has no contract "+path.Join(dir, "testdata/events", typ+".v1.json")+
							" (UPDATE_GOLDEN=1 go test ./internal/"+dir+"/...)")
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestOnlyTheCompositionRootSubscribes(t *testing.T) {
	report(t, subscribeViolations(sources(t, false)))
}
func TestEventContractsImportNoDomain(t *testing.T) {
	report(t, eventsFileViolations(sources(t, false)))
}
func TestRepositoriesJoinTheTransaction(t *testing.T) {
	report(t, repositoryTxViolations(sources(t, false)))
}
func TestWritesPublishEvents(t *testing.T) { report(t, publishViolations(sources(t, false))) }
func TestEventsHaveContracts(t *testing.T) {
	report(t, contractViolations(sources(t, false), func(rel string) bool {
		_, err := os.Stat(filepath.Join("..", filepath.FromSlash(rel)))
		return err == nil
	}))
}

func TestEventRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		// correct
		"bootstrap/wire.go": `package bootstrap; import "` + m + `event"; func w(b *event.Bus) { event.Subscribe(b, "", 1, "", nil) }`,
		"widget/widgetmodule/m.go": `package widgetmodule; import ev "` + m + `event"
var _ = ev.Subscription{}`,
		"widget/events.go": `package widget; import (_ "` + m + `event"; _ "` + m + `identity")
const EventCreated = "widget.created"`,
		"widget/adapters/widgetpg/r.go": `package widgetpg
func (r *R) Create(ctx C) error { _, err := txn.Q(ctx, r.db).ExecContext(ctx, "x"); return err }`,
		"widget/widgetsvc/s.go": `package widgetsvc
func (s *S) Create(ctx C) error { return s.tx.Do(ctx, func(ctx C) error { s.repo.Create(ctx); return s.events.Publish(ctx) }) }
func (s *S) Find(ctx C) error { _, err := s.repo.Find(ctx); return err }`,

		// violations
		"order/ordersvc/s.go": `package ordersvc; import "` + m + `event"
func (s *S) Wire(b *event.Bus) { event.Subscribe(b, "", 1, "", nil) }
func (s *S) Archive(ctx C) error { return s.repo.Archive(ctx) }
func (s *S) Ping(ctx C) error { return s.repo.ObservePing(ctx) }`,
		"fleet/fleetsvc/s.go": `package fleetsvc
func (s *S) Heartbeat(ctx C) error { return s.repo.ObserveHeartbeat(ctx) }`,
		"order/events.go": `package order; import _ "` + m + `widget"
const EventPlaced = "order.placed"`,
		"order/adapters/orderpg/r.go": `package orderpg
func (r *R) Create(ctx C) error { _, err := r.db.ExecContext(ctx, "x"); return err }`,
	})
	expectViolations(t, subscribeViolations(files), "order/ordersvc/s.go: svc order/ordersvc must not subscribe")
	expectViolations(t, eventsFileViolations(files), "order/events.go: events.go of order is a public contract")
	expectViolations(t, repositoryTxViolations(files), "order/adapters/orderpg/r.go: order/adapters/orderpg calls db.ExecContext")
	expectViolations(t, publishViolations(files),
		"order/ordersvc/s.go: order/ordersvc.Archive writes (repo.Archive) without publishing",
		"order/ordersvc/s.go: order/ordersvc.Ping writes (repo.ObservePing) without publishing") // not an observer domain
	expectViolations(t, contractViolations(files, func(rel string) bool { return rel == "widget/testdata/events/widget.created.v1.json" }),
		"order/events.go: order.placed has no contract")
}
