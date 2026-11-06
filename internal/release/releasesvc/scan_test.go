package releasesvc

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// scanRules judges by a function of the request, so a test says what the
// rules do with and without the scan.
type scanRules struct {
	by      func(gate.Request) gate.Verdict
	scans   []*gate.Scan
	flags   []bool // ScanChecked of each request
	checked int
}

func (r *scanRules) Check(_ context.Context, _ identity.OrganizationID, q gate.Request) (gate.Verdict, error) {
	r.checked++
	r.scans = append(r.scans, q.Scan)
	r.flags = append(r.flags, q.ScanChecked)
	return r.by(q), nil
}

// builds serves one scripted build (b), or one per ID (each, for a
// deployment of several services).
type builds struct {
	release.Builder
	b    build.Build
	each map[build.ID]build.Build
}

func (b *builds) Status(_ context.Context, _ identity.OrganizationID, id build.ID) (build.Build, error) {
	if x, ok := b.each[id]; ok {
		return x, nil
	}
	return b.b, nil
}

func (b *builds) Cancel(context.Context, identity.OrganizationID, build.ID, build.Cancel) error {
	return nil
}

func scanRig(t *testing.T, by func(gate.Request) gate.Verdict, b build.Build) (*approvalRig, *scanRules) {
	t.Helper()
	r := newApprovalRig(t, gate.Allow, release.ReasonDeploy, dev)
	rules := &scanRules{by: by}
	r.svc.policy = rules
	id := build.NewID()
	b.ID = id
	r.repo.dep.Build = &id
	r.svc.builder = &builds{b: b}
	r.svc.opts.PollEvery = time.Second
	return r, rules
}

var (
	deny    = gate.Verdict{Effect: gate.Deny, Rules: []gate.Rule{{ID: "crit", Name: "Producción no publica con críticas"}}}
	approve = gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{ID: "crit2", Name: "Críticas se aprueban"}}, Approvers: []string{"odyssey:approvals:decide"}}
	ask     = gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{ID: "prod", Name: "Producción se aprueba"}}, Approvers: []string{"odyssey:approvals:decide"}}
)

func scanned(c, h int) build.Build {
	now := time.Now()
	return build.Build{Status: build.StatusSucceeded, FinishedAt: &now, Scan: &build.Scan{Critical: c, High: h}}
}

func TestAuthorizeScanFeedsTheScanAndDenies(t *testing.T) {
	r, rules := scanRig(t, func(q gate.Request) gate.Verdict {
		if q.Scan != nil && q.Scan.Critical > 0 {
			return deny
		}
		return gate.Verdict{Effect: gate.Allow}
	}, scanned(2, 5))
	_, err := r.svc.authorizeScanStep(context.Background(), r.st)
	var coded *errx.Error
	if c, ok := workflow.AsControl(err); !ok || c.Kind != workflow.ControlFail || !errors.As(err, &coded) || coded.Code != release.CodePolicyDenied {
		t.Fatalf("want a permanent POLICY_DENIED failure, got %v", err)
	}
	if got := rules.scans[0]; got == nil || got.Critical != 2 || got.High != 5 {
		t.Fatalf("judged with scan %+v", got)
	}
	if !slices.Equal(r.approver.steps, []string{release.StepAuthorizeScan}) {
		t.Fatalf("withdrew requests of steps %v", r.approver.steps)
	}
}

func TestAuthorizeScanAllowsACleanImage(t *testing.T) {
	r, _ := scanRig(t, func(q gate.Request) gate.Verdict {
		if q.Scan != nil && q.Scan.Critical > 0 {
			return deny
		}
		return gate.Verdict{Effect: gate.Allow}
	}, scanned(0, 3))
	out, err := r.svc.authorizeScanStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed {
		t.Fatalf("out %v, err %v", out, err)
	}
	if len(r.approver.held) != 0 {
		t.Fatalf("held %v", r.approver.held)
	}
}

