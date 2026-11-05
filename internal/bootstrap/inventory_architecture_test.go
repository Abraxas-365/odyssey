package bootstrap_test

// Inventory rules (3c, AGENTS.md "Rules"): the inventory and "Necesita tu
// atención" compose what the other domains know; they own nothing.
//
//  1. inventory only reads: it reaches another domain through that domain's
//     root package (its Queries types), never its svc, adapters, repository
//     or Commands, never touches the database, the event bus or a
//     transaction, and its own ports declare no method that sounds like a
//     write.
//  2. No domain knows the inventory: only bootstrap (the composition root)
//     imports it.

import (
	"go/ast"
	"path"
	"strconv"
	"strings"
	"testing"
)

const inventoryDir = "inventory"

func TestInventoryIsReadOnly(t *testing.T) { report(t, inventoryReadOnlyViolations(sources(t, false))) }
func TestNoDomainKnowsTheInventory(t *testing.T) {
	report(t, inventoryKnownViolations(sources(t, true)))
}

// writeVerbs start the name of a method that changes something.
var writeVerbs = []string{"Create", "Update", "Delete", "Remove", "Set", "Put", "Save", "Insert", "Add", "Apply", "Approve", "Reject",
	"Revoke", "Cancel", "Publish", "Record", "Enable", "Disable", "Rotate", "Move", "Archive", "Start", "Run"}

// forbiddenForInventory are the packages (relative to internal/ or external)
// inventory never imports.
var forbiddenForInventory = []string{"database/sql", "github.com/jmoiron/sqlx", "github.com/jackc/pgx", "txn", "event", "workflow", "audit"}

func inventoryReadOnlyViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != inventoryDir && !strings.HasPrefix(dir, inventoryDir+"/") {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			rel, internal := strings.CutPrefix(p, modulePath)
			switch {
			case !internal:
				for _, bad := range forbiddenForInventory {
					if p == bad || strings.HasPrefix(p, bad+"/") {
						out = append(out, pos+": "+dir+" imports "+p+"; inventory only reads through other domains' Queries")
					}
				}
			case strings.HasPrefix(rel, inventoryDir+"/") || rel == inventoryDir:
				// its own packages
			case strings.Contains(rel, "/"):
				// a domain's svc, adapter or module (domain/x), as opposed to its root package
				if !strings.HasPrefix(rel, "inventory") {
					out = append(out, pos+": "+dir+" imports "+rel+"; inventory takes a domain's root package only, never its svc or adapters")
				}
			default:
				for _, bad := range forbiddenForInventory {
					if rel == bad {
						out = append(out, pos+": "+dir+" imports "+rel+"; inventory only reads through other domains' Queries")
					}
				}
			}
		}
		// the write side of a domain, by name
		ast.Inspect(f.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if _, isPkg := sel.X.(*ast.Ident); isPkg && (sel.Sel.Name == "Commands" || sel.Sel.Name == "Repository") {
					out = append(out, f.fset.Position(sel.Pos()).String()+": "+dir+" uses "+sel.Sel.Name+"; inventory never writes")
				}
			}
			return true
		})
		// its own ports declare reads only
		if path.Base(f.rel) == "ports.go" && dir == inventoryDir {
			ast.Inspect(f.file, func(n ast.Node) bool {
				it, ok := n.(*ast.InterfaceType)
				if !ok {
					return true
				}
				for _, m := range it.Methods.List {
					for _, name := range m.Names {
						for _, verb := range writeVerbs {
							if strings.HasPrefix(name.Name, verb) {
								out = append(out, f.fset.Position(name.Pos()).String()+": inventory port method "+name.Name+" sounds like a write; ports are read-only")
							}
						}
					}
				}
				return true
			})
		}
	}
	return out
}

func inventoryKnownViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == inventoryDir || strings.HasPrefix(dir, inventoryDir+"/") || dir == "bootstrap" {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == modulePath+inventoryDir || strings.HasPrefix(p, modulePath+inventoryDir+"/") {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+strings.TrimPrefix(p, modulePath)+
					"; only the composition root knows the inventory (the domains hand it their Queries, not the other way round)")
			}
		}
	}
	return out
}

func TestInventoryRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		"inventory/ports.go":                    `package inventory; import _ "` + m + `project"; type P interface { List(); Create() }`,
		"inventory/inventorysvc/ok.go":          `package inventorysvc; import _ "` + m + `release"; import _ "` + m + `inventory"`,
		"inventory/inventorysvc/svc.go":         `package inventorysvc; import _ "` + m + `release/releasesvc"`,
		"inventory/inventorysvc/db.go":          `package inventorysvc; import _ "database/sql"`,
		"inventory/inventorysvc/write.go":       `package inventorysvc; import "` + m + `project"; var _ = project.Commands(nil)`,
		"inventory/adapters/inventorypg/r.go":   `package inventorypg; import _ "` + m + `txn"`,
		"release/releasesvc/uses.go":            `package releasesvc; import _ "` + m + `inventory"`,
		"project/adapters/projectmcp/t.go":      `package projectmcp; import _ "` + m + `inventory/adapters/inventorymcp"`,
		"bootstrap/bootstrap.go":                `package bootstrap; import _ "` + m + `inventory/inventorymodule"`,
		"inventory/adapters/inventoryhttp/h.go": `package inventoryhttp; import _ "` + m + `inventory"`,
	})
	expectViolations(t, inventoryReadOnlyViolations(files),
		"inventory/ports.go",
		"inventory/inventorysvc/svc.go: inventory/inventorysvc imports release/releasesvc",
		"inventory/inventorysvc/db.go: inventory/inventorysvc imports database/sql",
		"inventory/inventorysvc/write.go:1:89: inventory/inventorysvc uses Commands",
		"inventory/adapters/inventorypg/r.go: inventory/adapters/inventorypg imports txn",
	)
	expectViolations(t, inventoryKnownViolations(files),
		"release/releasesvc/uses.go: release/releasesvc imports inventory;",
		"project/adapters/projectmcp/t.go: project/adapters/projectmcp imports inventory/adapters/inventorymcp",
	)
}
