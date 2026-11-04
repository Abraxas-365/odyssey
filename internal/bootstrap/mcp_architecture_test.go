package bootstrap_test

// MCP rules (AGENTS.md "Rules", plan 1d §8):
//
//  1. Every mcpx.Tool literal declares Permissions as permission.X
//     constants (never string literals) and Risk as a mcpx.Risk* constant,
//     in the literal (never assigned afterwards; mcpx imported by name).
//     At runtime the contract test (mcp_tools_test) also checks every
//     registered tool's permissions against the catalog.
//  2. The <domain>mcp adapters use only their domains' Commands/Queries
//     ports: never a Repository (the layer rule already keeps services and
//     other adapters out), like the *http adapters.
//  3. Only mcpx imports the MCP SDK (tests may use its client): tools never
//     see it, so it changes in one place.
//  4. mcpx imports neither IAMKit nor Fiber: httpx.MCPAuth authenticates and
//     hands it a neutral Principal over net/http.

import (
	"go/ast"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	mcpSDK    = "github.com/modelcontextprotocol/go-sdk"
	mcpxPkg   = "mcpx"
	fiberLib  = "github.com/gofiber/fiber"
	permPkg   = "permission"
	riskConst = "Risk"
)

func TestMCPToolsDeclarePermissionsAndRisk(t *testing.T) {
	report(t, mcpToolViolations(sources(t, false)))
}
func TestMCPAdaptersUseOnlyPorts(t *testing.T) { report(t, mcpAdapterViolations(sources(t, false))) }
func TestMCPSDKStaysInMcpx(t *testing.T)       { report(t, mcpImportViolations(sources(t, false))) }

// selectorOf reports whether e is pkg.<Name with prefix>.
func selectorOf(e ast.Expr, pkg, prefix string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && strings.HasPrefix(sel.Sel.Name, prefix)
}

func mcpToolViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) == mcpxPkg {
			continue
		}
		mcpx := importName(f.file, modulePath+mcpxPkg)
		if mcpx == "" {
			continue
		}
		if mcpx == "." || mcpx == "_" {
			out = append(out, f.rel+": import mcpx by name (no dot import): the tool rules read mcpx.Tool")
			continue
		}
		perm := importName(f.file, modulePath+permPkg)
		ast.Inspect(f.file, func(n ast.Node) bool {
			// A tool is declared once, whole: no field set afterwards.
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Permissions" || sel.Sel.Name == "Risk") {
						out = append(out, f.fset.Position(sel.Pos()).String()+": set a tool's "+sel.Sel.Name+" in its mcpx.Tool literal, not afterwards")
					}
				}
			}
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !selectorOf(lit.Type, mcpx, "Tool") || lit.Type.(*ast.SelectorExpr).Sel.Name != "Tool" {
				return true
			}
			pos := f.fset.Position(lit.Pos()).String()
			fields := map[string]ast.Expr{}
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						fields[key.Name] = kv.Value
					}
				}
			}
			perms, ok := fields["Permissions"].(*ast.CompositeLit)
			if !ok || len(perms.Elts) == 0 {
				out = append(out, pos+": mcpx.Tool must declare Permissions as a []string{permission.X, ...} literal")
			} else {
				for _, p := range perms.Elts {
					if perm == "" || !selectorOf(p, perm, "") {
						out = append(out, f.fset.Position(p.Pos()).String()+": mcpx.Tool permissions must be permission.X constants")
					}
				}
			}
			if risk := fields["Risk"]; risk == nil || !selectorOf(risk, mcpx, riskConst) {
				out = append(out, pos+": mcpx.Tool must declare Risk as a mcpx.Risk* constant")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// isMCPAdapter: <domain>/adapters/<name>mcp.
func isMCPAdapter(dir string) bool {
	parts := strings.Split(dir, "/")
	return len(parts) == 3 && parts[1] == "adapters" && strings.HasSuffix(parts[2], "mcp")
}

func mcpAdapterViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if !isMCPAdapter(path.Dir(f.rel)) {
			continue
		}
		domains := map[string]bool{} // local names of imported domain roots
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if rel, ok := strings.CutPrefix(p, modulePath); ok && roleOf(rel).kind == "domain" && !strings.Contains(rel, "/") {
				domains[importName(f.file, p)] = true
			}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Repository" {
				if id, ok := sel.X.(*ast.Ident); ok && domains[id.Name] {
					out = append(out, f.fset.Position(sel.Pos()).String()+": MCP tools use the Commands/Queries ports, never a Repository")
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func mcpImportViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			if (p == mcpSDK || strings.HasPrefix(p, mcpSDK+"/")) && dir != mcpxPkg {
				out = append(out, pos+": "+dir+" imports "+p+"; only mcpx speaks the MCP SDK (declare tools as mcpx.Tool)")
			}
			if dir == mcpxPkg && (p == iamkitSDK || strings.HasPrefix(p, iamkitSDK+"/") || strings.HasPrefix(p, fiberLib)) {
				out = append(out, pos+": mcpx imports "+p+"; it serves net/http with a neutral Principal (httpx.MCPAuth authenticates)")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestMCPRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		// correct
		"mcpx/server.go": `package mcpx; import (_ "net/http"; _ "github.com/modelcontextprotocol/go-sdk/mcp")`,
		"widget/adapters/widgetmcp/t.go": `package widgetmcp
import ("` + m + `mcpx"; "` + m + `permission"; "` + m + `widget")
func Tools(q widget.Queries) []mcpx.Tool { return []mcpx.Tool{mcpx.Typed(mcpx.Tool{Name: "a", Permissions: []string{permission.WidgetsRead}, Risk: mcpx.RiskRead}, nil)} }`,
		"widget/adapters/widgethttp/h.go": `package widgethttp; import "` + m + `widget"; var _ widget.Repository`,

		// violations
		"order/adapters/ordermcp/t.go": `package ordermcp
import (x "` + m + `mcpx"; "` + m + `order"; _ "github.com/modelcontextprotocol/go-sdk/mcp")
var r order.Repository
var g = struct{ Repository string }{}.Repository
var a = x.Tool{Name: "a", Permissions: []string{"odyssey:orders:read"}, Risk: "read"}
var b = x.Tool{Name: "b"}
func init() { b.Permissions = []string{"x"}; b.Risk = x.RiskRead }`,
		"invoice/adapters/invoicemcp/t.go": `package invoicemcp; import . "` + m + `mcpx"; var _ = Tool{}`,
		"mcpx/auth.go":                     `package mcpx; import (_ "github.com/gofiber/fiber/v2"; _ "github.com/Abraxas-365/iamkit/sdk/resourceserver")`,
	})
	expectViolations(t, mcpToolViolations(files),
		"order/adapters/ordermcp/t.go: mcpx.Tool permissions must be permission.X constants",
		"order/adapters/ordermcp/t.go: mcpx.Tool must declare Risk",
		"order/adapters/ordermcp/t.go: mcpx.Tool must declare Permissions",
		"order/adapters/ordermcp/t.go: mcpx.Tool must declare Risk",
		"order/adapters/ordermcp/t.go: set a tool's Permissions in its mcpx.Tool literal",
		"order/adapters/ordermcp/t.go: set a tool's Risk in its mcpx.Tool literal",
		"invoice/adapters/invoicemcp/t.go: import mcpx by name",
	)
	expectViolations(t, mcpAdapterViolations(files),
		"order/adapters/ordermcp/t.go: never a Repository",
	)
	expectViolations(t, mcpImportViolations(files),
		"order/adapters/ordermcp/t.go: order/adapters/ordermcp imports github.com/modelcontextprotocol/go-sdk/mcp",
		"mcpx/auth.go: mcpx imports github.com/gofiber/fiber/v2",
		"mcpx/auth.go: mcpx imports github.com/Abraxas-365/iamkit/sdk/resourceserver",
	)
}
