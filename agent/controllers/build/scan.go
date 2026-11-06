package build

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/builder"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The scan (3c): after a build succeeds the agent runs one Job in the builds
// namespace that scans the image it pushed with Trivy, and the tunnel reports
// the outcome (fleet.v1 ScanReport). The Job is NOT owned by the OdysseyBuild:
// the control plane releases a finished build right away (CancelBuild deletes
// it) and the scan, which takes minutes, must outlive that. The Job is the
// scan's whole state: ObserveScan reads the outcome from it and its pod, so
// there is nothing to keep in step and nothing to patch.
const (
	// LabelScan names the build whose image a scan Job (or its pod) scans.
	// It is deliberately not LabelBuild: a build's pod is "the newest pod
	// labelled LabelBuild".
	LabelScan = "odyssey.dev/scan"
	// AnnotationDigest is the image digest a scan Job scans (the build's).
	AnnotationDigest = "odyssey.dev/image-digest"

	// ScanCacheClaim is the PVC that keeps Trivy's vulnerability database
	// between scans (it is downloaded once, not for every scan).
	ScanCacheClaim = "odyssey-trivy-cache"
	// scanCacheSize holds the database (about 1.7 GB), the Java index when an
	// image has jars (about 0.9 GB) and the download's temporary copy.
	scanCacheSize = "8Gi"
	// scanOutSize bounds the emptyDir the report is written to (the summary
	// container reads at most builder.MaxScanReport of it).
	scanOutSize = "512Mi"

	// LabelScanWarmup marks the Job that downloads the databases ahead of the
	// first scan (Warmup). It takes the cache like a scan does, so it queues
	// with them (ScanQueue), but it is no build's scan: it is not LabelScan.
	LabelScanWarmup = "odyssey.dev/scan-warmup"
	// WarmupJobName is the warm-up Job.
	WarmupJobName = "scan-warmup"
	// warmupTTL keeps a finished warm-up Job; while it exists no other warm-up
	// is made (the scans themselves refresh the database when it is stale).
	warmupTTL = 24 * time.Hour
	// downloadTimeout bounds each database download (a cluster with no way
	// out fails fast; a real download takes a minute or two).
	downloadTimeout = 5 * time.Minute

	// Containers of a scan's pod. The databases are downloaded by two init
	// containers WITHOUT --insecure (Trivy's flag turns TLS verification off
	// for every connection it makes, the database download included), then
	// trivy scans with --skip-db-update (so --insecure only reaches the
	// registry) and summary counts its report.
	ContainerDB      = "trivy-db"
	ContainerJavaDB  = "trivy-javadb"
	ContainerTrivy   = "trivy"
	ContainerSummary = "summary"

	// scanStartRetry is how often the start of a scan that failed to start is
	// tried again, and scanStartWindow how long after the build ended (the
	// control plane stops waiting for the scan ten minutes after it).
	scanStartRetry  = 10 * time.Second
	scanStartWindow = 5 * time.Minute
)

// ScanJobName is the Job of the scan of a build.
func ScanJobName(buildID string) string { return "scan-" + buildID }

// ScanConfig turns the scan on (Image set) and bounds it.
type ScanConfig struct {
	// Image is Trivy's image, pinned by digest; empty: no scans. Its tag
	// ("0.76.2") names the tool in the report.
	Image string
	// Deadline bounds a scan Job's run: the database downloads (the first
	// ones are minutes long unless the warm-up did them) and the scan itself
	// (default 15 minutes). The wait behind other scans is not in it: a Job
	// waits suspended (ScanQueue) and its clock starts when it is resumed.
	Deadline time.Duration
	// TTL keeps a finished scan Job (it is where the outcome is read from)
	// this long; longer than the build is kept, so a build never meets its
	// scan gone (default: Config.Retention plus an hour).
	TTL time.Duration
}

// Enabled says scans run.
func (s ScanConfig) Enabled() bool { return s.Image != "" }

// Tool is how the report names the scanner ("trivy 0.76.2").
func (s ScanConfig) Tool() string {
	ref, _, _ := strings.Cut(s.Image, "@")
	if i := strings.LastIndexByte(ref, ':'); i >= 0 && !strings.Contains(ref[i:], "/") {
		return "trivy " + ref[i+1:]
	}
	return "trivy"
}

