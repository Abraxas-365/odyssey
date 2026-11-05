package bootstrap_test

// Approvals (3b) rules (AGENTS.md "Approvals"):
//   - release, resource, ai and source never import approval: a rule that
//     wants approval opens its request through the domain's own Approvals
//     port, which an adapter in approval/adapters implements;
//   - the approval domain itself (types, service, repository, HTTP, MCP) does
//     not know the governed domains: only the per-domain bridges
//     (approvalrelease, approvalresource, approvalai) and the assembler
//     (approvalmodule) import them;
//   - approval's adapters and assembler are wired only in bootstrap.

import (
	"go/ast"
	"path"
	"strconv"
	"strings"
	"testing"
)

const approvalPackage = modulePath + "approval"

// approvalBridges are the only approval packages that may know a governed
// domain.
var approvalBridges = map[string]bool{
	"approval/adapters/approvalrelease":  true,
	"approval/adapters/approvalresource": true,
	"approval/adapters/approvalai":       true,
	"approval/approvalmodule":            true,
}

func TestGovernedDomainsDoNotKnowApproval(t *testing.T) {
	report(t, governedApprovalViolations(sources(t, true)))
}

func governedApprovalViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if !inGoverned(dir) && dir != "source" && !strings.HasPrefix(dir, "source/") {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == approvalPackage || strings.HasPrefix(p, approvalPackage+"/") {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
					strings.TrimPrefix(p, modulePath)+"; it holds an action for approval through its own Approvals port (an adapter in approval/adapters implements it)")
			}
		}
	}
	return out
}

func TestApprovalDoesNotKnowTheGovernedDomains(t *testing.T) {
	report(t, approvalCouplingViolations(sources(t, true)))
}

func approvalCouplingViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if (dir != "approval" && !strings.HasPrefix(dir, "approval/")) || approvalBridges[dir] {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, d := range governed {
				if p == modulePath+d || strings.HasPrefix(p, modulePath+d+"/") {
					out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
						strings.TrimPrefix(p, modulePath)+"; only the bridges (approvalrelease, approvalresource, approvalai) and approvalmodule know a governed domain")
				}
			}
		}
	}
	return out
}

func TestApprovalAdaptersAreWiredOnlyInBootstrap(t *testing.T) {
	report(t, approvalWiringViolations(sources(t, true)))
}

// approvalWiringViolations: what lives under approval/adapters and
// approval/approvalmodule is imported by the composition root or by approval
// itself, nobody else.
func approvalWiringViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == "approval" || strings.HasPrefix(dir, "approval/") || strings.HasPrefix(dir, "bootstrap") {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, approvalPackage+"/adapters/") || p == approvalPackage+"/approvalmodule" {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
					strings.TrimPrefix(p, modulePath)+"; approval adapters are wired only in bootstrap")
			}
		}
	}
	return out
}

func TestApprovalRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		"release/releasesvc/a.go":                 `package releasesvc; import _ "` + m + `approval"`,
		"source/sourcesvc/a.go":                   `package sourcesvc; import _ "` + m + `approval/adapters/approvalpg"`,
		"gate/g.go":                               `package gate; import _ "` + m + `errx"`,
		"approval/approvalsvc/s.go":               `package approvalsvc; import _ "` + m + `release"`,
		"approval/adapters/approvalhttp/h.go":     `package approvalhttp; import _ "` + m + `resource/resourcesvc"`,
		"approval/adapters/approvalrelease/r.go":  `package approvalrelease; import _ "` + m + `release"; import _ "` + m + `approval"`,
		"approval/approvalmodule/m.go":            `package approvalmodule; import _ "` + m + `ai"`,
		"audit/auditsvc/s.go":                     `package auditsvc; import _ "` + m + `approval/approvalmodule"`,
		"bootstrap/bootstrap.go":                  `package bootstrap; import _ "` + m + `approval/approvalmodule"; import _ "` + m + `approval/adapters/approvalmcp"`,
		"resource/adapters/resourcehttp/h.go":     `package resourcehttp; import _ "` + m + `gate"`,
		"approval/adapters/approvalresource/r.go": `package approvalresource; import _ "` + m + `resource"`,
	})
	expectViolations(t, governedApprovalViolations(files),
		"release/releasesvc/a.go: release/releasesvc imports approval",
		"source/sourcesvc/a.go: source/sourcesvc imports approval/adapters/approvalpg")
	expectViolations(t, approvalCouplingViolations(files),
		"approval/approvalsvc/s.go: approval/approvalsvc imports release",
		"approval/adapters/approvalhttp/h.go: approval/adapters/approvalhttp imports resource/resourcesvc")
	expectViolations(t, approvalWiringViolations(files),
		"source/sourcesvc/a.go: source/sourcesvc imports approval/adapters/approvalpg",
		"audit/auditsvc/s.go: audit/auditsvc imports approval/approvalmodule")
}

// Holding an action opens a request in its own transaction, so the governed
// services open it before theirs and never inside one (a refusal elsewhere in
// the same transaction would roll the request back): the Approvals port's
// Hold (`<x>.approvals.Hold`) is not called inside a function literal handed
// to `.Do(...)`. The one exception is the deployment workflow's authorize step
// (release/releasesvc/approval.go): the verdict is already in, and the request
// must be atomic with the deployment's waiting_approval status.
func TestApprovalsAreNotHeldInsideATransaction(t *testing.T) {
	report(t, holdInTxViolations(sources(t, false)))
}

func holdInTxViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if (dir != "release/releasesvc" && dir != "resource/resourcesvc" && dir != "ai/aisvc") || f.rel == "release/releasesvc/approval.go" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Do" {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.FuncLit)
				if !ok {
					continue
				}
				ast.Inspect(lit.Body, func(m ast.Node) bool {
					inner, ok := m.(*ast.CallExpr)
					if !ok {
						return true
					}
					if fn, ok := inner.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "Hold" && isApprovals(fn.X) {
						out = append(out, f.fset.Position(inner.Pos()).String()+": "+f.rel+
							" calls Hold inside a transaction; the approval request is opened before it")
					}
					return true
				})
			}
			return true
		})
	}
	return out
}

// isApprovals reports whether x is `approvals` or `<something>.approvals`.
func isApprovals(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name == "approvals"
	case *ast.SelectorExpr:
		return v.Sel.Name == "approvals"
	}
	return false
}

func TestApprovalHoldRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"release/releasesvc/ok.go":  `package releasesvc; func (s *S) A() { s.approvals.Hold(nil); s.tx.Do(nil, func(ctx C) error { return nil }) }`,
		"release/releasesvc/bad.go": `package releasesvc; func (s *S) B() { s.tx.Do(nil, func(ctx C) error { return s.approvals.Hold(ctx) }) }`,
		"project/projectsvc/o.go":   `package projectsvc; func (s *S) C() { s.tx.Do(nil, func(ctx C) error { return s.x.Hold(ctx) }) }`,
	})
	expectViolations(t, holdInTxViolations(files),
		"release/releasesvc/bad.go: release/releasesvc/bad.go calls Hold inside a transaction")
}
