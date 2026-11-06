package bootstrap_test

// Account rules (AGENTS.md "Rules"): the user's own bearer token leaves the
// API in one place. httpx.BearerToken (the raw token, for calling IAMKit on
// the user's behalf) is called only by account/adapters/accounthttp, and the
// IAMKit self-service calls that take it (ListConnectedApps, DisconnectApp)
// only by account/adapters/accountiam. Everything else acts on verified
// claims; a token handed to another adapter could be replayed anywhere the
// user can go. Tests are free to call them.

import (
	"go/ast"
	"path"
	"sort"
	"strings"
	"testing"
)

const accountHTTP, accountIAM = "account/adapters/accounthttp", "account/adapters/accountiam"

// bearerCalls: who may call each of them.
var bearerCalls = map[string]string{
	"BearerToken":       accountHTTP,
	"ListConnectedApps": accountIAM,
	"DisconnectApp":     accountIAM,
}

func bearerViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if strings.HasSuffix(f.rel, "_test.go") {
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
			if owner, ok := bearerCalls[sel.Sel.Name]; ok && dir != owner {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+dir+" calls "+sel.Sel.Name+
					"; the user's own token is used only by "+owner+" (act on claims instead)")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestTheUsersTokenLeavesInOnePlace(t *testing.T) { report(t, bearerViolations(sources(t, false))) }

func TestBearerRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		accountHTTP + "/h.go":      `package accounthttp; func c() { httpx.BearerToken(nil) }`,
		accountIAM + "/c.go":       `package accountiam; func (c C) l() { c.API.ListConnectedApps(nil); c.API.DisconnectApp(nil) }`,
		"bootstrap/me_test.go":     `package bootstrap_test; func T() { httpx.BearerToken(nil) }`,
		"project/adapters/x/ok.go": `package x; func ok() { httpx.User(nil) }`,

		// violations
		"release/adapters/releasehttp/h.go": `package releasehttp; func h() { httpx.BearerToken(nil) }`,
		"account/accountsvc/s.go":           `package accountsvc; func (s *S) l() { s.api.ListConnectedApps(nil) }`,
		accountHTTP + "/d.go":               `package accounthttp; func d() { api.DisconnectApp(nil) }`,
	})
	expectViolations(t, bearerViolations(files),
		"release/adapters/releasehttp/h.go: release/adapters/releasehttp calls BearerToken",
		"account/accountsvc/s.go: account/accountsvc calls ListConnectedApps",
		accountHTTP+"/d.go: "+accountHTTP+" calls DisconnectApp",
	)
}