func scanLabels(buildID string) map[string]string {
	return map[string]string{LabelManagedBy: ManagedBy, LabelScan: buildID}
}

// ScanCache is the claim that holds the vulnerability database.
func ScanCache() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: ScanCacheClaim, Namespace: Namespace, Labels: map[string]string{LabelManagedBy: ManagedBy}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(scanCacheSize)}},
		},
	}
}

func restricted() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// trivyContainer is one of Trivy's steps: the image pinned by digest,
// confined, bounded, writing only to /tmp, /out and (the databases) /cache.
// registry gives it the registry's credentials: only the step that pulls the
// built image gets them; the database downloads go to public registries.
func trivyContainer(name string, c Config, registry bool, args ...string) corev1.Container {
	mounts := []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "cache", MountPath: "/cache"}, {Name: "out", MountPath: "/out"}}
	env := []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}}
	if registry {
		// the registry's credentials, as the builds use them
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/var/run/odyssey/registry"})
		mounts = append(mounts, corev1.VolumeMount{Name: "registry", MountPath: "/var/run/odyssey/registry", ReadOnly: true})
	}
	return corev1.Container{
		Name:         name,
		Image:        c.Scan.Image,
		Command:      append([]string{"trivy"}, args...),
		Env:          env,
		VolumeMounts: mounts,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("3Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("8Gi")},
		},
		SecurityContext: restricted(),
		// what Trivy says when it fails is the end of its log
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}
}

// downloads are the two steps that bring the databases up to date in the
// cache: the vulnerability database and the Java index. NEITHER is given
// --insecure: Trivy's flag (and TRIVY_NON_SSL) turns TLS verification off for
// every connection the process makes, the database download from
// mirror.gcr.io / ghcr.io included, and there is no flag that limits it to
// the image's registry (Trivy 0.76, pkg/commands/artifact/run.go builds one
// HTTP transport from --insecure for all of them). They are a no-op while
// the cached databases are fresh. Each has its own short limit (downloadTimeout):
// a cluster with no way out fails in minutes instead of holding the queue for
// a whole scan's deadline.
func downloads(c Config) []corev1.Container {
	t := min(downloadTimeout, c.Scan.Deadline-time.Minute).String()
	common := []string{"--cache-dir", "/cache", "--no-progress", "--skip-version-check", "--timeout", t}
	return []corev1.Container{
		trivyContainer(ContainerDB, c, false, append([]string{"image", "--download-db-only"}, common...)...),
		trivyContainer(ContainerJavaDB, c, false, append([]string{"image", "--download-java-db-only"}, common...)...),
	}
}

// scanVolumes are the volumes of a scan's pod (and the warm-up's).
func scanVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		// bounds the report (the summary container reads at most
		// builder.MaxScanReport of it): a runaway scanner is evicted, it does not
		// fill the node
		{Name: "out", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse(scanOutSize))}}},
		{Name: "cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ScanCacheClaim}}},
		{Name: "registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: RegistrySecret, Optional: ptr.To(true), DefaultMode: ptr.To[int32](0o440),
			Items: []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}}}}},
	}
}

func scanPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true),
			RunAsUser:    ptr.To[int64](builderUID),
			RunAsGroup:   ptr.To[int64](builderUID),
			FSGroup:      ptr.To[int64](builderUID),
		},
		Volumes: scanVolumes(),
	}
}

// WarmupJob downloads the databases into the cache ahead of the first scan
// (the first download is minutes long; a build's scan would otherwise carry
// it). Like a scan it is created suspended and queued (ScanQueue); it runs
// the two downloads and nothing else.
func WarmupJob(c Config) *batchv1.Job {
	c = c.withDefaults()
	dl := downloads(c)
	ttl := int32(warmupTTL.Seconds())
	spec := scanPodSpec()
	spec.InitContainers = dl[:1]
	spec.Containers = dl[1:]
	labels := map[string]string{LabelManagedBy: ManagedBy, LabelScanWarmup: "true"}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: WarmupJobName, Namespace: Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			Suspend:                 ptr.To(true),
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64(2 * c.Scan.Deadline.Seconds())),
			TTLSecondsAfterFinished: &ttl,
			Template:                corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec},
		},
	}
}

