package builds

import (
	"context"
	"sort"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scan is the outcome of the build's scan (nil while it runs, or when the
// cluster has no scan of that build: it was never started or has expired).
// A scan the agent gave up starting is its build's recorded reason.
func (m *Manager) Scan(ctx context.Context, id string) (*fleetv1.ScanStatus, error) {
	if !idPattern.MatchString(id) {
		return nil, nil
	}
	var job batchv1.Job
	err := m.Client.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: buildctl.ScanJobName(id)}, &job)
	if apierrors.IsNotFound(err) {
		var ob v1alpha1.OdysseyBuild
		if err := m.Client.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &ob); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		if o, ok := buildctl.ScanNotStarted(&ob); ok {
			return ScanToProto(id, o), nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.scanOf(ctx, &job)
}

// Scans are the outcomes of every scan that ended.
func (m *Manager) Scans(ctx context.Context) ([]*fleetv1.ScanStatus, error) {
	var jobs batchv1.JobList
	if err := m.Client.List(ctx, &jobs, client.InNamespace(buildctl.Namespace), client.HasLabels{buildctl.LabelScan}); err != nil {
		return nil, err
	}
	var out []*fleetv1.ScanStatus
	for i := range jobs.Items {
		s, err := m.scanOf(ctx, &jobs.Items[i])
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, s)
		}
	}
	var builds v1alpha1.OdysseyBuildList
	if err := m.Client.List(ctx, &builds, client.InNamespace(buildctl.Namespace)); err != nil {
		return nil, err
	}
	for i := range builds.Items {
		if o, ok := buildctl.ScanNotStarted(&builds.Items[i]); ok {
			out = append(out, ScanToProto(builds.Items[i].Name, o))
		}
	}
	return out, nil
}

func (m *Manager) scanOf(ctx context.Context, job *batchv1.Job) (*fleetv1.ScanStatus, error) {
	id := job.Labels[buildctl.LabelScan]
	var pods corev1.PodList
	if err := m.Client.List(ctx, &pods, client.InNamespace(buildctl.Namespace), client.MatchingLabels{buildctl.LabelScan: id}); err != nil {
		return nil, err
	}
	var pod *corev1.Pod
	if len(pods.Items) > 0 {
		sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[j].CreationTimestamp.Before(&pods.Items[i].CreationTimestamp) })
		pod = &pods.Items[0]
	}
	return ScanToProto(id, buildctl.ObserveScan(job, pod)), nil
}

// ScanToProto is a finished scan as the protocol reports it (nil: it has not
// ended).
func ScanToProto(id string, o buildctl.ScanOutcome) *fleetv1.ScanStatus {
	if !o.Done {
		return nil
	}
	if o.Error != "" {
		return &fleetv1.ScanStatus{BuildId: id, ImageDigest: o.Digest, Error: o.Error}
	}
	return &fleetv1.ScanStatus{BuildId: id, ImageDigest: o.Digest,
		Critical: int32(o.Summary.Critical), High: int32(o.Summary.High), Medium: int32(o.Summary.Medium), Low: int32(o.Summary.Low),
		Tool: o.Summary.Tool, DbUpdatedAt: o.Summary.DBUpdatedAt}
}
