package bootstrap_test

// Backend and shape rules (AGENTS.md "Rules"):
//
//  1. A foundation port's backends (blob/adapters/*, event/adapters/*,
//     workflow/adapters/*) are opened only by the composition root: domains
//     and services depend on the port (blob.Store, event.Bus,
//     workflow.Engine), so S3 can replace the disk, or Temporal River,
//     without touching them.
//  2. The Go CDK (gocloud.dev) stays inside blob/adapters: what a storage
//     URL means is the adapter's business.
//  3. A domain with a service has the golden sample's shape: ports.go
//     declares Commands, Queries and Repository, and a <domain>module
//     assembles it.

import (
	"go/ast"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const goCloud = "gocloud.dev"

func backendViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		from := roleOf(dir)
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			if (p == goCloud || strings.HasPrefix(p, goCloud+"/")) && !strings.HasPrefix(dir, "blob/adapters/") {
				out = append(out, pos+": "+dir+" imports "+p+"; the Go CDK stays inside blob/adapters (depend on blob.Store)")
			}
			if !strings.HasPrefix(p, modulePath) {
				continue
			}
			to := strings.TrimPrefix(p, modulePath)
			parts := strings.Split(to, "/")
			if len(parts) < 3 || !foundation[parts[0]] || parts[1] != "adapters" {
				continue
			}
			if from.kind == "wiring" || strings.HasPrefix(dir, parts[0]+"/adapters/") {
				continue
			}
			out = append(out, pos+": "+from.kind+" "+dir+" imports the backend "+to+
				"; only the composition root opens one (depend on the "+parts[0]+" port)")
		}
	}
	sort.Strings(out)
	return out
}

// shapePorts are the interfaces every domain with a service declares.
var shapePorts = []string{"Commands", "Queries", "Repository"}

// statelessDomains keep nothing of their own, so they declare no Repository
// port: access seals a sign-in's state in the browser's cookie and looks the
// app up through release.AccessLookup; account asks IAMKit (which holds the
// user's assistant connections) through account.Connections.
var statelessDomains = map[string]bool{"access": true, "account": true}

// readOnlyDomains compose what other domains answer and change nothing:
// no Commands to give and no Repository to keep (TestInventoryIsReadOnly
// holds them to it).
var readOnlyDomains = map[string]bool{"inventory": true}

func shapeViolations(files []goFile) []string {
	type domain struct {
		svc, module bool
		ports       map[string]bool
	}
	domains := map[string]*domain{}
	get := func(name string) *domain {
		if domains[name] == nil {
			domains[name] = &domain{ports: map[string]bool{}}
		}
		return domains[name]
	}
	for _, f := range files {
		dir := path.Dir(f.rel)
		role := roleOf(dir)
		switch {
		case role.kind == "svc":
			get(role.domain).svc = true
		case role.kind == "module":
			get(role.domain).module = true
		case role.kind == "domain" && dir == role.domain && path.Base(f.rel) == "ports.go":
			d := get(role.domain)
			for _, decl := range f.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gen.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						if _, ok := ts.Type.(*ast.InterfaceType); ok {
							d.ports[ts.Name.Name] = true
						}
					}
				}
			}
		}
	}
	var out []string
	for name, d := range domains {
		if !d.svc {
			continue
		}
		for _, p := range shapePorts {
			if !d.ports[p] && !(p == "Repository" && statelessDomains[name]) && !(p != "Queries" && readOnlyDomains[name]) {
				out = append(out, name+"/ports.go: domain "+name+" has a service but declares no "+p+" port")
			}
		}
		if !d.module {
			out = append(out, name+": domain "+name+" has a service but no "+name+"module to assemble it")
		}
	}
	sort.Strings(out)
	return out
}

func TestBackendsStayInTheCompositionRoot(t *testing.T) {
	report(t, backendViolations(sources(t, false)))
}
func TestDomainsHaveTheGoldenShape(t *testing.T) { report(t, shapeViolations(sources(t, false))) }

func TestBackendAndShapeRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		// correct
		"blob/blob.go":                   `package blob`,
		"blob/adapters/gocloudblob/s.go": `package gocloudblob; import (_ "gocloud.dev/blob"; _ "` + m + `blob")`,
		"bootstrap/b.go":                 `package bootstrap; import (_ "` + m + `blob/adapters/gocloudblob"; _ "` + m + `workflow/adapters/workflowpg")`,
		"widget/ports.go":                `package widget; type Commands interface{}; type Queries interface{}; type Repository interface{}; type Runner interface{}`,
		"widget/widgetsvc/s.go":          `package widgetsvc; import _ "` + m + `blob"`,
		"widget/widgetmodule/m.go":       `package widgetmodule; import _ "` + m + `blob"`,
		"widget/adapters/widgetpg/r.go":  `package widgetpg`,

		// violations
		"order/ports.go":                 `package order; type Commands interface{}; type Repository struct{}`,
		"order/ordersvc/s.go":            `package ordersvc; import _ "` + m + `blob/adapters/gocloudblob"`,
		"order/adapters/orderblob/a.go":  `package orderblob; import _ "gocloud.dev/blob/s3blob"`,
		"order/ordermodule/m.go":         `package ordermodule; import _ "` + m + `event/adapters/outboxpg"`,
		"invoice/invoicesvc/s.go":        `package invoicesvc`,
		"invoice/ports.go":               `package invoice; type Commands interface{}; type Queries interface{}; type Repository interface{}`,
		"access/accessmodule/m.go":       `package accessmodule`,
		"access/accesssvc/s.go":          `package accesssvc`,
		"access/ports.go":                `package access; type Commands interface{}; type Queries interface{}`, // stateless: no Repository
		"inventory/inventorymodule/m.go": `package inventorymodule`,
		"inventory/inventorysvc/s.go":    `package inventorysvc`,
		"inventory/ports.go":             `package inventory; type Queries interface{}`, // read-only: no Commands, no Repository
	})
	expectViolations(t, backendViolations(files),
		"order/ordersvc/s.go: svc order/ordersvc imports the backend blob/adapters/gocloudblob",
		"order/adapters/orderblob/a.go: order/adapters/orderblob imports gocloud.dev/blob/s3blob",
		"order/ordermodule/m.go: module order/ordermodule imports the backend event/adapters/outboxpg",
	)
	expectViolations(t, shapeViolations(files),
		"order/ports.go: domain order has a service but declares no Queries port",
		"order/ports.go: domain order has a service but declares no Repository port",
		"invoice: domain invoice has a service but no invoicemodule",
	)
}