// MS-3a: a version of several services is judged on all of its images: the
// findings added up, unknown when any image's is, and not before every
// scan could have come.
func TestAuthorizeScanAddsUpTheServicesImages(t *testing.T) {
	now := time.Now()
	topology := []project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "api", Kind: project.ServiceWeb, Route: "/api"}}
	for name, c := range map[string]struct {
		api  build.Build
		want *gate.Scan
	}{
		"both scanned":  {api: scanned(1, 2), want: &gate.Scan{Critical: 1, High: 5}},
		"one unscanned": {api: build.Build{Status: build.StatusSucceeded, FinishedAt: &now, Scan: &build.Scan{Error: build.ScanUnsupportedMessage}}},
		"one still due": {api: build.Build{Status: build.StatusSucceeded, FinishedAt: &now}},
	} {
		r, rules := scanRig(t, func(gate.Request) gate.Verdict { return gate.Verdict{Effect: gate.Allow} }, build.Build{})
		services := release.BuildsFor(topology)
		r.repo.dep.Build, r.repo.dep.Builds = &services[0].Build, services
		r.svc.builder = &builds{each: map[build.ID]build.Build{services[0].Build: scanned(0, 3), services[1].Build: c.api}}
		out, err := r.svc.authorizeScanStep(context.Background(), r.st)
		if name == "one still due" {
			if ctl, ok := workflow.AsControl(err); !ok || ctl.Kind != workflow.ControlPoll || len(rules.scans) != 0 {
				t.Fatalf("%s: want a poll before judging, got %v %v", name, out, err)
			}
			continue
		}
		if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed {
			t.Fatalf("%s: out %v, err %v", name, out, err)
		}
		got := rules.scans[0]
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("%s: judged with scan %+v, want %+v", name, got, c.want)
		}
		// unknown with findings elsewhere: judged also on what is known
		if name == "one unscanned" && (len(rules.scans) != 2 || rules.scans[1] == nil || *rules.scans[1] != (gate.Scan{High: 3})) {
			t.Fatalf("%s: the known findings were not judged: %+v", name, rules.scans)
		}
	}
}

// An unscanned service does not hide another's critical findings: a rule on
// the findings still denies, and one requiring a scan still fires.
func TestAuthorizeScanDoesNotHideFindingsBehindAnUnscannedService(t *testing.T) {
	now := time.Now()
	topology := []project.Service{{Name: "web", Kind: project.ServiceWeb, Route: "/"}, {Name: "api", Kind: project.ServiceWeb, Route: "/api"}}
	unscanned := build.Build{Status: build.StatusSucceeded, FinishedAt: &now, Scan: &build.Scan{Error: build.ScanUnsupportedMessage}}
	for name, rule := range map[string]func(gate.Request) gate.Verdict{
		"critical denies": func(q gate.Request) gate.Verdict {
			if q.Scan != nil && q.Scan.Critical > 0 {
				return deny
			}
			return gate.Verdict{Effect: gate.Allow}
		},
		"unknown denies": func(q gate.Request) gate.Verdict {
			if q.ScanChecked && q.Scan == nil {
				return deny
			}
			return gate.Verdict{Effect: gate.Allow}
		},
	} {
		r, _ := scanRig(t, rule, build.Build{})
		services := release.BuildsFor(topology)
		r.repo.dep.Build, r.repo.dep.Builds = &services[0].Build, services
		r.svc.builder = &builds{each: map[build.ID]build.Build{services[0].Build: scanned(2, 0), services[1].Build: unscanned}}
		_, err := r.svc.authorizeScanStep(context.Background(), r.st)
		var coded *errx.Error
		if c, ok := workflow.AsControl(err); !ok || c.Kind != workflow.ControlFail || !errors.As(err, &coded) || coded.Code != release.CodePolicyDenied {
			t.Fatalf("%s: want POLICY_DENIED, got %v", name, err)
		}
	}
}

func TestStricterVerdict(t *testing.T) {
	allow := gate.Verdict{Effect: gate.Allow}
	if got := stricter(allow, deny); got.Effect != gate.Deny {
		t.Errorf("allow, deny: %+v", got)
	}
	if got := stricter(deny, approve); got.Effect != gate.Deny {
		t.Errorf("deny, approve: %+v", got)
	}
	got := stricter(approve, ask)
	if got.Effect != gate.Approval || len(got.Rules) != 2 || len(got.Approvers) != 1 {
		t.Errorf("two approvals: %+v", got)
	}
}

