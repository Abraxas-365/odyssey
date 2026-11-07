package bootstrap_test

// IT's rules (3a) rules (AGENTS.md "Rules"):
//   - only the policycel adapter imports cel-go: the policy domain and its
//     service know the Engine port, not the expression language;
//   - release, resource, ai and project never import policy: each asks
//     through its own Policy port, which an adapter in policy/adapters
//     implements;
//   - those adapters (and the policy assembler) are wired only in bootstrap;
//   - the governed services ask IT's rules before a transaction, never inside
//     one (authorize / AuthorizeConnect in a `.Do(func…)` literal).

import (
	"go/ast"
	"path"
	"strconv"
	"strings"
	"testing"
)

const (
	celSDK        = "github.com/google/cel-go"
	celAdapter    = "policy/adapters/policycel"
	policyPackage = modulePath + "policy"
)

// governed are the domains IT's rules judge: they must not know policy.
var governed = []string{"release", "resource", "ai", "project"}

// governedServices are the services that ask IT's rules and hold approvals.
var governedServices = map[string]bool{
	"release/releasesvc": true, "resource/resourcesvc": true, "ai/aisvc": true, "project/projectsvc": true,
}

func TestCELIsOnlyInItsAdapter(t *testing.T) { report(t, celViolations(sources(t, true))) }

func celViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == celAdapter {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == celSDK || strings.HasPrefix(p, celSDK+"/") {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+p+"; only "+celAdapter+" knows the expression language")
			}
		}
	}
	return out
}

func TestGovernedDomainsDoNotKnowPolicy(t *testing.T) {
	report(t, governedViolations(sources(t, true)))
}

func governedViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if !inGoverned(dir) {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == policyPackage || strings.HasPrefix(p, policyPackage+"/") {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
					strings.TrimPrefix(p, modulePath)+"; it asks IT's rules through its own Policy port (an adapter in policy/adapters implements it)")
			}
		}
	}
	return out
}

func inGoverned(dir string) bool {
	for _, d := range governed {
		if dir == d || strings.HasPrefix(dir, d+"/") {
			return true
		}
	}
	return false
}

func TestPolicyAdaptersAreWiredOnlyInBootstrap(t *testing.T) {
	report(t, policyWiringViolations(sources(t, true)))
}

// policyWiringViolations: what lives under policy/adapters and policy/policymodule
// is imported by the composition root or by policy itself, nobody else.
func policyWiringViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir == "policy" || strings.HasPrefix(dir, "policy/") || strings.HasPrefix(dir, "bootstrap") {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, policyPackage+"/adapters/") || p == policyPackage+"/policymodule" {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+" imports "+
					strings.TrimPrefix(p, modulePath)+"; policy adapters are wired only in bootstrap")
			}
		}
	}
	return out
}

func TestPolicyRulesCatchViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		"policy/adapters/policycel/e.go":      `package policycel; import _ "` + celSDK + `"`,
		"policy/policysvc/s.go":               `package policysvc; import _ "` + celSDK + `/cel"`,
		"release/releasesvc/p.go":             `package releasesvc; import _ "` + m + `policy"`,
		"resource/adapters/resourcehttp/h.go": `package resourcehttp; import _ "` + m + `policy/adapters/policyresource"`,
		"ai/aisvc/s.go":                       `package aisvc; import _ "` + m + `gate"`,
		"policy/adapters/policyai/p.go":       `package policyai; import _ "` + m + `policy"; import _ "` + m + `ai"`,
		"project/projectsvc/p.go":             `package projectsvc; import _ "` + m + `policy/adapters/policyproject"`,
		"bootstrap/bootstrap.go":              `package bootstrap; import _ "` + m + `policy/policymodule"; import _ "` + m + `policy/adapters/policyai"`,
		"audit/auditsvc/s.go":                 `package auditsvc; import _ "` + m + `policy/policymodule"`,
	})
	expectViolations(t, celViolations(files), "policy/policysvc/s.go: policy/policysvc imports "+celSDK+"/cel")
	expectViolations(t, governedViolations(files), "release/releasesvc/p.go: release/releasesvc imports policy",
		"resource/adapters/resourcehttp/h.go: resource/adapters/resourcehttp imports policy/adapters/policyresource",
		"project/projectsvc/p.go: project/projectsvc imports policy/adapters/policyproject")
	expectViolations(t, policyWiringViolations(files),
		"resource/adapters/resourcehttp/h.go: resource/adapters/resourcehttp imports policy/adapters/policyresource",
		"project/projectsvc/p.go: project/projectsvc imports policy/adapters/policyproject",
		"audit/auditsvc/s.go: audit/auditsvc imports policy/policymodule")
}

// IT's rules are asked BEFORE a transaction, never inside one: a refusal (and,
// in 3b, the approval request a refusal stores) must not live in a transaction
// that rolls back on that same refusal. The governed services therefore never
// call authorize / AuthorizeConnect inside a function literal handed to a
// `.Do(...)` (their tx.Do).
func TestITRulesAreNotAskedInsideATransaction(t *testing.T) {
	report(t, authorizeInTxViolations(sources(t, false)))
}

// txAskers are the calls that ask IT's rules.
var txAskers = map[string]bool{"authorize": true, "AuthorizeConnect": true}

func authorizeInTxViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if !governedServices[dir] {
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
					name := ""
					switch fn := inner.Fun.(type) {
					case *ast.SelectorExpr:
						name = fn.Sel.Name
					case *ast.Ident:
						name = fn.Name
					}
					if txAskers[name] {
						out = append(out, f.fset.Position(inner.Pos()).String()+": "+f.rel+" calls "+name+
							" inside a transaction; IT's rules are asked before it (a refusal must not live in a transaction it rolls back)")
					}
					return true
				})
			}
			return true
		})
	}
	return out
}

func TestITRulesInTxRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"release/releasesvc/ok.go":    `package releasesvc; func (s *S) A() { s.authorize(nil); s.tx.Do(nil, func(ctx C) error { return s.repo.X() }) }`,
		"release/releasesvc/bad.go":   `package releasesvc; func (s *S) B() { s.tx.Do(nil, func(ctx C) error { return s.authorize(ctx) }) }`,
		"resource/resourcesvc/bad.go": `package resourcesvc; func (s *S) C() { s.tx.Do(nil, func(ctx C) error { return s.usage.AuthorizeConnect(ctx) }) }`,
		"ai/aisvc/ok.go":              `package aisvc; func (s *S) D() { s.authorize(nil); s.tx.Do(nil, func(ctx C) error { return nil }) }`,
		"project/projectsvc/bad.go":   `package projectsvc; func (s *S) E() { s.tx.Do(nil, func(ctx C) error { return s.authorize(ctx) }) }`,
		"fleet/fleetsvc/other.go":     `package fleetsvc; func (s *S) F() { s.tx.Do(nil, func(ctx C) error { return s.authorize(ctx) }) }`,
	})
	expectViolations(t, authorizeInTxViolations(files),
		"release/releasesvc/bad.go: release/releasesvc/bad.go calls authorize inside a transaction",
		"resource/resourcesvc/bad.go: resource/resourcesvc/bad.go calls AuthorizeConnect inside a transaction",
		"project/projectsvc/bad.go: project/projectsvc/bad.go calls authorize inside a transaction")
}
