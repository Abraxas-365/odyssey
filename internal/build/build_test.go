package build_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	s := build.Source{ID: build.MustParseSourceID("00000000-0000-4000-8000-0000000000e1"), Organization: org,
		Project: project.MustParseID("00000000-0000-4000-8000-0000000000aa"), Name: "visitas", Size: 48213,
		SHA256: strings.Repeat("0f", 32), Files: 37, UnpackedSize: 190442}
	b := build.Build{ID: build.MustParseID("00000000-0000-4000-8000-0000000000f1"), Organization: org, Project: s.Project,
		Environment: project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb"), Source: s.ID, RequestedBy: user,
		Image: "registry.local/o/visitas@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	n := build.Names{Project: "Visitas"}
	f := build.Failure{Code: build.CodeUnsupported, Message: build.Message(build.CodeUnsupported), Detail: "railpack: no start command"}
	for _, e := range []event.Event{
		build.NewSourceUploaded(org, user, s, n),
		build.NewSourceExpired(org, s),
		build.NewStarted(org, user, b, n),
		build.NewSucceeded(org, b, n),
		build.NewFailed(org, b, f, n),
		build.NewCancelled(org, user, b, n),
	} {
		eventtest.Golden(t, e)
	}
}

func TestUploadNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"visitas":                 "visitas",
		"  C:\\Users\\ana\\app ":  "app",
		"/home/ana/mi app.tar.gz": "mi app",
		"x.tgz":                   "x",
		".tgz":                    ".tgz",
		"":                        "código",
		"..":                      "código",
		"a\x00b\"c\n":             "abc",
		strings.Repeat("ñ", 150):  strings.Repeat("ñ", 100),
	} {
		got := build.Upload{Name: in}.Normalize()
		if got.Name != want || got.Validate() != nil {
			t.Errorf("%q → %q (%v), want %q", in, got.Name, got.Validate(), want)
		}
	}
}

func TestFailures(t *testing.T) {
	for reason, code := range map[string]string{
		build.ReasonUnsupported: build.CodeUnsupported, build.ReasonSource: build.CodeSourceInvalid,
		build.ReasonPush: build.CodePushFailed, build.ReasonTimeout: build.CodeTimeout,
		build.ReasonFailed: build.CodeFailed, "something new": build.CodeFailed,
	} {
		f := build.FailureOf(build.Observation{Phase: build.PhaseFailed, Reason: reason, Message: "  why  "})
		if f.Code != code || f.Message == "" || f.Detail != "why" {
			t.Errorf("%s: %+v", reason, f)
		}
	}
	long := build.FailureOf(build.Observation{Reason: build.ReasonFailed, Message: strings.Repeat("x", 10000) + "end"})
	if len(long.Detail) > 4100 || !strings.HasSuffix(long.Detail, "end") {
		t.Errorf("long detail kept %d bytes", len(long.Detail))
	}

	if f := build.FailureFromError(build.StepDispatch, build.Refused(build.CodeNoBuilder, "no registry")); f.Code != build.CodeNoBuilder || f.Detail != "no registry" {
		t.Errorf("refused: %+v", f)
	}
	if f := build.FailureFromError(build.StepWait, errx.Internal("step wait timed out")); f.Code != build.CodeTimeout {
		t.Errorf("timeout: %+v", f)
	}
	if f := build.FailureFromError(build.StepDispatch, errors.New("dial tcp: refused")); f.Code != build.CodeInternal || f.Message != build.Message(build.CodeInternal) {
		t.Errorf("internal: %+v", f)
	}
	if build.Message("NOPE") != build.Message(build.CodeInternal) {
		t.Error("unknown code message")
	}
}

func TestLogIndexStart(t *testing.T) {
	x := build.LogIndex{{Seq: 1001, Offset: 500}, {Seq: 2001, Offset: 900}}
	for after, want := range map[int64]build.LogMark{
		0: {}, 999: {}, 1000: x[0], 1500: x[0], 1999: x[0], 2000: x[1], 9000: x[1],
	} {
		if got := x.Start(after); got != want {
			t.Errorf("after %d: %+v, want %+v", after, got, want)
		}
	}
	if got := (build.LogIndex(nil)).Start(5000); got != (build.LogMark{}) {
		t.Errorf("empty index: %+v", got)
	}
}

func TestStartValidation(t *testing.T) {
	ok := build.Start{ID: build.NewID(), Project: project.NewID(), Environment: project.NewEnvironmentID(), Source: build.NewSourceID()}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []build.Start{
		{Project: ok.Project, Environment: ok.Environment, Source: ok.Source},
		{ID: ok.ID, Environment: ok.Environment, Source: ok.Source},
		{ID: ok.ID, Project: ok.Project, Environment: ok.Environment},
	} {
		if !errx.IsType(bad.Validate(), errx.TypeValidation) {
			t.Errorf("%+v accepted", bad)
		}
	}
	if q := (build.LogQuery{Limit: 5000}).Normalize(); q.Limit != build.MaxLogPage {
		t.Errorf("limit %d", q.Limit)
	}
	if (build.LogQuery{After: -1}).Validate() == nil {
		t.Error("negative after accepted")
	}
}
