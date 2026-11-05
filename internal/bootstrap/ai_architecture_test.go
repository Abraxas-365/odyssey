package bootstrap_test

// AI rules (AGENTS.md "Rules"):
//
//  1. An app's AI key exists in clear only inside `aisvc.createKey`, between
//     the gateway making it and the sealing to the cluster: nothing else in
//     aisvc reads `ProviderKey.Secret` (so it cannot be stored, logged, put
//     in an event or in a workflow output).
//  2. Requests never call the gateway: `Enable`, `Rotate`, `Disable`, `Get`
//     and their helpers (the sweep included) do not touch `s.provider`'s keys. The gateway is only reached
//     from workflow steps, with no lock held and with retries.
//  3. Changes of an environment's AI serialise on `ai.Repository.LockEnvironment`:
//     `Enable`, `Rotate`, `Disable`, the `apply` step, the sweep's `retryRevocation`
//     and the failure hooks (`revokeFailed`, `makeFailed`) all call it.
//
// AGENTS.md rows:
//
//	An app's AI key exists in clear only inside `aisvc.createKey`: no other function in `aisvc` reads a provider key's `Secret`
//	| `TestAIKeyStaysInCreateKey`
//	The AI gateway is called only from workflow steps (never from `Enable`/`Rotate`/`Disable`/`Get`)
//	| `TestAIGatewayIsOnlyCalledFromWorkflowSteps`
//	Changes of an environment's AI serialise on `LockEnvironment`
//	| `TestAIChangesAreSerialised`

import (
	"go/ast"
	"path"
	"sort"
	"strings"
	"testing"
)

// aiGatewayUsers documents which aisvc functions reach the gateway: the
// workflow steps and what they call (the rule itself forbids it in requests).

var aiRequestFuncs = map[string]bool{"Enable": true, "Rotate": true, "Disable": true, "Get": true, "view": true, "begin": true, "place": true, "refusal": true,
	"Sweep": true, "retryRevocation": true, "startRevocation": true}

// selectors reports whether body mentions .name (a field or method).
func selectors(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func aiViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) != "ai/aisvc" || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := f.fset.Position(fn.Pos()).String() + ": " + fn.Name.Name
			if fn.Name.Name != "createKey" && selectors(fn.Body, "Secret") {
				out = append(out, where+" reads a provider key's Secret; the clear key lives only in createKey")
			}
			if aiRequestFuncs[fn.Name.Name] && (selectors(fn.Body, "CreateKey") || selectors(fn.Body, "RevokeKey") || selectors(fn.Body, "ListKeys")) {
				out = append(out, where+" calls the AI gateway; only workflow steps may")
			}
		}
	}
	sort.Strings(out)
	return out
}

var aiLockChain = []mustCall{
	{"ai/aisvc", "Enable", "LockEnvironment"},
	{"ai/aisvc", "Rotate", "LockEnvironment"},
	{"ai/aisvc", "Disable", "LockEnvironment"},
	{"ai/aisvc", "apply", "LockEnvironment"},
	{"ai/aisvc", "retryRevocation", "LockEnvironment"},
	{"ai/aisvc", "revokeFailed", "LockEnvironment"},
	{"ai/aisvc", "makeFailed", "LockEnvironment"},
}

func TestAIKeyStaysInCreateKey(t *testing.T) {
	report(t, filterAI(aiViolations(sources(t, false)), "Secret"))
}

func TestAIGatewayIsOnlyCalledFromWorkflowSteps(t *testing.T) {
	report(t, filterAI(aiViolations(sources(t, false)), "gateway"))
}

func filterAI(all []string, kind string) []string {
	var out []string
	for _, v := range all {
		if strings.Contains(v, "Secret") == (kind == "Secret") {
			out = append(out, v)
		}
	}
	return out
}

func TestAIChangesAreSerialised(t *testing.T) {
	report(t, resourceLockViolations(sources(t, false), aiLockChain))
}

func TestAIRulesCatchViolations(t *testing.T) {
	good := parseFixtures(t, map[string]string{
		"ai/aisvc/a.go": `package aisvc
func (s *S) createKey() { k, _ := s.provider.CreateKey(nil, ""); seal(k.Secret) }
func (s *S) Enable()    { s.repo.LockEnvironment(nil) }
func (s *S) Rotate()    { s.repo.LockEnvironment(nil) }
func (s *S) Disable()   { s.repo.LockEnvironment(nil) }
func (s *S) apply()     { s.repo.LockEnvironment(nil) }
func (s *S) retryRevocation() { s.repo.LockEnvironment(nil) }
func (s *S) revokeFailed() { s.repo.LockEnvironment(nil) }
func (s *S) makeFailed() { s.repo.LockEnvironment(nil) }
func (s *S) revokeAll() { s.provider.RevokeKey(nil, "") }`,
	})
	if v := aiViolations(good); len(v) != 0 {
		t.Fatalf("correct code was flagged:\n%s", strings.Join(v, "\n"))
	}
	if v := resourceLockViolations(good, aiLockChain); len(v) != 0 {
		t.Fatalf("correct locks were flagged:\n%s", strings.Join(v, "\n"))
	}
	bad := parseFixtures(t, map[string]string{
		"ai/aisvc/a.go": `package aisvc
func (s *S) apply()     { log(k.Secret) }
func (s *S) Enable()    { s.provider.CreateKey(nil, "") }
func (s *S) Rotate()    { s.repo.Create(nil) }
func (s *S) Disable()   { s.repo.LockEnvironment(nil) }
func (s *S) retryRevocation() { s.repo.LockEnvironment(nil) }
func (s *S) revokeFailed() { s.repo.LockEnvironment(nil) }
func (s *S) makeFailed() { s.repo.LockEnvironment(nil) }`,
	})
	expectViolations(t, aiViolations(bad),
		"ai/aisvc/a.go: apply reads a provider key's Secret",
		"ai/aisvc/a.go: Enable calls the AI gateway")
	expectViolations(t, resourceLockViolations(bad, aiLockChain),
		"ai/aisvc/a.go: Enable does not call LockEnvironment",
		"ai/aisvc/a.go: Rotate does not call LockEnvironment",
		"ai/aisvc/a.go: apply does not call LockEnvironment")
}