// An image that could not be scanned judges as scan.known = false: a nil Scan.
func TestAuthorizeScanJudgesAnUnknownScanAsUnknown(t *testing.T) {
	now := time.Now()
	for name, b := range map[string]build.Build{
		"failed scan": {Status: build.StatusSucceeded, FinishedAt: &now, Scan: &build.Scan{Error: build.ScanUnsupportedMessage}},
		"never came":  {Status: build.StatusSucceeded, FinishedAt: ptr(now.Add(-release.ScanWait - time.Second))},
	} {
		r, rules := scanRig(t, func(gate.Request) gate.Verdict { return gate.Verdict{Effect: gate.Allow} }, b)
		out, err := r.svc.authorizeScanStep(context.Background(), r.st)
		if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed {
			t.Fatalf("%s: out %v, err %v", name, out, err)
		}
		if rules.scans[0] != nil {
			t.Fatalf("%s: judged with scan %+v, want unknown", name, rules.scans[0])
		}
		// unknown, but looked at: scan.checked && !scan.known is what a rule
		// that requires a scan sees
		if !rules.flags[0] {
			t.Fatalf("%s: the judgment after the build must say the scan was checked", name)
		}
	}
}

// The step after the build judges with the scan looked at, and the "ask
// again?" comparison (the request authorize made, without the scan) does not:
// a rule that requires a scan can only match the first.
func TestAuthorizeScanMarksTheScanChecked(t *testing.T) {
	r, rules := scanRig(t, func(q gate.Request) gate.Verdict {
		if q.ScanChecked && q.Scan == nil {
			return approve // production_requires_scan: no scan to show for it
		}
		return gate.Verdict{Effect: gate.Allow}
	}, build.Build{Status: build.StatusSucceeded, FinishedAt: ptr(time.Now()), Scan: &build.Scan{Error: build.ScanFailedMessage}})
	if _, err := r.svc.authorizeScanStep(context.Background(), r.st); err == nil {
		t.Fatal("an unknown scan under a rule that requires one should park for approval")
	}
	if len(rules.flags) < 2 || !rules.flags[0] || rules.flags[1] {
		t.Fatalf("flags %v: want the judgment checked, the earlier-request comparison not", rules.flags)
	}
}

func TestAuthorizeScanWaitsForTheScan(t *testing.T) {
	now := time.Now()
	r, rules := scanRig(t, func(gate.Request) gate.Verdict { return deny }, build.Build{Status: build.StatusSucceeded, FinishedAt: &now})
	_, err := r.svc.authorizeScanStep(context.Background(), r.st)
	if c, ok := workflow.AsControl(err); !ok || c.Kind != workflow.ControlPoll || c.Delay != time.Second {
		t.Fatalf("want a poll, got %v", err)
	}
	if rules.checked != 0 {
		t.Fatal("the rules judged before the scan arrived")
	}
}

func TestAuthorizeScanHoldsAtItsOwnStepAndParksOnItsOwnSignal(t *testing.T) {
	r, _ := scanRig(t, func(q gate.Request) gate.Verdict {
		if q.Scan != nil {
			return approve
		}
		return gate.Verdict{Effect: gate.Allow}
	}, scanned(1, 0))
	_, err := r.svc.authorizeScanStep(context.Background(), r.st)
	c, ok := workflow.AsControl(err)
	if !ok || c.Kind != workflow.ControlWait || c.Signal != gate.SignalFor(release.StepAuthorizeScan) || c.Signal == gate.SignalApproval {
		t.Fatalf("want WaitFor(its own signal), got %v", err)
	}
	if len(r.approver.held) != 1 {
		t.Fatalf("held %d", len(r.approver.held))
	}
	h := r.approver.held[0]
	if h.RunStep != release.StepAuthorizeScan || h.Run != r.st.Run.ID.String() || h.Summary == "" {
		t.Fatalf("request %+v", h)
	}
	if r.repo.dep.Status != release.StatusWaitingApproval {
		t.Fatalf("status %s", r.repo.dep.Status)
	}
}