// ScanJob scans the image digest of build b (pushed to its repository).
// Nothing in the pod talks to the Kubernetes API; it runs once (no retries:
// a failed scan is reported as not done, the control plane never guesses).
//
// The pod has three steps, in order: the two database downloads (TLS
// verified, see downloads), then Trivy, which scans with the databases it
// finds (--skip-db-update, so --insecure, when the registry speaks plain
// HTTP, reaches only the registry) and writes its JSON report to a volume
// shared with the last container (the agent's own image), which counts it: a
// report is far larger than the 4 KiB a termination message holds.
//
// The Job is created SUSPENDED: the scan queue (ScanQueue) resumes one at a
// time, so the volume (ReadWriteOnce) is never contended and the deadline
// counts the run, not the wait (Kubernetes restarts the deadline's clock when
// a Job is resumed).
func ScanJob(b *v1alpha1.OdysseyBuild, c Config) *batchv1.Job {
	c = c.withDefaults()
	ref := b.Spec.ImageRepository + "@" + b.Status.ImageDigest
	deadline := c.Scan.Deadline
	ttl := int32(c.Scan.TTL.Seconds())
	trivy := []string{"image",
		"--cache-dir", "/cache",
		// the layer cache is a bolt file one process may hold: in memory
		"--cache-backend", "memory",
		"--format", "json", "--scanners", "vuln", "--output", "/out/report.json",
		"--no-progress", "--skip-version-check", "--image-src", "remote",
		"--skip-db-update", "--skip-java-db-update",
		"--timeout", (deadline - time.Minute).String()}
	if c.InsecureRegistry {
		trivy = append(trivy, "--insecure")
	}
	trivy = append(trivy, ref)
	spec := scanPodSpec()
	spec.InitContainers = append(downloads(c), trivyContainer(ContainerTrivy, c, true, trivy...))
	spec.Containers = []corev1.Container{{
		Name:    ContainerSummary,
		Image:   c.AgentImage,
		Command: []string{"odyssey-agent", "builder", "scan-summary"},
		Env: []corev1.EnvVar{
			{Name: "ODYSSEY_SCAN_REPORT", Value: "/out/report.json"},
			{Name: "ODYSSEY_SCAN_DB_METADATA", Value: "/cache/db/metadata.json"},
			{Name: "ODYSSEY_SCAN_TOOL", Value: c.Scan.Tool()},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "cache", MountPath: "/cache", ReadOnly: true},
			{Name: "out", MountPath: "/out", ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("64Mi")},
		},
		SecurityContext:          restricted(),
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: ScanJobName(b.Name), Namespace: Namespace, Labels: scanLabels(b.Name),
			Annotations: map[string]string{AnnotationDigest: b.Status.ImageDigest}},
		Spec: batchv1.JobSpec{
			Suspend:                 ptr.To(true),
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64(deadline.Seconds())),
			TTLSecondsAfterFinished: &ttl,
			Template:                corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: scanLabels(b.Name)}, Spec: spec},
		},
	}
}

// ScanOutcome is what a scan Job showed: the counts, or why it could not be
// done (Error). Done is false while the Job still runs.
type ScanOutcome struct {
	Done    bool
	Digest  string
	Summary builder.ScanSummary
	Error   string
}

// maxScanError bounds the reason a scan reports.
const maxScanError = 500

