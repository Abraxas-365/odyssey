package buildfleet_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfleet"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// builds is a scripted fleet.Builds.
type builds struct {
	dispatchErr error
	sent        fleet.DesiredBuild
	status      fleet.BuildStatus
	scan        fleet.BuildScan
}

func (f *builds) DispatchBuild(_ context.Context, b fleet.DesiredBuild) error {
	f.sent = b
	return f.dispatchErr
}
func (f *builds) CancelBuild(context.Context, identity.OrganizationID, fleet.BuildID) error {
	return nil
}
func (f *builds) BuildStatus(context.Context, identity.OrganizationID, fleet.BuildID) (fleet.BuildStatus, error) {
	return f.status, nil
}
func (f *builds) BuildLogs(context.Context, identity.OrganizationID, fleet.BuildID, int64, int) (fleet.LogPage, error) {
	return fleet.LogPage{Lines: []fleet.LogLine{{Seq: 7, Stream: "build", Text: "hi"}}}, nil
}
func (f *builds) ForgetBuildLogs(context.Context, identity.OrganizationID, fleet.BuildID) error {
	return nil
}
func (f *builds) BuildScan(context.Context, identity.OrganizationID, fleet.BuildID) (fleet.BuildScan, error) {
	return f.scan, nil
}

func job() build.Job {
	org := identity.NewOrganizationID()
	b := build.Build{ID: build.NewID(), Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}
	return build.Job{Build: b, Source: build.Source{Key: blob.TenantKey(org, "sources", "y.tar.gz"), SHA256: strings.Repeat("a", 64), Size: 3}}
}

func TestDispatch(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string // the build's failure code; "" for an error that is retried
	}{
		{errx.Business("no registry").WithCode(fleet.CodeNoBuilder), build.CodeNoBuilder},
		{errx.Business("no cluster").WithCode(fleet.CodeNoCluster), build.CodeNoCluster},
		{errx.Business("revoked").WithCode(fleet.CodeClusterRevoked), build.CodeNoCluster},
		{errx.Conflict("moved").WithCode(fleet.CodePlacementMoved), ""},
		{errx.Internal("db down"), ""},
	} {
		f := &builds{dispatchErr: c.err}
		err := buildfleet.New(f).Dispatch(context.Background(), job())
		var e *errx.Error
		refused := errx.As(err, &e) && e.Type == errx.TypeBusiness
		switch {
		case c.code != "" && (!refused || e.Code != c.code):
			t.Errorf("%v: want refused with %s, got %v", c.err, c.code, err)
		case c.code == "" && refused:
			t.Errorf("%v: refused, want it retried", c.err)
		case c.code == "" && err == nil:
			t.Errorf("%v: swallowed", c.err)
		}
	}
	f := &builds{}
	j := job()
	if err := buildfleet.New(f).Dispatch(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if f.sent.ID.String() != j.Build.ID.String() || f.sent.SourceKey != j.Source.Key || f.sent.Timeout != buildfleet.Deadline {
		t.Fatalf("sent %+v", f.sent)
	}
	if buildfleet.Deadline >= build.Timeout {
		t.Fatal("the cluster's deadline must come before the workflow's")
	}
}

func TestObserve(t *testing.T) {
	image := "zot.odyssey-system.svc:5000/" + identity.NewOrganizationID().String() + "/" + project.NewID().String() + "@sha256:" + strings.Repeat("c", 64)
	for _, c := range []struct {
		status fleet.BuildStatus
		want   build.Observation
	}{
		{fleet.BuildStatus{Phase: fleet.BuildPending}, build.Observation{Phase: build.PhasePending}},
		{fleet.BuildStatus{Phase: fleet.BuildRunning, Message: "pushing"}, build.Observation{Phase: build.PhaseRunning, Message: "pushing"}},
		{fleet.BuildStatus{Phase: fleet.BuildSucceeded, Image: image}, build.Observation{Phase: build.PhaseSucceeded, Image: image}},
		{fleet.BuildStatus{Phase: fleet.BuildFailed}, build.Observation{Phase: build.PhaseFailed, Reason: build.ReasonFailed}},
		{fleet.BuildStatus{Phase: fleet.BuildFailed, Reason: "push"}, build.Observation{Phase: build.PhaseFailed, Reason: build.ReasonPush}},
	} {
		got, err := buildfleet.New(&builds{status: c.status}).Observe(context.Background(), job().Build)
		if err != nil || got != c.want {
			t.Errorf("%+v: got %+v %v, want %+v", c.status, got, err, c.want)
		}
	}
	// what a cluster's builder pushes is an image a release accepts
	if !build.PinnedImage(image) || !release.ValidImage(image) {
		t.Fatalf("%s is not a pinned, valid image", image)
	}
}

func TestLogs(t *testing.T) {
	lines, err := buildfleet.New(&builds{}).Logs(context.Background(), job().Build, 0, 10)
	if err != nil || len(lines) != 1 || lines[0] != (build.LogLine{Seq: 7, Stream: "build", Text: "hi"}) {
		t.Fatalf("logs %+v %v", lines, err)
	}
}
