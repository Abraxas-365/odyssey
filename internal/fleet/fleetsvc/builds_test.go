package fleetsvc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/google/uuid"
)

// builder connects the agent announcing a builder (nil: none).
func (e *env) connect(t *testing.T, agent fleet.AgentRef, builder *fleet.Builder) fleet.Session {
	t.Helper()
	session := fleet.NewSession()
	if err := e.svc.Connect(context.Background(), agent, session,
		fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{Builder: builder}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	return session
}

func (e *env) desiredBuild(environment project.EnvironmentID) fleet.DesiredBuild {
	return fleet.DesiredBuild{ID: fleet.MustParseBuildID(uuid.NewString()), Organization: e.org, Project: project.NewID(),
		Environment: environment, SourceKey: blob.TenantKey(e.org, "sources", uuid.NewString()+".tar.gz"), SourceSHA256: strings.Repeat("a", 64), SourceSize: 10,
		Timeout: 20 * time.Minute}
}

var zot = &fleet.Builder{Registry: "zot.odyssey-system.svc:5000", Platforms: []string{"linux/amd64"}}

func (e *env) count(eventType string) int {
	n := 0
	for _, t := range e.events.Types() {
		if t == eventType {
			n++
		}
	}
	return n
}

// A build goes to its environment's cluster, under the registry the
// cluster announced, once; a cluster without a builder refuses it.
func TestDispatchBuild(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	e.connect(t, agent, nil)

	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, b); code(err) != fleet.CodeNoBuilder {
		t.Fatalf("dispatch to a cluster without a builder: %v", err)
	}
	e.connect(t, agent, zot)
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DispatchBuild(ctx, b); err != nil { // a retried dispatch
		t.Fatal(err)
	}
	if n := e.count(fleet.EventBuildSent); n != 1 {
		t.Fatalf("%d build_sent events, want 1", n)
	}
	got, ok, err := e.svc.DesiredBuild(ctx, agent, b.ID)
	if err != nil || !ok {
		t.Fatalf("desired build: %v %v", ok, err)
	}
	repo := "zot.odyssey-system.svc:5000/" + e.org.String() + "/" + b.Project.String()
	if got.Cluster != agent.Cluster || got.ImageRepository != repo || got.CacheRef != repo+":buildcache" || got.Timeout != 20*time.Minute {
		t.Fatalf("desired build %+v", got)
	}
	if all, err := e.svc.DesiredBuilds(ctx, agent); err != nil || len(all) != 1 {
		t.Fatalf("desired builds %d %v", len(all), err)
	}
	// another cluster of the organization neither sees it nor fetches its source
	other := e.enrolled(t, "cusco")
	if _, ok, _ := e.svc.DesiredBuild(ctx, other, b.ID); ok {
		t.Fatal("another cluster sees the build")
	}
	if _, err := e.svc.BuildSource(ctx, other, b.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another cluster fetching the source: %v", err)
	}
	if _, err := e.svc.BuildSource(ctx, agent, b.ID); err != nil {
		t.Fatalf("its cluster fetching the source: %v", err)
	}
}

func TestDispatchBuildWithoutCluster(t *testing.T) {
	e := setup(t)
	if err := e.svc.DispatchBuild(context.Background(), e.desiredBuild(project.NewEnvironmentID())); code(err) != fleet.CodeNoCluster {
		t.Fatalf("dispatch without a cluster: %v", err)
	}
}