// ObserveScan is the outcome of a scan Job (pod: its newest pod, nil when it
// has none). It is a pure function of what the cluster shows.
func ObserveScan(job *batchv1.Job, pod *corev1.Pod) ScanOutcome {
	if job == nil {
		return ScanOutcome{}
	}
	out := ScanOutcome{Digest: job.Annotations[AnnotationDigest]}
	failedWith := func(msg string) ScanOutcome {
		out.Done, out.Error = true, builder.Tail(msg, maxScanError)
		return out
	}
	if c := jobCondition(job, batchv1.JobFailed); c != nil {
		if c.Reason == batchv1.JobReasonDeadlineExceeded {
			msg := "the scan ran past its time limit"
			if why := stuck(pod); why != "" {
				msg += " (" + why + ")"
			}
			return failedWith(msg)
		}
		if pod != nil {
			for _, name := range []string{ContainerDB, ContainerJavaDB, ContainerTrivy} {
				if s := status(pod.Status.InitContainerStatuses, name); s != nil && s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
					return failedWith("the scanner failed (" + name + "): " + s.State.Terminated.Message)
				}
			}
			if s := status(pod.Status.ContainerStatuses, ContainerSummary); s != nil && s.State.Terminated != nil {
				if r, ok := builder.ParseResult(s.State.Terminated.Message); ok && r.Message != "" {
					return failedWith(r.Message)
				}
			}
			if pod.Status.Reason == "Evicted" {
				return failedWith("the cluster evicted the scan: " + pod.Status.Message)
			}
			if why := stuck(pod); why != "" {
				return failedWith(why)
			}
		}
		return failedWith(strings.TrimSpace(c.Reason + ": " + c.Message))
	}
	if jobCondition(job, batchv1.JobComplete) == nil {
		return out
	}
	if pod != nil {
		if s := status(pod.Status.ContainerStatuses, ContainerSummary); s != nil && s.State.Terminated != nil && s.State.Terminated.ExitCode == 0 {
			if sum, ok := builder.ParseScanSummary(s.State.Terminated.Message); ok {
				out.Done, out.Summary = true, sum
				return out
			}
			return failedWith("the scan finished but its result was unreadable")
		}
	}
	// The Job is complete but its pod is not in view (yet): the next event
	// or full report finds it; until then the scan has not ended.
	return out
}

// AnnotationScanError is set on a build whose scan could not even be
// started (the cluster refused its Job for longer than scanStartWindow): the
// build is where the outcome is then read from (Manager.Scan), so the control
// plane learns "never" at once instead of waiting out its own timeout.
const AnnotationScanError = "odyssey.dev/scan-error"

// ScanNotStarted is the outcome recorded on a build whose scan was given up.
func ScanNotStarted(b *v1alpha1.OdysseyBuild) (ScanOutcome, bool) {
	msg := b.Annotations[AnnotationScanError]
	if msg == "" {
		return ScanOutcome{}, false
	}
	return ScanOutcome{Done: true, Digest: b.Status.ImageDigest, Error: msg}, true
}

// scanFailedToStart handles a scan that could not be queued. It never fails
// the build's cleanup: a scan is not the build. It says how soon to try
// again (0: it gave up, and recorded why on the build for the control plane).
func (r *Reconciler) scanFailedToStart(ctx context.Context, b *v1alpha1.OdysseyBuild, cause error) time.Duration {
	at := b.CreationTimestamp.Time
	if b.Status.FinishedAt != nil {
		at = b.Status.FinishedAt.Time
	}
	if r.now().Sub(at) < scanStartWindow {
		slog.Warn("scan: could not queue it, trying again", "build", b.Name, "err", cause)
		return scanStartRetry
	}
	slog.Warn("scan: could not queue it, giving up", "build", b.Name, "err", cause)
	patch := client.MergeFrom(b.DeepCopy())
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[AnnotationScanError] = builder.Tail("the scan could not be started: "+cause.Error(), maxScanError)
	if err := r.Client.Patch(ctx, b, patch); err != nil {
		slog.Warn("scan: could not record why it was given up", "build", b.Name, "err", err)
	}
	return 0
}

// ensureScan queues the scan of a succeeded build (once: its Job is kept
// longer than the build is, so an existing one is never redone) and creates
// the volume the vulnerability database lives on (if missing). The Job is
// created suspended; ScanQueue runs the queued Jobs one at a time.
func (r *Reconciler) ensureScan(ctx context.Context, b *v1alpha1.OdysseyBuild) error {
	if !r.Config.Scan.Enabled() || b.Status.Phase != v1alpha1.BuildSucceeded || b.Status.ImageDigest == "" {
		return nil
	}
	if _, gaveUp := ScanNotStarted(b); gaveUp {
		return nil
	}
	if b.Spec.SkipScan && b.Status.Reused {
		return nil // the control plane copies the earlier image's scan
	}
	var job batchv1.Job
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: ScanJobName(b.Name)}, &job)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := ensureCache(ctx, r.Client, r.API); err != nil {
		return err
	}
	if err := r.Client.Create(ctx, ScanJob(b, r.Config)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("start the scan: %w", err)
	}
	return nil
}
