package build

import (
	"context"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/builder"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const trivyImage = "aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb"

func scanCfg() Config {
	c := cfg
	c.Scan = ScanConfig{Image: trivyImage}
	return c
}

func succeeded() *v1alpha1.OdysseyBuild {
	b := newBuild()
	b.Status = v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildSucceeded, ImageDigest: digest}
	return b
}

func TestScanTool(t *testing.T) {
	for image, want := range map[string]string{trivyImage: "trivy 0.76.2", "aquasec/trivy@sha256:abc": "trivy", "localhost:5000/trivy": "trivy"} {
		if got := (ScanConfig{Image: image}).Tool(); got != want {
			t.Errorf("%s: %q, want %q", image, got, want)
		}
	}
}

// The scan Job is bounded and confined: it scans the digest the build
// pushed, with Trivy pinned by digest, a deadline, a TTL, resources, no
// service account and the registry's credentials.
func TestScanJob(t *testing.T) {
	b := succeeded()
	j := ScanJob(b, scanCfg())
	if j.Name != "scan-"+id || j.Namespace != Namespace || j.Labels[LabelScan] != id || j.Annotations[AnnotationDigest] != digest {
		t.Fatalf("job %+v", j.ObjectMeta)
	}
	if len(j.OwnerReferences) != 0 {
		t.Error("the scan must outlive the build: it has no owner")
	}
	s := j.Spec
	if *s.BackoffLimit != 0 || *s.ActiveDeadlineSeconds != 900 || s.TTLSecondsAfterFinished == nil || *s.TTLSecondsAfterFinished <= int32(cfg.withDefaults().Retention.Seconds()) {
		t.Errorf("bounds: backoff %d, deadline %d, ttl %v", *s.BackoffLimit, *s.ActiveDeadlineSeconds, s.TTLSecondsAfterFinished)
	}
	p := s.Template.Spec
	if *p.AutomountServiceAccountToken || !*p.SecurityContext.RunAsNonRoot || p.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("pod %+v", p)
	}
	if len(p.InitContainers) != 3 || len(p.Containers) != 1 {
		t.Fatalf("containers %d %d", len(p.InitContainers), len(p.Containers))
	}
	db, jdb, tr, sum := p.InitContainers[0], p.InitContainers[1], p.InitContainers[2], p.Containers[0]
	if db.Name != ContainerDB || jdb.Name != ContainerJavaDB || tr.Name != ContainerTrivy {
		t.Fatalf("steps %s %s %s", db.Name, jdb.Name, tr.Name)
	}
	cmd := strings.Join(tr.Command, " ")
	for _, want := range []string{"trivy image", "--format json", "--scanners vuln", "--output /out/report.json", "--cache-dir /cache", "--insecure", "--timeout 14m0s",
		"--skip-db-update", "--skip-java-db-update", "registry.test/o/p@" + digest} {
		if !strings.Contains(cmd, want) {
			t.Errorf("trivy command %q lacks %q", cmd, want)
		}
	}
	// M4: --insecure turns TLS verification off for EVERYTHING Trivy
	// connects to, the database download included; so the scan step never
	// downloads (it skips the update), and the download steps never get it.
	for _, d := range []corev1.Container{db, jdb} {
		dc := strings.Join(d.Command, " ")
		if strings.Contains(dc, "--insecure") || strings.Contains(dc, "--skip-db") || strings.Contains(dc, "--skip-java-db") || !strings.Contains(dc, "--download-") {
			t.Errorf("download step %s: %q", d.Name, dc)
		}
		if _, ok := env(d)["DOCKER_CONFIG"]; ok {
			t.Errorf("%s gets the registry's credentials; it only talks to the public database registries", d.Name)
		}
		for _, e := range d.Env {
			if strings.Contains(e.Name, "NON_SSL") || strings.Contains(e.Name, "INSECURE") {
				t.Errorf("%s: %s", d.Name, e.Name)
			}
		}
	}
	for _, e := range tr.Env {
		if strings.Contains(e.Name, "NON_SSL") || strings.Contains(e.Name, "INSECURE") {
			t.Errorf("trivy: %s (use the flag so the manifest shows it)", e.Name)
		}
	}
	if tr.Image != trivyImage || db.Image != trivyImage || jdb.Image != trivyImage || !strings.Contains(trivyImage, "@sha256:") {
		t.Errorf("trivy image %q", tr.Image)
	}
	if env(tr)["DOCKER_CONFIG"] != "/var/run/odyssey/registry" {
		t.Errorf("trivy does not read the registry's credentials: %v", env(tr))
	}
	if sum.Image != cfg.AgentImage || strings.Join(sum.Command, " ") != "odyssey-agent builder scan-summary" || env(sum)["ODYSSEY_SCAN_TOOL"] != "trivy 0.76.2" {
		t.Errorf("summary %+v", sum)
	}
	for _, c := range []corev1.Container{db, jdb, tr, sum} {
		if c.Resources.Limits.Memory().IsZero() || c.Resources.Limits.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() {
			t.Errorf("%s has no resource bounds", c.Name)
		}
		if *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem {
			t.Errorf("%s is not confined", c.Name)
		}
	}
	if s.Suspend == nil || !*s.Suspend {
		t.Error("a scan is queued suspended (ScanQueue runs one at a time)")
	}
	var claim, secret, out bool
	for _, v := range p.Volumes {
		claim = claim || (v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == ScanCacheClaim)
		secret = secret || (v.Secret != nil && v.Secret.SecretName == RegistrySecret && *v.Secret.Optional)
		out = out || (v.Name == "out" && v.EmptyDir != nil && v.EmptyDir.SizeLimit != nil && !v.EmptyDir.SizeLimit.IsZero())
	}
	if !claim || !secret || !out {
		t.Errorf("volumes: cache claim %v, registry secret %v, bounded out %v", claim, secret, out)
	}

	// Over TLS Trivy is not told to skip verification.
	secure := scanCfg()
	secure.InsecureRegistry = false
	if strings.Contains(strings.Join(ScanJob(b, secure).Spec.Template.Spec.InitContainers[2].Command, " "), "--insecure") {
		t.Error("--insecure against a registry that speaks TLS")
	}
}

