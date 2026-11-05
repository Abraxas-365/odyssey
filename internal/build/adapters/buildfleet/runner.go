// Package buildfleet is the build.Runner adapter on the fleet: a build goes
// to the cluster its environment is placed on, whose agent builds it in a
// Job and pushes the image to the registry the cluster announced; status
// and log lines come back through the tunnel. It depends on fleet's Builds
// port only (never on fleetsvc).
package buildfleet

import (
	"context"
	"errors"
	"time"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
)

// Deadline bounds a build in the cluster: shorter than build.Timeout, so
// the cluster's own words ("timeout") arrive before the workflow gives up.
const Deadline = 20 * time.Minute

type Runner struct{ fleet fleet.Builds }

func New(builds fleet.Builds) *Runner { return &Runner{fleet: builds} }

var _ build.Runner = (*Runner)(nil)

func buildID(b build.Build) (fleet.BuildID, error) {
	id, err := fleet.ParseBuildID(b.ID.String())
	if err != nil {
		return fleet.BuildID{}, errx.Wrap(err, "build id", errx.TypeInternal)
	}
	return id, nil
}

// Dispatch sends the build to its environment's cluster. What retrying
// cannot fix (no cluster, a revoked one, a cluster without a builder) is
// refused with a code the build fails with; the rest is retried.
func (r *Runner) Dispatch(ctx context.Context, job build.Job) error {
	id, err := buildID(job.Build)
	if err != nil {
		return err
	}
	err = r.fleet.DispatchBuild(ctx, fleet.DesiredBuild{
		ID: id, Organization: job.Build.Organization, Project: job.Build.Project, Environment: job.Build.Environment,
		SourceKey: job.Source.Key, SourceSHA256: job.Source.SHA256, SourceSize: job.Source.Size, Timeout: Deadline,
	})
	var e *errx.Error
	if errors.As(err, &e) && e.Type == errx.TypeBusiness {
		switch e.Code {
		case fleet.CodeNoBuilder:
			return build.Refused(build.CodeNoBuilder, e.Message)
		case fleet.CodeNoCluster, fleet.CodeClusterRevoked:
			return build.Refused(build.CodeNoCluster, e.Message)
		}
	}
	return err
}

func (r *Runner) Observe(ctx context.Context, b build.Build) (build.Observation, error) {
	id, err := buildID(b)
	if err != nil {
		return build.Observation{}, err
	}
	s, err := r.fleet.BuildStatus(ctx, b.Organization, id)
	if err != nil {
		return build.Observation{}, err
	}
	o := build.Observation{Phase: build.PhasePending, Message: s.Message}
	switch s.Phase {
	case fleet.BuildRunning:
		o.Phase = build.PhaseRunning
	case fleet.BuildSucceeded:
		o.Phase, o.Image = build.PhaseSucceeded, s.Image
	case fleet.BuildFailed:
		o.Phase, o.Reason = build.PhaseFailed, s.Reason
		if o.Reason == "" {
			o.Reason = build.ReasonFailed
		}
	}
	return o, nil
}

func (r *Runner) Logs(ctx context.Context, b build.Build, after int64, limit int) ([]build.LogLine, error) {
	id, err := buildID(b)
	if err != nil {
		return nil, err
	}
	page, err := r.fleet.BuildLogs(ctx, b.Organization, id, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]build.LogLine, len(page.Lines))
	for i, l := range page.Lines {
		out[i] = build.LogLine{Seq: l.Seq, Stream: l.Stream, Text: l.Text}
	}
	return out, nil
}

// Cancel lets the cluster go of the build: it stops it if it still runs
// and deletes its Job either way (build.archive calls it for every build).
func (r *Runner) Cancel(ctx context.Context, b build.Build) error {
	id, err := buildID(b)
	if err != nil {
		return err
	}
	return r.fleet.CancelBuild(ctx, b.Organization, id)
}

func (r *Runner) Forget(ctx context.Context, b build.Build) error {
	id, err := buildID(b)
	if err != nil {
		return err
	}
	return r.fleet.ForgetBuildLogs(ctx, b.Organization, id)
}

// ObserveScan is what the cluster reported of the image's scan: pending
// while its agent scans, unsupported when it does not list the capability.
func (r *Runner) ObserveScan(ctx context.Context, b build.Build) (build.ScanObservation, error) {
	id, err := buildID(b)
	if err != nil {
		return build.ScanObservation{}, err
	}
	s, err := r.fleet.BuildScan(ctx, b.Organization, id)
	if err != nil {
		return build.ScanObservation{}, err
	}
	switch s.Phase {
	case fleet.ScanDone:
		sc := build.Scan{Digest: s.Status.Digest, Critical: s.Status.Critical, High: s.Status.High, Medium: s.Status.Medium,
			Low: s.Status.Low, Tool: s.Status.Tool, DBUpdatedAt: s.Status.DBUpdatedAt, Error: s.Status.Error, ScannedAt: s.Status.ObservedAt}
		if sc.Error != "" {
			sc.Error = build.ScanFailedMessage + " " + sc.Error
		}
		return build.ScanObservation{Phase: build.ScanDone, Scan: sc}, nil
	case fleet.ScanPending:
		return build.ScanObservation{Phase: build.ScanPending}, nil
	}
	return build.ScanObservation{Phase: build.ScanUnsupported}, nil
}
