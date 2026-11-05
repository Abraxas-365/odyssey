package buildsvc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Archive limits: a runaway build log is cut, not stored whole.
const (
	maxArchivedLines = 50_000
	maxLineBytes     = 8 << 10
)

// Workflows returns build.run and build.archive:
//
//	build.run      dispatch  hand the build to the runner
//	               wait      follow it until it ends (bounded by the build timeout)
//	               finish    record the outcome
//	build.archive  archive   stop it in the runner, store its log, let the runner forget it
//
// Every step first reads the build: once it is finished (a newer deployment
// cancelled it) the remaining steps of build.run do nothing but hand it to
// build.archive. A failure (a refusal, retries exhausted, the timeout) fails
// the build in OnFailure, which also starts build.archive.
func (s *Service) Workflows() []workflow.Definition {
	return []workflow.Definition{
		{
			Name: build.WorkflowRun,
			Steps: []workflow.Step{
				// ~4 min of retries ride out a cluster briefly unreachable.
				{Name: build.StepDispatch, Run: s.dispatch, MaxAttempts: 8},
				{Name: build.StepWait, Run: s.wait, Timeout: s.opts.Timeout, MaxAttempts: 30},
				{Name: build.StepFinish, Run: s.finish},
			},
			OnFailure: s.runFailed,
		},
		{
			Name:  build.WorkflowArchive,
			Steps: []workflow.Step{{Name: build.StepArchive, Run: s.archive, MaxAttempts: 10}},
		},
		{
			// Polls don't count as attempts; the step's Timeout bounds a
			// cluster that never reports and OnFailure then records why.
			Name:      build.WorkflowScan,
			Steps:     []workflow.Step{{Name: build.StepScan, Run: s.scan, Timeout: s.opts.ScanTimeout, MaxAttempts: 30}},
			OnFailure: s.scanFailed,
		},
	}
}

// scan follows the runner's scan of a succeeded build and records it, once.
// A runner that does not scan, one that reports another image's scan or one
// that never reports (the timeout) leaves the build "not scanned" with the
// reason: a deployment waiting for the scan then proceeds as known=false.
func (s *Service) scan(ctx context.Context, st *workflow.State) (any, error) {
	b, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	if b.Status != build.StatusSucceeded || b.Scan != nil {
		return nil, nil
	}
	o, err := s.runner.ObserveScan(ctx, b)
	if err != nil {
		return nil, err
	}
	var scan build.Scan
	switch o.Phase {
	case build.ScanPending:
		return nil, workflow.Poll(s.opts.PollEvery)
	case build.ScanDone:
		scan = o.Scan
		if d := build.DigestOf(b.Image); d == "" || scan.Digest != d {
			scan = build.Scan{Error: build.ScanMismatchMessage}
		}
	default:
		scan = build.Scan{Error: build.ScanUnsupportedMessage}
	}
	if scan.Error != "" {
		scan.Critical, scan.High, scan.Medium, scan.Low = 0, 0, 0, 0
	}
	return nil, s.record(ctx, b.Organization, b.ID, scan)
}

// scanFailed records the scan that never came (the step timed out or kept
// failing). It runs in the transaction that marks the run failed.
func (s *Service) scanFailed(ctx context.Context, st *workflow.State, _ error) error {
	b, err := s.load(ctx, st)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Status != build.StatusSucceeded || b.Scan != nil {
		return nil
	}
	return s.record(ctx, b.Organization, b.ID, build.Scan{Error: build.ScanTimeoutMessage})
}

// record stores a build's scan once and publishes build.scanned in the same
// transaction.
func (s *Service) record(ctx context.Context, org identity.OrganizationID, id build.ID, scan build.Scan) error {
	if scan.ScannedAt.IsZero() {
		scan.ScannedAt = time.Now().UTC()
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		b, err := s.repo.FindBuildForUpdate(ctx, org, id)
		if err != nil {
			return err
		}
		if b.Status != build.StatusSucceeded || b.Scan != nil {
			return nil
		}
		if ok, err := s.repo.SetScan(ctx, org, id, scan); err != nil || !ok {
			return err
		}
		b.Scan = &scan
		n, err := s.names(ctx, org, b.Project)
		if err != nil {
			return err
		}
		return s.events.Publish(ctx, build.NewScanned(org, b, n))
	})
}

