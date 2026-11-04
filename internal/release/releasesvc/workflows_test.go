package releasesvc

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func clockedService(now *time.Time) *Service {
	s := &Service{opts: Options{ReadyTimeout: 5 * time.Minute, PollEvery: time.Second}}
	s.now = func() time.Time { return *now }
	return s
}

func stateWith(t *testing.T, apply any, saved bool) *workflow.State {
	t.Helper()
	st := &workflow.State{Run: workflow.Run{Outputs: map[string]json.RawMessage{}}}
	if saved {
		raw, err := json.Marshal(apply)
		if err != nil {
			t.Fatal(err)
		}
		st.Run.Outputs[release.StepApply] = raw
	}
	return st
}

func kind(err error) workflow.ControlKind {
	c, ok := workflow.AsControl(err)
	if !ok {
		return 0
	}
	return c.Kind
}

// wait_ready's outer bound leaves room for the agent's wait for resources.
func TestReadyBoundCoversTheResourceWait(t *testing.T) {
	now := time.Now()
	s := clockedService(&now)
	if got, want := s.readyBound(), resourcekind.Wait+resourcesGrace; got != want {
		t.Errorf("bound %v, want %v", got, want)
	}
	s.opts.ReadyTimeout = time.Hour
	if got := s.readyBound(); got != time.Hour {
		t.Errorf("a longer ReadyTimeout was shortened to %v", got)
	}
	for _, step := range clockedService(&now).Workflows()[0].Steps {
		if step.Name == release.StepWaitReady && step.Timeout <= resourcekind.Wait {
			t.Errorf("wait_ready times out (%v) before the agent stops waiting for resources (%v)", step.Timeout, resourcekind.Wait)
		}
	}
}

func TestStillWaiting(t *testing.T) {
	now := time.Now()
	s := clockedService(&now)
	applied := now
	cases := map[string]struct {
		out   applyOutput
		saved bool
		later time.Duration
		fail  bool
	}{
		"no resources, within ReadyTimeout":    {applyOutput{false, applied}, true, 4 * time.Minute, false},
		"no resources, past ReadyTimeout":      {applyOutput{false, applied}, true, 6 * time.Minute, true},
		"resources, past ReadyTimeout":         {applyOutput{true, applied}, true, 9 * time.Minute, false},
		"resources, long wait is still polled": {applyOutput{true, applied}, true, 12 * time.Minute, false},
		"run from before the output existed":   {applyOutput{}, false, 20 * time.Minute, false},
		"null output":                          {applyOutput{}, true, 20 * time.Minute, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st := stateWith(t, c.out, c.saved)
			if name == "null output" {
				st.Run.Outputs[release.StepApply] = json.RawMessage("null")
			}
			now = applied.Add(c.later)
			err := s.stillWaiting(st)
			if c.fail {
				ctl, ok := workflow.AsControl(err)
				if !ok || ctl.Kind != workflow.ControlFail {
					t.Fatalf("want a failure, got %v", err)
				}
				// the user sees what the step timeout always produced
				f := release.FailureOf(release.StepWaitReady, ctl.Failure)
				want := release.FailureOf(release.StepWaitReady, errors.New("step timed out"))
				if f != want || f.Code != release.CodeNotReady {
					t.Errorf("failure %+v, want %+v", f, want)
				}
				return
			}
			if kind(err) != workflow.ControlPoll {
				t.Errorf("want a poll, got %v", err)
			}
		})
	}
}
