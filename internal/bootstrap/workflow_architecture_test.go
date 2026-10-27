package bootstrap_test

// Workflow rules (AGENTS.md "Rules"):
//
//  1. River is an implementation detail of workflow/adapters/workflowpg:
//     nothing else imports github.com/riverqueue/... (domains depend on the
//     workflow port, so the engine can be swapped, e.g. for Temporal).
//  2. Only the composition root (bootstrap, *module) builds the registry and
//     registers workflows and schedules; domains and services declare
//     workflow.Definition values and never decide what runs in the process.

import (
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go/ast"
)

const (
	workflowPkg   = modulePath + "workflow"
	workflowAdapt = "workflow/adapters/workflowpg"
	riverModule   = "github.com/riverqueue/"
)

func riverViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == workflowAdapt {
			continue
		}
		for _, imp := range f.file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(p, riverModule) {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+p+
					"; River stays inside "+workflowAdapt+" (depend on the workflow port)")
			}
		}
	}
	sort.Strings(out)
	return out
}

func registrationViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		role := roleOf(dir)
		if role.kind == "wiring" || role.kind == "module" || role.kind == "foundation" {
			continue
		}
		name := importName(f.file, workflowPkg)
		if name == "" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, isIdent := sel.X.(*ast.Ident)
			switch {
			case isIdent && x.Name == name && sel.Sel.Name == "NewRegistry",
				sel.Sel.Name == "Define" || sel.Sel.Name == "Every":
				out = append(out, f.fset.Position(call.Pos()).String()+": "+role.kind+" "+dir+
					" must not register workflows ("+sel.Sel.Name+"); return workflow.Definition values and register them in the composition root")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestRiverStaysInTheAdapter(t *testing.T) { report(t, riverViolations(sources(t, true))) }
func TestOnlyTheCompositionRootRegistersWorkflows(t *testing.T) {
	report(t, registrationViolations(sources(t, false)))
}

func TestWorkflowRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		// correct
		"workflow/adapters/workflowpg/e.go": `package workflowpg; import _ "github.com/riverqueue/river"`,
		"bootstrap/wire.go": `package bootstrap; import "` + m + `workflow"
func w() { r := workflow.NewRegistry(); r.Define(workflow.Definition{}); r.Every(workflow.Schedule{}) }`,
		"widget/widgetmodule/m.go": `package widgetmodule; import "` + m + `workflow"
func (m Module) Register(r *workflow.Registry) { r.Define(m.svc.Deploy()) }`,
		"widget/widgetsvc/w.go": `package widgetsvc; import "` + m + `workflow"
func (s *S) Deploy() workflow.Definition { return workflow.Definition{Name: "widget.deploy"} }`,

		// violations
		"order/ordersvc/s.go": `package ordersvc; import (wf "` + m + `workflow"; _ "github.com/riverqueue/river/rivertype")
func (s *S) Init() { r := wf.NewRegistry(); r.Define(wf.Definition{}) }`,
	})
	expectViolations(t, riverViolations(files), "order/ordersvc/s.go: order/ordersvc imports github.com/riverqueue/river/rivertype")
	expectViolations(t, registrationViolations(files),
		"order/ordersvc/s.go: svc order/ordersvc must not register workflows (NewRegistry)",
		"order/ordersvc/s.go: svc order/ordersvc must not register workflows (Define)")
}