// load reads the run's build.
func (s *Service) load(ctx context.Context, st *workflow.State) (build.Build, error) {
	var in build.RunInput
	if err := st.Input(&in); err != nil {
		return build.Build{}, workflow.Fail(err)
	}
	return s.repo.FindBuild(ctx, st.Run.Organization, in.Build)
}

func (s *Service) dispatch(ctx context.Context, st *workflow.State) (any, error) {
	b, err := s.load(ctx, st)
	if err != nil || b.Status.Finished() {
		return nil, err
	}
	src, err := s.repo.FindSource(ctx, b.Organization, b.Source)
	if err != nil {
		return nil, err
	}
	if src.Expired() {
		return nil, workflow.Fail(build.Refused(build.CodeSourceExpired, "the source was swept before it was built"))
	}
	if err := s.runner.Dispatch(ctx, build.Job{Build: b, Source: src}); err != nil {
		if refused(err) {
			return nil, workflow.Fail(err)
		}
		return nil, err // the runner is unreachable: retry
	}
	return nil, nil
}

// waited is the wait step's output.
type waited struct {
	Observation *build.Observation `json:"observation"` // nil: the build was finished elsewhere
}

func (s *Service) wait(ctx context.Context, st *workflow.State) (any, error) {
	b, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	if b.Status.Finished() {
		return waited{}, nil
	}
	o, err := s.runner.Observe(ctx, b)
	if err != nil {
		return nil, err
	}
	switch o.Phase {
	case build.PhaseSucceeded, build.PhaseFailed:
		return waited{Observation: &o}, nil
	case build.PhaseRunning:
		if b.Status == build.StatusQueued {
			if err := s.repo.MarkRunning(ctx, b.Organization, b.ID); err != nil {
				return nil, err
			}
		}
	}
	return nil, workflow.Poll(s.opts.PollEvery)
}

func (s *Service) finish(ctx context.Context, st *workflow.State) (any, error) {
	var w waited
	if err := st.Output(build.StepWait, &w); err != nil {
		return nil, workflow.Fail(err)
	}
	var in build.RunInput
	if err := st.Input(&in); err != nil {
		return nil, workflow.Fail(err)
	}
	org := st.Run.Organization
	return nil, s.tx.Do(ctx, func(ctx context.Context) error {
		b, err := s.repo.FindBuildForUpdate(ctx, org, in.Build)
		if err != nil {
			return err
		}
		if !b.Status.Finished() && w.Observation != nil {
			o := *w.Observation
			switch {
			case o.Phase == build.PhaseSucceeded && build.PinnedImage(o.Image):
				b.Image = o.Image
				err = s.settle(ctx, b, build.StatusSucceeded, nil)
			case o.Phase == build.PhaseSucceeded:
				f := build.Failure{Code: build.CodeInternal, Message: build.Message(build.CodeInternal),
					Detail: "the builder reported an image not pinned by digest"}
				err = s.settle(ctx, b, build.StatusFailed, &f)
			default:
				f := build.FailureOf(o)
				err = s.settle(ctx, b, build.StatusFailed, &f)
			}
			if err != nil {
				return err
			}
		}
		return s.startArchive(ctx, b)
	})
}

// runFailed fails the build (unless something else finished it) and has its
// log archived. It runs in the transaction that marks the run failed.
func (s *Service) runFailed(ctx context.Context, st *workflow.State, cause error) error {
	var in build.RunInput
	if err := st.Input(&in); err != nil {
		return nil // nothing to fail: the input itself is broken
	}
	b, err := s.repo.FindBuildForUpdate(ctx, st.Run.Organization, in.Build)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !b.Status.Finished() {
		f := build.FailureFromError(st.Run.StepName, unwrap(cause))
		if err := s.settle(ctx, b, build.StatusFailed, &f); err != nil {
			return err
		}
	}
	return s.startArchive(ctx, b)
}