// The agent's reports move the status; a final phase never changes, and a
// finished build's source is no longer served.
func TestReportBuilds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := e.connect(t, agent, zot)
	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	if s, err := e.svc.BuildStatus(ctx, e.org, b.ID); err != nil || s.Phase != fleet.BuildPending {
		t.Fatalf("status before any report: %+v %v", s, err)
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	err := e.svc.ReportBuilds(ctx, agent, session, []fleet.BuildStatus{
		{Build: b.ID, Phase: fleet.BuildRunning},
		{Build: b.ID, Phase: fleet.BuildSucceeded},                                  // no digest: dropped
		{Build: fleet.MustParseBuildID(uuid.NewString()), Phase: fleet.BuildFailed}, // never desired: ignored
	})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("report with an invalid status: %v", err)
	}
	if s, _ := e.svc.BuildStatus(ctx, e.org, b.ID); s.Phase != fleet.BuildRunning {
		t.Fatalf("status %+v", s)
	}
	if err := e.svc.ReportBuilds(ctx, agent, session, []fleet.BuildStatus{{Build: b.ID, Phase: fleet.BuildSucceeded, Digest: digest}}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ReportBuilds(ctx, agent, session, []fleet.BuildStatus{{Build: b.ID, Phase: fleet.BuildFailed, Reason: "failed"}}); err != nil {
		t.Fatal(err)
	}
	s, _ := e.svc.BuildStatus(ctx, e.org, b.ID)
	repo := "zot.odyssey-system.svc:5000/" + e.org.String() + "/" + b.Project.String()
	if s.Phase != fleet.BuildSucceeded || s.Image != repo+"@"+digest {
		t.Fatalf("a final status changed: %+v", s)
	}
	if _, err := e.svc.BuildSource(ctx, agent, b.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("source of a finished build: %v", err)
	}
	// a stale tunnel's report is refused and stores nothing
	other := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, other); err != nil {
		t.Fatal(err)
	}
	e.connect(t, agent, zot)
	if err := e.svc.ReportBuilds(ctx, agent, session, []fleet.BuildStatus{{Build: other.ID, Phase: fleet.BuildRunning}}); code(err) != fleet.CodeSuperseded {
		t.Fatalf("report from a superseded tunnel: %v", err)
	}
	if s, _ := e.svc.BuildStatus(ctx, e.org, other.ID); s.Phase != fleet.BuildPending {
		t.Fatalf("a superseded tunnel's report was stored: %+v", s)
	}
	if err := e.svc.LogBuild(ctx, agent, session, fleet.BuildLog{Build: other.ID, Lines: []fleet.LogLine{{Seq: 1, Stream: "build", Text: "x"}}}); code(err) != fleet.CodeSuperseded {
		t.Fatalf("log from a superseded tunnel: %v", err)
	}
	if page, _ := e.svc.BuildLogs(ctx, e.org, other.ID, 0, 10); len(page.Lines) != 0 {
		t.Fatalf("a superseded tunnel's log was stored: %+v", page.Lines)
	}
}

