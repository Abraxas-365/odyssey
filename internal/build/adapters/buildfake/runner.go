// Package buildfake is a build.Runner that builds nothing. It stands in for
// the cluster's builder (phase 1b-2) in local development and tests: a
// dispatched build "runs" for a while, printing a scripted log, and then
// succeeds with an image named after its source, or fails as Behavior says.
//
// Progress and log lines derive from the build itself (its creation time and
// ids), so the API process, which never dispatched the build, shows the same
// live log as the worker that did. Only the outcome chosen by Behavior lives
// in memory, in the worker: it is reported by Observe and, at the end, in the
// log the worker archives.
package buildfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Outcome is how a dispatched build ends.
type Outcome struct {
	// Reason, when set, fails the build with it (a build.Reason* constant).
	Reason  string
	Message string
	// Refuse makes Dispatch refuse the build with this code (e.g. NO_BUILDER).
	Refuse string
	// Hold keeps the build running until it is cancelled (whatever Duration).
	Hold bool
	// Lines adds that many build lines after the script (tests of big logs).
	Lines int
}

// Options configure the fake; zero values take the defaults.
type Options struct {
	// Duration is how long a build runs (default 0: it ends at the first Observe).
	Duration time.Duration
	// Registry names the fake images (default "registry.odyssey.localhost").
	Registry string
	// Behavior decides each build's outcome (default: success).
	Behavior func(build.Job) Outcome
	// Now is the clock (tests).
	Now func() time.Time
}

type Runner struct {
	opts Options

	mu        sync.Mutex
	outcomes  map[build.ID]Outcome
	cancelled map[build.ID]bool
	forgotten map[build.ID]bool
	down      error
}

var _ build.Runner = (*Runner)(nil)

// ByName is the local-development behavior, like releasefake's "unhealthy"
// repositories: a source whose name contains "unsupported" fails as an app
// without a start command; any other succeeds.
func ByName(j build.Job) Outcome {
	if strings.Contains(j.Source.Name, "unsupported") {
		return Outcome{Reason: build.ReasonUnsupported, Message: "No start command could be found (railpack: no start command)."}
	}
	return Outcome{}
}

func New(opts Options) *Runner {
	if opts.Registry == "" {
		opts.Registry = "registry.odyssey.localhost"
	}
	if opts.Behavior == nil {
		opts.Behavior = func(build.Job) Outcome { return Outcome{} }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Runner{opts: opts, outcomes: map[build.ID]Outcome{}, cancelled: map[build.ID]bool{}, forgotten: map[build.ID]bool{}}
}

// SetDown makes every call fail with err (nil brings the runner back).
func (r *Runner) SetDown(err error) {
	r.mu.Lock()
	r.down = err
	r.mu.Unlock()
}

// Cancelled reports whether Cancel was asked for b (tests).
func (r *Runner) Cancelled(b build.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled[b]
}

// Forgotten reports whether Forget was asked for b (tests).
func (r *Runner) Forgotten(b build.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forgotten[b]
}

func (r *Runner) Dispatch(_ context.Context, job build.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down != nil {
		return r.down
	}
	o := r.opts.Behavior(job)
	if o.Refuse != "" {
		return build.Refused(o.Refuse, "the fake builder refused the build")
	}
	r.outcomes[job.Build.ID] = o
	return nil
}

// Image is the image the fake "builds" of b: named after its project, with a
// digest derived from the build.
func (r *Runner) Image(b build.Build) string {
	sum := sha256.Sum256([]byte(b.ID.String()))
	return r.opts.Registry + "/" + b.Organization.String() + "/" + b.Project.String() + "@sha256:" + hex.EncodeToString(sum[:])
}

func (r *Runner) Observe(_ context.Context, b build.Build) (build.Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down != nil {
		return build.Observation{}, r.down
	}
	o, known := r.outcomes[b.ID]
	if r.cancelled[b.ID] {
		return build.Observation{Phase: build.PhasePending}, nil
	}
	if !known { // dispatched before this process started: it succeeds
		o = Outcome{}
	}
	if o.Hold || r.elapsed(b) < r.opts.Duration {
		return build.Observation{Phase: build.PhaseRunning}, nil
	}
	if o.Reason != "" {
		return build.Observation{Phase: build.PhaseFailed, Reason: o.Reason, Message: o.Message}, nil
	}
	return build.Observation{Phase: build.PhaseSucceeded, Image: r.Image(b)}, nil
}

func (r *Runner) elapsed(b build.Build) time.Duration {
	return r.opts.Now().Sub(b.CreatedAt)
}

// script is the log every fake build prints, spread over its duration.
var script = []build.LogLine{
	{Stream: "prepare", Text: "↳ descargando el código"},
	{Stream: "prepare", Text: "↳ detectando el tipo de app"},
	{Stream: "build", Text: "#1 [internal] load build definition"},
	{Stream: "build", Text: "#2 [internal] load metadata"},
	{Stream: "build", Text: "#3 install dependencies"},
	{Stream: "build", Text: "#4 build"},
	{Stream: "build", Text: "#5 exporting to image"},
}

func (r *Runner) Logs(_ context.Context, b build.Build, after int64, limit int) ([]build.LogLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down != nil {
		return nil, r.down
	}
	if r.forgotten[b.ID] {
		return nil, errx.NotFound("the builder no longer has this build's log")
	}
	lines := r.lines(b)
	out := []build.LogLine{}
	for _, l := range lines {
		if l.Seq > after && len(out) < limit {
			out = append(out, l)
		}
	}
	return out, nil
}

// lines are what b printed so far.
func (r *Runner) lines(b build.Build) []build.LogLine {
	shown := len(script)
	if b.Status.Finished() && b.StartedAt == nil {
		shown = 0 // refused before it ran: only its end line
	}
	if r.opts.Duration > 0 && !b.Status.Finished() {
		shown = 1 + int(float64(len(script)-1)*float64(r.elapsed(b))/float64(r.opts.Duration))
		shown = min(max(shown, 1), len(script))
	}
	out := make([]build.LogLine, 0, shown+1)
	for i := range shown {
		l := script[i]
		l.Seq = int64(i + 1)
		out = append(out, l)
	}
	if shown < len(script) && !b.Status.Finished() {
		return out
	}
	o, known := r.outcomes[b.ID]
	for i := range o.Lines {
		out = append(out, build.LogLine{Seq: int64(len(out) + 1), Stream: "build", Text: fmt.Sprintf("paso %d de %d", i+1, o.Lines)})
	}
	end := build.LogLine{Seq: int64(len(out) + 1), Stream: "system"}
	switch {
	case r.cancelled[b.ID] || b.Status == build.StatusCancelled:
		end.Text = "construcción cancelada"
	case known && o.Reason != "":
		end.Text = "error: " + strings.TrimSpace(o.Reason+" "+o.Message)
	case b.Status == build.StatusFailed && b.Error != nil:
		end.Text = "error: " + b.Error.Code
	default:
		end.Text = "imagen lista: " + r.Image(b)
	}
	return append(out, end)
}

func (r *Runner) Cancel(_ context.Context, b build.Build) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down != nil {
		return r.down
	}
	if b.Status != build.StatusSucceeded && b.Status != build.StatusFailed {
		r.cancelled[b.ID] = true
	}
	return nil
}

func (r *Runner) Forget(_ context.Context, b build.Build) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down != nil {
		return r.down
	}
	r.forgotten[b.ID] = true
	delete(r.outcomes, b.ID)
	return nil
}
