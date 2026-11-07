package releasesvc

import (
	"context"
	"log/slog"
	"slices"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// authorizeScanStep is the deploy workflow's second gate (3c): right after
// the build, IT's rules judge the deployment again with the image's
// vulnerability scan in `scan.*`.
//
// The build does not wait for the scan (the build.scan workflow follows it
// and records it on the build); this step does, up to release.ScanWait after
// the build finished. A scan that never came, one that could not be done and
// a cluster that does not scan (a build whose runner lists no
// `odicea.scan`) all judge as scan.known = false: an unknown scan is not a
// clean one, and rules that care say `scan.known && …`. A deployment of an
// image (deploy_image) is never scanned: nothing was built here, so it has no
// scan and its first judgment (before anything ran) is the only one.
//
// The verdict is the authorize step's, with the same consequences:
//
//   - deny: the deployment fails, POLICY_DENIED, naming the rule;
//   - approval: the request is stored (run + step authorize_scan) and the run
//     parks for the decision (approval_scan acts on it) — but only when a
//     rule asks that did not already ask at authorize: someone who approved
//     "production needs approval" is not asked the same thing again because
//     the scan was clean;
//   - otherwise it goes on.
func (s *Service) authorizeScanStep(ctx context.Context, st *workflow.State) (any, error) {
	var in release.DeployInput
	if err := st.Input(&in); err != nil {
		return nil, workflow.Fail(err)
	}
	j, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	d, org := j.deployment, j.target.Organization
	action, ok := judged(d.Reason)
	if !ok || d.Build == nil || in.Actor.IsZero() {
		return gate.Decision{Status: gate.DecisionAllowed}, nil
	}
	if s.policy == nil {
		return nil, errx.Internal("IT's rules are not wired")
	}
	// The build recorded the release and moved on to apply; the gate is still
	// part of the build as far as the console goes ("build" running), so a
	// refusal reads as the build's and nothing says the cluster was reached.
	if err := s.progress(ctx, j, release.StatusBuilding, release.StepBuild); err != nil {
		return nil, err
	}
	scan, partial, wait, err := s.scanOfAll(ctx, d)
	if err != nil {
		return nil, err
	}
	if wait {
		return nil, workflow.Poll(s.opts.PollEvery)
	}
	p, e, err := s.place(ctx, org, d.Project, d.Environment)
	if err != nil {
		return nil, err
	}
	ctx = actor.With(ctx, in.Actor) // judged, and the request held, as who asked
	// before is what authorize judged (the layout: nothing was built yet);
	// now the version exists, so `changed` also names the services whose
	// code changed.
	asked := gate.Request{Action: action, Tier: string(p.Tier), ProjectSlug: p.Slug, Environment: e.Slug}
	unbuilt := d
	if len(d.Builds) > 0 { // a deployment from source: authorize saw no version yet
		unbuilt.Release = nil
	}
	base, err := s.judgedRequest(ctx, unbuilt, asked)
	if err != nil {
		return nil, err
	}
	withScan, err := s.judgedRequest(ctx, d, asked)
	if err != nil {
		return nil, err
	}
	withScan.Scan = scan
	withScan.ScanChecked = true
	verdict, err := s.policy.Check(ctx, org, withScan)
	if err != nil {
		return nil, err
	}
	if scan == nil && partial != nil {
		// Some images unscanned, others with findings: judged as unknown (a
		// rule that requires a scan fires) and on what is known (a rule on
		// the findings fires too); the stricter verdict stands.
		withScan.Scan = partial
		found, err := s.policy.Check(ctx, org, withScan)
		if err != nil {
			return nil, err
		}
		verdict = stricter(verdict, found)
	}
	switch verdict.Effect {
	case gate.Deny:
		if err := s.withdraw(ctx, org, st, release.StepAuthorizeScan); err != nil {
			return nil, err
		}
		return nil, workflow.Fail(refused(verdict))
	case gate.Approval:
		// what authorize already asked (and IT already answered) is not asked again
		before, err := s.policy.Check(ctx, org, base)
		if err != nil {
			return nil, err
		}
		verdict = onlyNewRules(verdict, before)
		if len(verdict.Rules) == 0 {
			return gate.Decision{Status: gate.DecisionAllowed}, nil
		}
	default:
		if err := s.withdraw(ctx, org, st, release.StepAuthorizeScan); err != nil {
			return nil, err
		}
		return gate.Decision{Status: gate.DecisionAllowed}, nil
	}
	if s.approvals == nil {
		return nil, errx.Internal("approvals are not wired")
	}
	summary := "Publicar " + where(p, e) + " (" + scanSummary(scan) + ")"
	if scan == nil && partial != nil {
		summary = "Publicar " + where(p, e) + " (" + scanSummary(partial) + " en lo revisado; hay servicios sin revisar)"
	}
	if err := s.holdDeployment(ctx, st, p, e, d, action, verdict, release.StepAuthorizeScan, summary); err != nil {
		return nil, err
	}
	return nil, workflow.WaitFor(gate.SignalFor(release.StepAuthorizeScan), approvalWait)
}

// scanOfAll is the scan of the deployment's images, one per service: the
// findings of all of them added up, unknown (nil) when any is — one
// unscanned image is not a clean version. partial is then the findings of
// the images that were scanned, when some have any (so they are judged
// too, not hidden by the unknown one). It waits while any scan may still
// come.
func (s *Service) scanOfAll(ctx context.Context, d release.Deployment) (scan, partial *gate.Scan, wait bool, err error) {
	total, known := &gate.Scan{}, true
	for _, id := range d.BuildIDs() {
		b, err := s.builder.Status(ctx, d.Organization, id)
		if err != nil {
			return nil, nil, false, err
		}
		one, wait := s.scanOf(b)
		if wait {
			return nil, nil, true, nil
		}
		if one == nil {
			known = false
			continue
		}
		total.Critical += one.Critical
		total.High += one.High
	}
	if known {
		return total, nil, false, nil
	}
	if total.Critical+total.High > 0 {
		return nil, total, false, nil
	}
	return nil, nil, false, nil
}

// stricter is the verdict that weighs more: a denial over an approval over
// an allowance; two approvals ask for both (their rules and approvers).
func stricter(a, b gate.Verdict) gate.Verdict {
	weight := map[gate.Effect]int{gate.Allow: 0, gate.Approval: 1, gate.Deny: 2}
	if weight[b.Effect] > weight[a.Effect] {
		return b
	}
	if a.Effect != gate.Approval || b.Effect != gate.Approval {
		return a
	}
	out := a
	out.Rules = slices.Clone(a.Rules)
	for _, r := range b.Rules {
		if !slices.ContainsFunc(out.Rules, func(x gate.Rule) bool { return x.ID == r.ID }) {
			out.Rules = append(out.Rules, r)
		}
	}
	out.Approvers = slices.Clone(a.Approvers)
	for _, p := range b.Approvers {
		if !slices.Contains(out.Approvers, p) {
			out.Approvers = append(out.Approvers, p)
		}
	}
	return out
}

// scanOf is the scan IT's rules judge: nil when unknown (never reported, not
// done, or the cluster does not scan). wait says the scan may still come: the
// build.scan workflow has not recorded anything and ScanWait has not passed
// since the build finished.
func (s *Service) scanOf(b build.Build) (scan *gate.Scan, wait bool) {
	if b.Scan != nil {
		if !b.Scan.Known() {
			return nil, false
		}
		return &gate.Scan{Critical: b.Scan.Critical, High: b.Scan.High}, false
	}
	finished := b.UpdatedAt
	if b.FinishedAt != nil {
		finished = *b.FinishedAt
	}
	if s.now().Sub(finished) >= release.ScanWait {
		slog.Warn("deploy authorize_scan: the scan did not arrive in time: judged as unknown", "build", b.ID)
		return nil, false
	}
	return nil, true
}

// onlyNewRules keeps, of the verdict, the rules the earlier verdict did not
// already carry (all of them when the earlier one allowed).
func onlyNewRules(v, before gate.Verdict) gate.Verdict {
	if before.Effect != gate.Approval {
		return v
	}
	asked := map[string]bool{}
	for _, r := range before.Rules {
		asked[r.ID] = true
	}
	var fresh []gate.Rule
	for _, r := range v.Rules {
		if !asked[r.ID] {
			fresh = append(fresh, r)
		}
	}
	v.Rules = fresh
	return v
}

// scanSummary is the scan in words for the approver.
func scanSummary(s *gate.Scan) string {
	if s == nil {
		return "sin revisar vulnerabilidades"
	}
	return plural(s.Critical, "crítica", "críticas") + ", " + plural(s.High, "alta", "altas")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