// A failure's message is a builder's raw output: it is cleaned, not
// refused (a lost final report would leave the build hanging).
func TestReportBuildsCleansFailures(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := e.connect(t, agent, zot)
	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", fleet.MaxMessage+50)
	if err := e.svc.ReportBuilds(ctx, agent, session, []fleet.BuildStatus{
		{Build: b.ID, Phase: fleet.BuildFailed, Reason: "exploded", Message: "\x1b[31mboom\x1b[0m\r\n" + long},
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := e.svc.BuildStatus(ctx, e.org, b.ID)
	if s.Phase != fleet.BuildFailed || s.Reason != "failed" || !strings.HasPrefix(s.Message, "boom") ||
		len([]rune(s.Message)) != fleet.MaxMessage || strings.ContainsAny(s.Message, "\x1b\r\n") {
		t.Fatalf("cleaned failure %+v", s)
	}
}

// One organization reaches nothing of another's builds, whatever the id.
func TestBuildsStayInTheirOrganization(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := e.connect(t, agent, zot)
	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.LogBuild(ctx, agent, session, fleet.BuildLog{Build: b.ID, Lines: []fleet.LogLine{{Seq: 1, Stream: "build", Text: "secret"}}}); err != nil {
		t.Fatal(err)
	}
	stranger := identity.NewOrganizationID()
	if s, err := e.svc.BuildStatus(ctx, stranger, b.ID); err != nil || s.Phase != fleet.BuildPending {
		t.Fatalf("another organization's status: %+v %v", s, err)
	}
	if page, err := e.svc.BuildLogs(ctx, stranger, b.ID, 0, 10); err != nil || len(page.Lines) != 0 {
		t.Fatalf("another organization's log: %+v %v", page, err)
	}
	if err := e.svc.CancelBuild(ctx, stranger, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ForgetBuildLogs(ctx, stranger, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := e.svc.DesiredBuild(ctx, agent, b.ID); got.Cancelled {
		t.Fatal("another organization cancelled the build")
	}
	if page, _ := e.svc.BuildLogs(ctx, e.org, b.ID, 0, 10); len(page.Lines) != 1 {
		t.Fatalf("another organization forgot the log: %+v", page.Lines)
	}
	// a build naming another organization's source is refused
	theirs := e.desiredBuild(project.NewEnvironmentID())
	theirs.SourceKey = blob.TenantKey(stranger, "sources", uuid.NewString()+".tar.gz")
	if err := e.svc.DispatchBuild(ctx, theirs); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("dispatch with another organization's source: %v", err)
	}
}

// Cancelling tells the cluster once; a cancelled build's source is not served.
func TestCancelBuild(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	e.connect(t, agent, zot)
	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.CancelBuild(ctx, e.org, b.ID); err != nil {
		t.Fatalf("cancel an unknown build: %v", err)
	}
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.svc.CancelBuild(ctx, e.org, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(fleet.EventBuildReleased); n != 1 {
		t.Fatalf("%d build_released events, want 1", n)
	}
	if got, _, _ := e.svc.DesiredBuild(ctx, agent, b.ID); !got.Cancelled {
		t.Fatal("the build is not marked cancelled")
	}
	if _, err := e.svc.BuildSource(ctx, agent, b.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("source of a cancelled build: %v", err)
	}
}

// Lines are stored once (resends), cleaned, capped per build with a "cut"
// line at the end, and dropped once archived.
func TestBuildLogs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := e.connect(t, agent, zot)
	b := e.desiredBuild(project.NewEnvironmentID())
	if err := e.svc.DispatchBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	batch := fleet.BuildLog{Build: b.ID, Lines: []fleet.LogLine{
		{Seq: 1, Stream: "prepare", Text: "\x1b[32mfetching\x1b[0m source"},
		{Seq: 2, Stream: "build", Text: "step 1\r"},
		{Seq: 3, Stream: "nope", Text: "dropped"},
	}}
	for range 2 { // a resend stores nothing new
		if err := e.svc.LogBuild(ctx, agent, session, batch); err != nil {
			t.Fatal(err)
		}
	}
	page, err := e.svc.BuildLogs(ctx, e.org, b.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 2 || page.Lines[0].Text != "fetching source" || page.Lines[1].Text != "step 1" || page.Truncated {
		t.Fatalf("log %+v", page)
	}
	if page, _ := e.svc.BuildLogs(ctx, e.org, b.ID, 1, 100); len(page.Lines) != 1 || page.Lines[0].Seq != 2 {
		t.Fatalf("log after 1: %+v", page)
	}

	// past the cap: the line that does not fit and every later one are dropped
	big := strings.Repeat("x", fleet.MaxLogLineBytes-1)
	var seq int64 = 3
	for total := 0; total <= fleet.MaxBuildLogBytes; {
		lines := make([]fleet.LogLine, 0, fleet.MaxLogBatch)
		for range fleet.MaxLogBatch {
			lines = append(lines, fleet.LogLine{Seq: seq, Stream: "build", Text: big})
			seq++
			total += len(big) + 1
		}
		if err := e.svc.LogBuild(ctx, agent, session, fleet.BuildLog{Build: b.ID, Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.LogBuild(ctx, agent, session, fleet.BuildLog{Build: b.ID, Lines: []fleet.LogLine{{Seq: seq, Stream: "build", Text: "small"}}}); err != nil {
		t.Fatal(err)
	}
	var stored []fleet.LogLine
	var after int64
	for {
		page, err := e.svc.BuildLogs(ctx, e.org, b.ID, after, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) == 0 {
			break
		}
		stored = append(stored, page.Lines...)
		after = page.Lines[len(page.Lines)-1].Seq
	}
	last := stored[len(stored)-1]
	if last.Stream != "system" || last.Text != fleet.LogCutText {
		t.Fatalf("a truncated log ends with %+v", last)
	}
	bytes := 0
	for _, l := range stored[:len(stored)-1] {
		bytes += len(l.Text) + 1
		if l.Text == "small" {
			t.Fatal("a line after the cut was stored")
		}
	}
	if bytes > fleet.MaxBuildLogBytes || bytes < fleet.MaxBuildLogBytes-fleet.MaxLogLineBytes-1 {
		t.Fatalf("%d bytes stored, cap %d", bytes, fleet.MaxBuildLogBytes)
	}

	if err := e.svc.ForgetBuildLogs(ctx, e.org, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.LogBuild(ctx, agent, session, fleet.BuildLog{Build: b.ID, Lines: []fleet.LogLine{{Seq: seq + 1, Stream: "build", Text: "late"}}}); err != nil {
		t.Fatal(err)
	}
	if page, _ := e.svc.BuildLogs(ctx, e.org, b.ID, 0, 10); len(page.Lines) != 0 {
		t.Fatalf("lines after archiving: %+v", page.Lines)
	}
}
