package build

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The scan queue. Trivy's database lives on one ReadWriteOnce volume and two
// Trivy processes should not meet over it, so a cluster runs ONE scan Job at
// a time. Jobs are created suspended (ScanJob, WarmupJob): that is the whole
// queue, kept by the cluster, not by the agent (a restart loses nothing, and
// it does not depend on the build, which the control plane deletes as soon as
// it has its result). ScanQueue resumes the oldest suspended Job when none
// is running, so scans go first in, first out by the time their Job was made
// (right after their build ended; ties within a second by name).
//
// The wait is outside the pod: a suspended Job has no pod and its
// activeDeadlineSeconds (the scan's time limit) starts when it is resumed,
// so a scan that waits behind three others is not penalized for it. The
// control plane's own wait (release.ScanWait, 10 minutes) is what bounds how
// long a deployment waits for a queued scan.

// ScanQueue starts the queued scan Jobs one at a time and keeps the databases
// warm. It is a manager Runnable (run by the leader only).
type ScanQueue struct {
	Client client.Client
	// API reads what the agent does not cache (the cache volume).
	API    client.Reader
	Config Config
	// Every is how often the queue looks (default 10s: a scan takes minutes).
	Every time.Duration
	// Now is the clock (tests); time.Now by default.
	Now func() time.Time

	warmedAt time.Time
}

// warmupEvery is how often a missing warm-up Job is looked for (its TTL, a
// day, is what makes the databases get a fresh look daily).
const warmupEvery = time.Hour

func (q *ScanQueue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// Start runs the queue until ctx ends.
func (q *ScanQueue) Start(ctx context.Context) error {
	every := q.Every
	if every <= 0 {
		every = 10 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := q.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("scan queue", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick makes one pass: it resumes the oldest queued Job if none is running
// and, now and then, makes sure the databases are being warmed.
func (q *ScanQueue) Tick(ctx context.Context) error {
	if !q.Config.Scan.Enabled() {
		return nil
	}
	if q.now().Sub(q.warmedAt) >= warmupEvery {
		// the warm-up only saves the first scan its download: when it cannot be
		// made (quota, RBAC) the queue still goes on, each scan downloads itself
		if err := q.ensureWarmup(ctx); err != nil {
			slog.Warn("scan database warm-up", "err", err)
		} else {
			q.warmedAt = q.now()
		}
	}
	var jobs []batchv1.Job
	for _, label := range []string{LabelScan, LabelScanWarmup} {
		var l batchv1.JobList
		if err := q.Client.List(ctx, &l, client.InNamespace(Namespace), client.HasLabels{label}); err != nil {
			return err
		}
		jobs = append(jobs, l.Items...)
	}
	var queued []*batchv1.Job
	for i := range jobs {
		j := &jobs[i]
		switch {
		case finishedJob(j):
		case ptr.Deref(j.Spec.Suspend, false):
			queued = append(queued, j)
		case q.stuck(j):
			// past its time limit and still not failed: its pod is stuck on a
			// lost node (Kubernetes marks the Job failed only once the pod is
			// gone). It no longer holds the queue.
			slog.Warn("scan Job past its time limit, not waiting for it", "job", j.Name)
		default:
			return nil // one runs: the volume is taken
		}
	}
	if len(queued) == 0 {
		return nil
	}
	sort.Slice(queued, func(i, j int) bool {
		a, b := queued[i], queued[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Name < b.Name
	})
	next := queued[0]
	patch := client.MergeFrom(next.DeepCopy())
	next.Spec.Suspend = ptr.To(false)
	if err := q.Client.Patch(ctx, next, patch); err != nil {
		return client.IgnoreNotFound(fmt.Errorf("start the queued scan %s: %w", next.Name, err))
	}
	return nil
}

// stuckGrace is how long past its time limit a running Job may go unfailed
// before the queue stops waiting for it.
const stuckGrace = 5 * time.Minute

// stuck says a resumed Job has run past its time limit by more than
// stuckGrace without being marked failed.
func (q *ScanQueue) stuck(j *batchv1.Job) bool {
	if j.Status.StartTime == nil || j.Spec.ActiveDeadlineSeconds == nil {
		return false
	}
	limit := time.Duration(*j.Spec.ActiveDeadlineSeconds)*time.Second + stuckGrace
	return q.now().Sub(j.Status.StartTime.Time) > limit
}

// finishedJob says the Job ended, either way.
func finishedJob(j *batchv1.Job) bool {
	return jobCondition(j, batchv1.JobComplete) != nil || jobCondition(j, batchv1.JobFailed) != nil
}

// ensureWarmup makes the Job that downloads the databases when there is none
// (the first scan of a cluster would otherwise carry the download: minutes).
// It is cheap when the cached databases are fresh (Trivy does nothing).
func (q *ScanQueue) ensureWarmup(ctx context.Context) error {
	var job batchv1.Job
	err := q.Client.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: WarmupJobName}, &job)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := ensureCache(ctx, q.Client, q.API); err != nil {
		return err
	}
	if err := q.Client.Create(ctx, WarmupJob(q.Config)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("start the database warm-up: %w", err)
	}
	return nil
}

// ensureCache creates the volume the databases live on, when missing.
func ensureCache(ctx context.Context, c client.Client, api client.Reader) error {
	var pvc corev1.PersistentVolumeClaim
	switch err := api.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: ScanCacheClaim}, &pvc); {
	case apierrors.IsNotFound(err):
		if err := c.Create(ctx, ScanCache()); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create the scan's cache volume: %w", err)
		}
	case err != nil:
		return err
	}
	return nil
}