// settle finishes a locked build and publishes the outcome.
func (s *Service) settle(ctx context.Context, b build.Build, status build.Status, f *build.Failure) error {
	if err := s.repo.Finish(ctx, b.Organization, b.ID, status, b.Image, f); err != nil {
		return err
	}
	n, err := s.names(ctx, b.Organization, b.Project)
	if err != nil {
		return err
	}
	if status == build.StatusSucceeded {
		if err := s.events.Publish(ctx, build.NewSucceeded(b.Organization, b, n)); err != nil {
			return err
		}
		// The scan is followed on its own: the build never waits for it.
		_, err := s.engine.Start(ctx, workflow.Start{Workflow: build.WorkflowScan, Organization: b.Organization,
			Subject: event.Subject(build.SubjectKind, b.ID), Input: build.RunInput{Build: b.ID},
			IdempotencyKey: b.ID.String()})
		return err
	}
	return s.events.Publish(ctx, build.NewFailed(b.Organization, b, *f, n))
}

func (s *Service) names(ctx context.Context, organization identity.OrganizationID, id project.ID) (build.Names, error) {
	p, err := s.projects.Find(ctx, organization, id)
	if err != nil {
		return build.Names{}, err
	}
	return build.Names{Project: p.Name}, nil
}

func (s *Service) startArchive(ctx context.Context, b build.Build) error {
	_, err := s.engine.Start(ctx, workflow.Start{Workflow: build.WorkflowArchive, Organization: b.Organization,
		Subject: event.Subject(build.SubjectKind, b.ID), Input: build.RunInput{Build: b.ID},
		IdempotencyKey: b.ID.String()})
	return err
}

// archive stops the build in the runner (a no-op unless it was cancelled or
// timed out), stores its log under a fresh key and lets the runner forget it.
func (s *Service) archive(ctx context.Context, st *workflow.State) (any, error) {
	b, err := s.load(ctx, st)
	if err != nil {
		return nil, err
	}
	if !b.Status.Finished() {
		return nil, workflow.Fail(errx.Internal("build " + b.ID.String() + " is not finished"))
	}
	if b.LogsKey == "" {
		if err := s.runner.Cancel(ctx, b); err != nil {
			return nil, err
		}
		data, index, err := s.collect(ctx, b)
		if err != nil {
			return nil, err
		}
		key := logsKey(b)
		err = s.blobs.Put(ctx, key, bytes.NewReader(data), blob.PutOptions{ContentType: "application/x-ndjson"})
		if err != nil && !errx.IsType(err, errx.TypeConflict) { // Conflict: an earlier attempt stored it
			return nil, err
		}
		if errx.IsType(err, errx.TypeConflict) {
			// The stored log may differ from this collection (the runner
			// kept printing), so its marks are not this index's: read it
			// from the top rather than trust them.
			index = nil
		}
		if err := s.repo.SetLogs(ctx, b.Organization, b.ID, key, index); err != nil {
			return nil, err
		}
	}
	return nil, s.runner.Forget(ctx, b)
}

// collect reads the whole log from the runner as JSON lines, marking where
// every build.LogIndexEvery-th line starts.
func (s *Service) collect(ctx context.Context, b build.Build) ([]byte, build.LogIndex, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keeps "<" as one byte; archived() sizes its buffer for the worst case
	index := build.LogIndex{}
	after, n := int64(0), 0
	for n < maxArchivedLines {
		lines, err := s.runner.Logs(ctx, b, after, build.MaxLogPage)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range lines {
			if l.Seq <= after {
				continue // a runner must not resend, but a duplicate costs nothing to drop
			}
			if len(l.Text) > maxLineBytes {
				l.Text = l.Text[:maxLineBytes] + "…"
			}
			if n > 0 && n%build.LogIndexEvery == 0 {
				index = append(index, build.LogMark{Seq: l.Seq, Offset: int64(buf.Len())})
			}
			if err := enc.Encode(l); err != nil {
				return nil, nil, errx.Wrap(err, "encode build log", errx.TypeInternal)
			}
			after = l.Seq
			n++
		}
		if len(lines) < build.MaxLogPage {
			return buf.Bytes(), index, nil
		}
	}
	_ = enc.Encode(build.LogLine{Seq: after + 1, Stream: "system", Text: "(log cut: the build printed too much)"})
	return buf.Bytes(), index, nil
}