func TestWarmupJob(t *testing.T) {
	j := WarmupJob(scanCfg())
	p := j.Spec.Template.Spec
	if j.Name != WarmupJobName || j.Labels[LabelScanWarmup] == "" || j.Labels[LabelScan] != "" || !*j.Spec.Suspend {
		t.Fatalf("job %+v", j.ObjectMeta)
	}
	all := append(append([]corev1.Container{}, p.InitContainers...), p.Containers...)
	if len(all) != 2 {
		t.Fatalf("containers %d", len(all))
	}
	for _, c := range all {
		cmd := strings.Join(c.Command, " ")
		if strings.Contains(cmd, "--insecure") || !strings.Contains(cmd, "--download-") || c.Image != trivyImage {
			t.Errorf("%s: %q", c.Name, cmd)
		}
	}
}

func summaryStatus(msg string, code int32) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		InitContainerStatuses: []corev1.ContainerStatus{{Name: ContainerTrivy, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		ContainerStatuses:     []corev1.ContainerStatus{{Name: ContainerSummary, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Message: msg}}}},
	}}
}

func scanJobWith(conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationDigest: digest}}, Status: batchv1.JobStatus{Conditions: conds}}
}

func TestObserveScan(t *testing.T) {
	complete := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	failedC := func(reason string) batchv1.JobCondition {
		return batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: "m"}
	}
	trivyFailed := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
		{Name: ContainerTrivy, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "FATAL failed to download vulnerability DB: dial tcp: lookup ghcr.io: no such host"}}}}}}
	pulling := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
		{Name: ContainerTrivy, Image: "x", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "denied"}}}}}}
	cases := []struct {
		name      string
		job       *batchv1.Job
		pod       *corev1.Pod
		done      bool
		crit, bad int
		errHas    string
	}{
		{"no job", nil, nil, false, 0, 0, ""},
		{"running", scanJobWith(), summaryStatus("", 0), false, 0, 0, ""},
		{"counted", scanJobWith(complete), summaryStatus(`{"critical":2,"high":5,"tool":"trivy 0.76.2"}`, 0), true, 2, 5, ""},
		{"clean", scanJobWith(complete), summaryStatus(`{"tool":"trivy 0.76.2"}`, 0), true, 0, 0, ""},
		{"complete but its pod is not in view", scanJobWith(complete), nil, false, 0, 0, ""},
		{"unreadable summary", scanJobWith(complete), summaryStatus("garbage", 0), true, 0, 0, "unreadable"},
		{"no database network", scanJobWith(failedC("BackoffLimitExceeded")), trivyFailed, true, 0, 0, "vulnerability DB"},
		{"summary failed", scanJobWith(failedC("BackoffLimitExceeded")), summaryStatus(string(builder.Result{Reason: "failed", Message: "the scanner wrote no report"}.Encode()), 1), true, 0, 0, "no report"},
		{"deadline", scanJobWith(failedC(batchv1.JobReasonDeadlineExceeded)), pulling, true, 0, 0, "time limit (the cluster could not pull the image x: denied)"},
		{"failed with no pod", scanJobWith(failedC("BackoffLimitExceeded")), nil, true, 0, 0, "BackoffLimitExceeded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ObserveScan(c.job, c.pod)
			if got.Done != c.done || got.Summary.Critical != c.crit || got.Summary.High != c.bad || !strings.Contains(got.Error, c.errHas) || (c.errHas == "" && got.Error != "") {
				t.Errorf("got %+v", got)
			}
			if c.job != nil && got.Digest != digest {
				t.Errorf("digest %q", got.Digest)
			}
		})
	}
}

