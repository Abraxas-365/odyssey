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
//  3. The cluster is told about an app (Cluster.Apply, Cluster.Retire) only
//     while the project is held: in release/releasesvc a function that calls
//     cluster.Apply or cluster.Retire calls projects.Hold before it (in the
//     same transaction). That is what keeps an archive and a deployment from
//     crossing: either the deployment reached the cluster first and the
//     retirement is above it, or it sees the archive and stops.
//  4. The organization's directory (release.People) is asked in one place:
//     release/releasesvc's NameRequester, the deployment.started handler,
//     outside any transaction. A deployment never waits on (or fails with)
//     the directory, and the name is a snapshot the console only shows.

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

// clusterCalls are the release.Cluster methods that change what a cluster runs.
var clusterCalls = map[string]bool{"Apply": true, "Retire": true}

// on reports whether call is <owner>.<method> (s.cluster.Apply, s.projects.Hold).
func on(call *ast.CallExpr, owner string, methods map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !methods[sel.Sel.Name] {
		return false
	}
	switch x := sel.X.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == owner
	case *ast.Ident:
		return x.Name == owner
	}
	return false
}

func heldClusterViolations(files []goFile) []string {
	var out []string
	hold := map[string]bool{"Hold": true}
	for _, f := range files {
		if path.Dir(f.rel) != releaseSvc || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			held := token.NoPos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if on(call, "projects", hold) && (held == token.NoPos || call.Pos() < held) {
					held = call.Pos()
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && on(call, "cluster", clusterCalls) && (held == token.NoPos || call.Pos() < held) {
					out = append(out, f.fset.Position(call.Pos()).String()+": "+fn.Name.Name+
						" tells the cluster without holding the project first (projects.Hold); an archive could cross it")
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestTheClusterIsToldOnlyWhileTheProjectIsHeld(t *testing.T) {
	report(t, heldClusterViolations(sources(t, false)))
}

func TestHeldClusterRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/ok.go": `package releasesvc
func (s *S) handOver() { s.tx.Do(ctx, func(ctx C) error { s.projects.Hold(ctx); return s.cluster.Apply(ctx) }) }
func (s *S) retire() { s.tx.Do(ctx, func(ctx C) error { s.projects.Hold(ctx); return s.cluster.Retire(ctx) }) }`,
		releaseSvc + "/x_test.go":            `package releasesvc; func T() { s.cluster.Apply(nil) }`,
		"release/adapters/releasefleet/c.go": `package releasefleet; func (c *C) Apply() { c.fleet.Apply(nil) }`,
		// violations
		releaseSvc + "/none.go": `package releasesvc
func (s *S) push() { s.cluster.Apply(nil) }`,
		releaseSvc + "/late.go": `package releasesvc
func (s *S) gone() { s.cluster.Retire(nil); s.projects.Hold(nil) }`,
		releaseSvc + "/find.go": `package releasesvc
func (s *S) found() { s.projects.Find(nil); s.cluster.Apply(nil) }`,
	})
	expectViolations(t, heldClusterViolations(files),
		releaseSvc+"/find.go: found tells the cluster without holding",
		releaseSvc+"/late.go: gone tells the cluster without holding",
		releaseSvc+"/none.go: push tells the cluster without holding",
	)
}

// peopleAsker is the only function that asks the directory.
const peopleAsker = "NameRequester"

// peopleViolations: a call to <x>.people.Name anywhere in release outside
// NameRequester, or inside a transaction (tx.Do's function literal).
func peopleViolations(files []goFile) []string {
	var out []string
	asks := func(n ast.Node) (ast.Node, bool) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Name" {
			return nil, false
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		return call, ok && inner.Sel.Name == "people"
	}
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != "release" && !strings.HasPrefix(dir, "release/") || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := asks(n); ok && (dir != releaseSvc || fn.Name.Name != peopleAsker) {
					out = append(out, f.fset.Position(call.Pos()).String()+": "+fn.Name.Name+
						" asks the directory; only releasesvc."+peopleAsker+" (the deployment.started handler) may")
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Do" {
					for _, arg := range call.Args {
						if lit, ok := arg.(*ast.FuncLit); ok {
							ast.Inspect(lit.Body, func(m ast.Node) bool {
								if inner, ok := asks(m); ok {
									out = append(out, f.fset.Position(inner.Pos()).String()+": "+fn.Name.Name+
										" asks the directory inside a transaction")
								}
								return true
							})
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func TestTheDirectoryIsAskedOutsideTheRequest(t *testing.T) {
	report(t, peopleViolations(sources(t, false)))
}

// messageWriters are the only functions that set a deployment's message: the
// two that start a deployment from what someone asked for (UX v4 §5.3).
var messageWriters = map[string]bool{"Deploy": true, "startSource": true}

// messageViolations: a release.Deployment literal with a Message key outside
// messageWriters (a rollback, a restore or an access change has no words of
// its own), or whose Message is a string literal (the platform does not put
// words in someone's mouth).
func messageViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if dir != "release" && !strings.HasPrefix(dir, "release/") || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isDeploymentType(lit.Type) {
					return true
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if key, isID := kv.Key.(*ast.Ident); !ok || !isID || key.Name != "Message" {
						continue
					}
					pos := f.fset.Position(kv.Pos()).String()
					if dir != releaseSvc || !messageWriters[fn.Name.Name] {
						out = append(out, pos+": "+fn.Name.Name+" sets a deployment's message; only releasesvc.Deploy and startSource may")
					}
					if _, isLit := kv.Value.(*ast.BasicLit); isLit {
						out = append(out, pos+": "+fn.Name.Name+" writes a deployment's message itself; it is the requester's words")
					}
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

// isDeploymentType: release.Deployment, or Deployment inside package release.
func isDeploymentType(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && x.Name == "release" && t.Sel.Name == "Deployment"
	case *ast.Ident:
		return t.Name == "Deployment"
	}
	return false
}

func TestOnlyTheRequesterNamesAVersion(t *testing.T) {
	report(t, messageViolations(sources(t, false)))
}

func TestMessageRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/ok.go":      `package releasesvc; func (s *S) Deploy() { _ = release.Deployment{Message: input.Message} }`,
		releaseSvc + "/src.go":     `package releasesvc; func (s *S) startSource() { _ = release.Deployment{Reason: r, Message: a.message} }`,
		releaseSvc + "/ok_test.go": `package releasesvc; func T() { _ = release.Deployment{Message: "x"} }`,
		releaseSvc + "/other.go":   `package releasesvc; func (s *S) Rollback() { _ = release.Failure{Message: m} }`,
		// violations
		releaseSvc + "/back.go":   `package releasesvc; func (s *S) Rollback() { _ = release.Deployment{Message: old.Message} }`,
		releaseSvc + "/invent.go": `package releasesvc; func (s *S) Deploy() { _ = release.Deployment{Message: "Nueva versión"} }`,
		"release/adapters/x/h.go": `package x; func (h *H) f() { _ = release.Deployment{Message: m} }`,
		"release/deployment_x.go": `package release; func g() { _ = Deployment{Message: m} }`,
	})
	expectViolations(t, messageViolations(files),
		"release/adapters/x/h.go: f sets a deployment's message; only releasesvc.Deploy and startSource may",
		"release/deployment_x.go: g sets a deployment's message; only releasesvc.Deploy and startSource may",
		releaseSvc+"/back.go: Rollback sets a deployment's message; only releasesvc.Deploy and startSource may",
		releaseSvc+"/invent.go: Deploy writes a deployment's message itself; it is the requester's words",
	)
}

func TestDirectoryRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		// correct
		releaseSvc + "/ok.go":      `package releasesvc; func (s *S) NameRequester() { n, _ := s.people.Name(ctx, o, u); s.repo.ObserveRequesterName(ctx, o, d, n) }`,
		releaseSvc + "/ok_test.go": `package releasesvc; func T() { s.people.Name(nil, o, u) }`,
		releaseSvc + "/user.go":    `package releasesvc; func (s *S) f() { s.user.Name(nil) }`,
		// violations
		releaseSvc + "/begin.go": `package releasesvc; func (s *S) begin() { s.people.Name(ctx, o, u) }`,
		releaseSvc + "/tx.go": `package releasesvc
func (s *S) NameRequester() { s.tx.Do(ctx, func(ctx C) error { s.people.Name(ctx, o, u); return nil }) }`,
		"release/adapters/releasehttp/h.go": `package releasehttp; func (h *H) list() { h.people.Name(ctx, o, u) }`,
	})
	expectViolations(t, peopleViolations(files),
		releaseSvc+"/begin.go: begin asks the directory; only releasesvc.NameRequester",
		releaseSvc+"/tx.go: NameRequester asks the directory inside a transaction",
		"release/adapters/releasehttp/h.go: list asks the directory; only releasesvc.NameRequester",
	)
}