// liveGrace is how long after a build ends its log is still read from the
// runner (while build.archive runs) before the page is called final anyway.
const liveGrace = 2 * time.Minute

// Logs pages a build's log: from the archive once stored, else live from
// the runner.
func (s *Service) Logs(ctx context.Context, organization identity.OrganizationID, id build.ID, q build.LogQuery) (build.LogPage, error) {
	q = q.Normalize()
	if err := q.Validate(); err != nil {
		return build.LogPage{}, err
	}
	b, err := s.repo.FindBuild(ctx, organization, id)
	if err != nil {
		return build.LogPage{}, err
	}
	var lines []build.LogLine
	if b.LogsKey != "" {
		lines, err = s.archived(ctx, b, q)
	} else {
		lines, err = s.runner.Logs(ctx, b, q.After, q.Limit)
	}
	if err != nil {
		return build.LogPage{}, err
	}
	page := build.LogPage{Lines: lines, Next: q.After}
	if page.Lines == nil {
		page.Lines = []build.LogLine{}
	}
	if len(lines) > 0 {
		page.Next = lines[len(lines)-1].Seq
	}
	ended := b.LogsKey != "" || (b.FinishedAt != nil && time.Since(*b.FinishedAt) > liveGrace)
	page.Finished = b.Status.Finished() && ended && len(lines) < q.Limit
	return page, nil
}

func (s *Service) archived(ctx context.Context, b build.Build, q build.LogQuery) ([]build.LogLine, error) {
	start := b.LogsIndex.Start(q.After)
	out, landed, err := s.readArchive(ctx, b, start, q)
	if err == nil && !landed { // a mark that is not a line start: never trust it over the log
		slog.Warn("archived log index is off; reading from the top", "build", b.ID, "seq", start.Seq, "offset", start.Offset)
		out, _, err = s.readArchive(ctx, b, build.LogMark{}, q)
	}
	return out, err
}

// readArchive reads the lines after q.After from mark on; landed reports
// whether the first line there is the mark's (always true from the top).
func (s *Service) readArchive(ctx context.Context, b build.Build, mark build.LogMark, q build.LogQuery) ([]build.LogLine, bool, error) {
	r, err := s.blobs.OpenAt(ctx, b.LogsKey, mark.Offset)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	// Worst case of one encoded line: every byte of its text escaped to six
	// (a control byte "\u0001", invalid UTF-8 "\ufffd"), plus seq and stream
	// (~50 KiB). The scanner allows the larger of this and the buffer's
	// 64 KiB, so any line collect wrote reads back.
	sc.Buffer(make([]byte, 0, 64<<10), 6*(maxLineBytes+len("…"))+1024)
	var out []build.LogLine
	first, landed := true, mark.Offset == 0
	for sc.Scan() && len(out) < q.Limit {
		var l build.LogLine
		err := json.Unmarshal(sc.Bytes(), &l)
		if first && mark.Offset > 0 {
			if err != nil || l.Seq != mark.Seq {
				return nil, false, nil
			}
			landed = true
		}
		first = false
		if err != nil {
			slog.Warn("skipping an unreadable archived log line", "build", b.ID, "error", err)
			continue
		}
		if l.Seq > q.After {
			out = append(out, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, errx.Wrap(err, "read archived build log", errx.TypeInternal)
	}
	return out, landed, nil
}

// refused reports a runner's coded refusal (permanent: no retry).
func refused(err error) bool {
	var e *errx.Error
	return errors.As(err, &e) && e.Code != "" && build.Message(e.Code) != build.Message(build.CodeInternal)
}

// unwrap digs a Fail's cause out of its control wrapper.
func unwrap(err error) error {
	var c *workflow.Control
	if errors.As(err, &c) && c.Failure != nil {
		return c.Failure
	}
	return err
}
