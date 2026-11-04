package bootstrap_test

// Release rules (AGENTS.md "Rules"):
//
//  1. What an environment serves changes in one place: the deploy workflow's
//     promote step, after the new version answered its health check. In
//     release/releasesvc only the function promote calls the repository's
//     Promote; in release/adapters/releasepg only Promote writes
//     current_release_id. Every other path (deploy, rollback, failures) goes
//     through a deployment and its health check.
//  2. An upload ticket is spent in one place: the repository's cross-org
//     lookups by token hash (ConsumeUploadTicket, FindUploadTicketByHash;
//     the token is all the caller has) are called only by release/releasesvc
//     (declared in release and releasepg, not called there); and Commands.RedeemUpload, which
//     turns a ticket into a deployment, is called only by the public upload
//     route in release/adapters/releasehttp (never by an MCP tool, which acts
//     as an authenticated user). Tests are free to drive tickets.

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	releaseSvc     = "release/releasesvc"
	releasePg      = "release/adapters/releasepg"
	promoteStep    = "promote"
	promoteRepoFn  = "Promote"
	currentRelease = "current_release_id"
)

var writesCurrent = regexp.MustCompile(`(?i)` + currentRelease + `\s*=`)

func promoteViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != releaseSvc && dir != releasePg {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if dir == releaseSvc && ok && sel.Sel.Name == promoteRepoFn && fn.Name.Name != promoteStep {
						out = append(out, f.fset.Position(x.Pos()).String()+": "+fn.Name.Name+
							" calls Promote; only the deploy workflow's promote step changes what an environment serves")
					}
				case *ast.BasicLit:
					if dir == releasePg && x.Kind == token.STRING && writesCurrent.MatchString(x.Value) && fn.Name.Name != promoteRepoFn {
						out = append(out, f.fset.Position(x.Pos()).String()+": "+fn.Name.Name+
							" writes "+currentRelease+"; only Repository.Promote may")
					}
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestOnlyThePromoteStepMovesTheCurrentRelease(t *testing.T) {
	report(t, promoteViolations(sources(t, false)))
}

func TestReleaseRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/w.go": `package releasesvc
func (s *S) promote() { s.repo.Promote(nil) }`,
		releasePg + "/r.go": "package releasepg\nfunc (r *R) Promote() { r.db.Exec(`UPDATE release.environment_states SET current_release_id = $3`) }\n" +
			"func (r *R) Find() { r.db.Get(`SELECT current_release_id FROM release.environment_states`) }",
		// violations
		releaseSvc + "/s.go": `package releasesvc
func (s *S) Rollback() { s.repo.Promote(nil) }`,
		releasePg + "/x.go": "package releasepg\nfunc (r *R) Begin() { r.db.Exec(`UPDATE release.environment_states SET Current_Release_ID=$1`) }",
	})
	expectViolations(t, promoteViolations(files),
		releasePg+"/x.go: Begin writes current_release_id",
		releaseSvc+"/s.go: Rollback calls Promote")
}

// ticketLookups are the release.Repository methods that find a ticket by its
// token hash, without an organization.
var ticketLookups = map[string]bool{"ConsumeUploadTicket": true, "FindUploadTicketByHash": true}

const (
	redeemUpload       = "RedeemUpload"
	releaseHTTPPackage = "release/adapters/releasehttp"
)

func uploadTicketViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if strings.HasSuffix(f.rel, "_test.go") {
			continue // tests may read and drive tickets
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
			pos := f.fset.Position(call.Pos()).String()
			switch {
			case ticketLookups[sel.Sel.Name] && dir != releaseSvc:
				out = append(out, pos+": "+dir+" calls "+sel.Sel.Name+", which is not scoped by organization; only release/releasesvc may")
			case sel.Sel.Name == redeemUpload && dir != releaseHTTPPackage:
				out = append(out, pos+": "+dir+" calls "+redeemUpload+"; only the public upload route in "+releaseHTTPPackage+" redeems tickets")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// Environment lookups without an organization: the access sign-in knows only
// the environment id the gate sent, and finds the tenant here. The repository
// method is called only by release/releasesvc (FindAccessTarget), and
// FindAccessTarget only by the access service: nothing else may resolve a
// tenant from an id in a request.
var envLookups = map[string]string{
	"FindStateByEnvironment": releaseSvc,
	"FindAccessTarget":       "access/accesssvc",
}

func envLookupViolations(files []goFile) []string {
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
			if owner, ok := envLookups[sel.Sel.Name]; ok && dir != owner {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+dir+" calls "+sel.Sel.Name+
					", which finds an environment without its organization; only "+owner+" may")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func TestEnvironmentLookupsWithoutTenantStayInOnePlace(t *testing.T) {
	report(t, envLookupViolations(sources(t, false)))
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/a.go":     `package releasesvc; func (s *S) f() { s.repo.FindStateByEnvironment(nil, id) }`,
		"access/accesssvc/s.go":  `package accesssvc; func (s *S) f() { s.targets.FindAccessTarget(nil, id) }`,
		releasePg + "/r.go":      `package releasepg; func (r *R) FindStateByEnvironment() {}`,
		releasePg + "/r_test.go": `package releasepg_test; func T() { repo.FindStateByEnvironment(nil, id) }`,
		// violations
		"access/adapters/accesshttp/h.go":   `package accesshttp; func (h *H) f() { h.lookup.FindAccessTarget(nil, id) }`,
		"release/adapters/releasehttp/h.go": `package releasehttp; func (h *H) f() { h.repo.FindStateByEnvironment(nil, id) }`,
		"source/sourcesvc/s.go":             `package sourcesvc; func (s *S) f() { s.releases.FindAccessTarget(nil, id) }`,
	})
	expectViolations(t, envLookupViolations(files),
		"access/adapters/accesshttp/h.go: access/adapters/accesshttp calls FindAccessTarget, which finds an environment without its organization; only access/accesssvc may",
		"release/adapters/releasehttp/h.go: release/adapters/releasehttp calls FindStateByEnvironment, which finds an environment without its organization; only release/releasesvc may",
		"source/sourcesvc/s.go: source/sourcesvc calls FindAccessTarget, which finds an environment without its organization; only access/accesssvc may",
	)
}

func TestUploadTicketsAreSpentInOnePlace(t *testing.T) {
	report(t, uploadTicketViolations(sources(t, false)))
}

func TestUploadTicketRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/u.go":                      `package releasesvc; func (s *S) redeem() { s.repo.ConsumeUploadTicket(nil); s.repo.FindUploadTicketByHash(nil) }`,
		releaseHTTPPackage + "/h.go":              `package releasehttp; func (h *H) upload() { h.commands.RedeemUpload(nil) }`,
		releasePg + "/r.go":                       `package releasepg; func (r *R) ConsumeUploadTicket() {}`,
		releasePg + "/r_test.go":                  `package releasepg_test; func T() { repo.ConsumeUploadTicket(nil); repo.FindUploadTicketByHash(nil) }`,
		"bootstrap/server_test.go":                `package bootstrap_test; func T() { c.RedeemUpload(nil) }`,
		releaseHTTPPackage + "/h_test.go":         `package releasehttp_test; func T() { c.RedeemUpload(nil) }`,
		"release/releasemodule/m.go":              `package releasemodule; func New() { svc.IssueUpload(nil) }`,
		"project/adapters/projectmcp/other_ok.go": `package projectmcp; func (t *T) x() { t.commands.Create(nil) }`,

		// violations
		"release/adapters/releasemcp/t.go":      `package releasemcp; func (t *T) deploy() { t.commands.RedeemUpload(nil) }`,
		"bootstrap/b.go":                        `package bootstrap; func b() { p.Releases.Commands.RedeemUpload(nil) }`,
		releaseHTTPPackage + "/peek.go":         `package releasehttp; func (h *H) peek() { h.repo.FindUploadTicketByHash(nil) }`,
		"release/releasemodule/peek.go":         `package releasemodule; func peek() { repo.ConsumeUploadTicket(nil) }`,
		releasePg + "/self.go":                  `package releasepg; func (r *R) x() { r.FindUploadTicketByHash(nil) }`,
		"release/release.go":                    `package release; func x() { repo.ConsumeUploadTicket(nil) }`,
		"source/adapters/sourcerelease/peek.go": `package sourcerelease; func (d D) peek() { d.repo.ConsumeUploadTicket(nil) }`,
	})
	expectViolations(t, uploadTicketViolations(files),
		"release/adapters/releasemcp/t.go: release/adapters/releasemcp calls RedeemUpload",
		"bootstrap/b.go: bootstrap calls RedeemUpload",
		releaseHTTPPackage+"/peek.go: "+releaseHTTPPackage+" calls FindUploadTicketByHash, which is not scoped by organization",
		"release/releasemodule/peek.go: release/releasemodule calls ConsumeUploadTicket, which is not scoped by organization",
		releasePg+"/self.go: "+releasePg+" calls FindUploadTicketByHash, which is not scoped",
		"release/release.go: release calls ConsumeUploadTicket, which is not scoped",
		"source/adapters/sourcerelease/peek.go: source/adapters/sourcerelease calls ConsumeUploadTicket, which is not scoped by organization",
	)
}