// A succeeded build starts its scan, once, and the volume the database
// lives on; nothing waits for it.
func TestReconcileStartsTheScanOnceBuildSucceeds(t *testing.T) {
	ctx := context.Background()
	b := succeeded()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
	r := &Reconciler{Client: c, API: c, Config: scanCfg()}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	for range 2 { // the second finds both and creates nothing
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "scan-" + id}, &job); err != nil {
		t.Fatalf("no scan: %v", err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: ScanCacheClaim}, &pvc); err != nil || pvc.Labels[LabelManagedBy] != ManagedBy {
		t.Fatalf("no cache volume: %v %+v", err, pvc.ObjectMeta)
	}

	// Past the build's retention the build is gone and the scan stays.
	r.Now = func() time.Time { return time.Now().Add(cfg.withDefaults().Retention + time.Minute) }
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &v1alpha1.OdysseyBuild{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the build stays: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&job), &job); err != nil {
		t.Fatalf("the scan went with the build: %v", err)
	}
}

// A reused build whose scan the control plane copies (skip_scan) is not
// scanned; a reused build without skip_scan is, like any other.
func TestScanOfAReusedBuild(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		skip, reused, scanned bool
	}{
		"reused, skip_scan":        {skip: true, reused: true, scanned: false},
		"reused":                   {reused: true, scanned: true},
		"built, skip_scan is moot": {skip: true, scanned: true},
	} {
		t.Run(name, func(t *testing.T) {
			b := succeeded()
			b.Spec.SkipScan = tc.skip
			b.Status.Reused = tc.reused
			c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
			r := &Reconciler{Client: c, API: c, Config: scanCfg()}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}); err != nil {
				t.Fatal(err)
			}
			err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "scan-" + id}, &batchv1.Job{})
			if tc.scanned != (err == nil) {
				t.Fatalf("scanned=%v, want %v (%v)", err == nil, tc.scanned, err)
			}
		})
	}
}

func TestNoScanWhenOffOrTheBuildDidNotSucceed(t *testing.T) {
	ctx := context.Background()
	failedB := newBuild()
	failedB.Status = v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildFailed, Reason: v1alpha1.BuildReasonFailed}
	for name, tc := range map[string]struct {
		b *v1alpha1.OdysseyBuild
		c Config
	}{"off": {succeeded(), cfg}, "failed build": {failedB, scanCfg()}} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(tc.b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
			r := &Reconciler{Client: c, API: c, Config: tc.c}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tc.b)}); err != nil {
				t.Fatal(err)
			}
			var jobs batchv1.JobList
			_ = c.List(ctx, &jobs, client.InNamespace(Namespace))
			for _, j := range jobs.Items {
				if strings.HasPrefix(j.Name, "scan-") {
					t.Errorf("scan %s started", j.Name)
				}
			}
			var pvcs corev1.PersistentVolumeClaimList
			_ = c.List(ctx, &pvcs, client.InNamespace(Namespace))
			if len(pvcs.Items) != 0 {
				t.Errorf("volumes %v", pvcs.Items)
			}
		})
	}
}