// IT already approved "production needs approval" at authorize: a clean
// scan does not ask the same question twice.
func TestAuthorizeScanDoesNotAskAgainWhatAuthorizeAsked(t *testing.T) {
	r, _ := scanRig(t, func(gate.Request) gate.Verdict { return ask }, scanned(0, 0))
	out, err := r.svc.authorizeScanStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed || len(r.approver.held) != 0 {
		t.Fatalf("out %v, err %v, held %v", out, err, r.approver.held)
	}
}

func TestApprovalScanStepActsOnItsOwnDecision(t *testing.T) {
	r, _ := scanRig(t, func(gate.Request) gate.Verdict { return gate.Verdict{Effect: gate.Allow} }, scanned(0, 0))
	// authorize's decision (allowed) must not stand for the scan's
	raw, _ := json.Marshal(gate.Decision{Request: "req-1", Status: gate.DecisionAllowed})
	r.st.Run.Outputs[release.StepAuthorize] = raw
	raw, _ = json.Marshal(gate.Decision{Request: "req-2", Status: gate.DecisionRejected})
	r.st.Run.Outputs[release.StepAuthorizeScan] = raw
	r.repo.dep.Status = release.StatusWaitingApproval
	_, err := r.svc.approvalScanStep(context.Background(), r.st)
	if !errors.Is(err, errSuperseded) || r.repo.dep.Status != release.StatusCancelled {
		t.Fatalf("err %v, deployment %s", err, r.repo.dep.Status)
	}

	r, _ = scanRig(t, func(gate.Request) gate.Verdict { return gate.Verdict{Effect: gate.Allow} }, scanned(0, 0))
	raw, _ = json.Marshal(gate.Decision{Request: "req-2", Status: gate.DecisionApproved})
	r.st.Run.Outputs[release.StepAuthorizeScan] = raw
	r.repo.dep.Status = release.StatusWaitingApproval
	rel := release.NewID()
	r.repo.dep.Release = &rel // the build recorded it; apply has not started
	if _, err := r.svc.approvalScanStep(context.Background(), r.st); err != nil || !slices.Equal(r.approver.consumed, []string{"req-2"}) {
		t.Fatalf("err %v, consumed %v", err, r.approver.consumed)
	}
	// still the end of the build as far as the console goes, not "pending"
	if r.repo.dep.Status != release.StatusBuilding {
		t.Fatalf("after approving the scan the deployment is %s, want building", r.repo.dep.Status)
	}
}

func TestAuthorizeScanSkipsWhatWasNotBuiltOrJudged(t *testing.T) {
	r, rules := scanRig(t, func(gate.Request) gate.Verdict { return deny }, scanned(9, 9))
	r.repo.dep.Build = nil // deploy_image: nothing built here, never scanned
	out, err := r.svc.authorizeScanStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed || rules.checked != 0 {
		t.Fatalf("image: out %v, err %v, checked %d", out, err, rules.checked)
	}
	r, rules = scanRig(t, func(gate.Request) gate.Verdict { return deny }, scanned(9, 9))
	r.repo.dep.Reason = release.ReasonConfig
	out, err = r.svc.authorizeScanStep(context.Background(), r.st)
	if err != nil || out.(gate.Decision).Status != gate.DecisionAllowed || rules.checked != 0 {
		t.Fatalf("config: out %v, err %v, checked %d", out, err, rules.checked)
	}
}

func TestOnlyNewRulesAndScanSummary(t *testing.T) {
	got := onlyNewRules(gate.Verdict{Effect: gate.Approval, Rules: []gate.Rule{{ID: "prod"}, {ID: "crit2"}}}, ask)
	if len(got.Rules) != 1 || got.Rules[0].ID != "crit2" {
		t.Fatalf("rules %v", got.Rules)
	}
	if got := scanSummary(&gate.Scan{Critical: 1, High: 5}); got != "1 crítica, 5 altas" {
		t.Fatalf("summary %q", got)
	}
	if got := scanSummary(nil); got != "sin revisar vulnerabilidades" {
		t.Fatalf("summary %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
