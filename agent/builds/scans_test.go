package builds

import (
	"context"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const scanDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func scanJob(buildID string, conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: buildctl.ScanJobName(buildID), Namespace: buildctl.Namespace,
		Labels: map[string]string{buildctl.LabelScan: buildID}, Annotations: map[string]string{buildctl.AnnotationDigest: scanDigest}},
		Status: batchv1.JobStatus{Conditions: conds}}
}

func scanPod(buildID, summary string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "scan-" + buildID + "-x", Namespace: buildctl.Namespace, Labels: map[string]string{buildctl.LabelScan: buildID}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: buildctl.ContainerSummary,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: summary}}}}}}
}

func TestScans(t *testing.T) {
	ctx := context.Background()
	other := "44444444-4444-4444-4444-444444444444"
	complete := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	c := newClient(t,
		scanJob(id, complete), scanPod(id, `{"critical":2,"high":5,"medium":1,"low":3,"tool":"trivy 0.76.2","db_updated_at":"2026-11-05T07:00:54Z"}`),
		scanJob(other), // still running
	)
	m := &Manager{Client: c}

	got, err := m.Scan(ctx, id)
	if err != nil || got.GetBuildId() != id || got.GetImageDigest() != scanDigest || got.GetCritical() != 2 || got.GetHigh() != 5 ||
		got.GetMedium() != 1 || got.GetLow() != 3 || got.GetTool() != "trivy 0.76.2" || got.GetDbUpdatedAt() != "2026-11-05T07:00:54Z" || got.GetError() != "" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := m.Scan(ctx, other); err != nil || got != nil {
		t.Errorf("a scan that runs is no outcome: %v %v", got, err)
	}
	if got, err := m.Scan(ctx, "55555555-5555-5555-5555-555555555555"); err != nil || got != nil {
		t.Errorf("no scan: %v %v", got, err)
	}
	if got, err := m.Scan(ctx, "../x"); err != nil || got != nil {
		t.Errorf("not a build id: %v %v", got, err)
	}
	all, err := m.Scans(ctx)
	if err != nil || len(all) != 1 || all[0].GetBuildId() != id {
		t.Fatalf("only the ended scans: %v %v", all, err)
	}
}

func TestAFailedScanReportsWhy(t *testing.T) {
	ctx := context.Background()
	failed := batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}
	m := &Manager{Client: newClient(t, scanJob(id, failed))}
	got, err := m.Scan(ctx, id)
	if err != nil || got.GetError() == "" || got.GetCritical() != 0 || got.GetImageDigest() != scanDigest {
		t.Fatalf("%v %v", got, err)
	}
}

// A scan the agent gave up starting is reported from the build that recorded
// why, so the control plane learns "never" without waiting out its timeout.
func TestAScanGivenUpIsReportedFromItsBuild(t *testing.T) {
	ctx := context.Background()
	ob := &v1alpha1.OdysseyBuild{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: buildctl.Namespace, Annotations: map[string]string{buildctl.AnnotationScanError: "the scan could not be started: boom"}},
		Status:     v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildSucceeded, ImageDigest: scanDigest},
	}
	m := &Manager{Client: newClient(t, ob)}
	got, err := m.Scan(ctx, id)
	if err != nil || got.GetError() != "the scan could not be started: boom" || got.GetImageDigest() != scanDigest {
		t.Fatalf("%v %v", got, err)
	}
	all, err := m.Scans(ctx)
	if err != nil || len(all) != 1 || all[0].GetBuildId() != id {
		t.Fatalf("%v %v", all, err)
	}
	// a build with no scan error and no Job has no scan yet
	ob.Annotations = nil
	m = &Manager{Client: newClient(t, ob)}
	if got, err := m.Scan(ctx, id); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}
