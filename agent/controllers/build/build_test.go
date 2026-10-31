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
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const id = "0b5c4a5e-3d4f-4a7b-9c1d-2e3f4a5b6c7d"

var (
	cfg    = Config{AgentImage: "odyssey-agent:dev", BuildKitImage: "moby/buildkit:rootless", Frontend: "ghcr.io/railwayapp/railpack-frontend:v1", SourceURL: "http://src:8099", Platform: "linux/amd64", InsecureRegistry: true, TTL: time.Hour}
	digest = "sha256:" + strings.Repeat("a", 64)
)

func newBuild() *v1alpha1.OdysseyBuild {
	return &v1alpha1.OdysseyBuild{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: Namespace, UID: "u1"},
		Spec: v1alpha1.OdysseyBuildSpec{
			Organization: "11111111-1111-1111-1111-111111111111", Project: "22222222-2222-2222-2222-222222222222",
			Environment:     "33333333-3333-3333-3333-333333333333",
			Source:          v1alpha1.BuildSource{SHA256: strings.Repeat("b", 64), Size: 1234},
			ImageRepository: "registry.test/o/p", CacheRef: "registry.test/o/p:buildcache", TimeoutSeconds: 1200,
		},
	}
}

func env(c corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestJob(t *testing.T) {
	j := Job(newBuild(), cfg)
	s := j.Spec
	if j.Name != "build-"+id || j.Namespace != Namespace || j.Labels[LabelBuild] != id || s.Template.Labels[LabelBuild] != id {
		t.Fatalf("name/labels: %s %v", j.Name, j.Labels)
	}
	if *s.BackoffLimit != 0 || *s.ActiveDeadlineSeconds != 1200 || *s.TTLSecondsAfterFinished != 3600 {
		t.Fatalf("job limits: %+v", s)
	}
	p := s.Template.Spec
	if p.RestartPolicy != corev1.RestartPolicyNever || *p.AutomountServiceAccountToken || *p.EnableServiceLinks ||
		!*p.SecurityContext.RunAsNonRoot || *p.SecurityContext.RunAsUser != 1000 {
		t.Fatalf("pod: %+v", p)
	}
	if len(p.InitContainers) != 1 || len(p.Containers) != 1 {
		t.Fatalf("containers: %d+%d", len(p.InitContainers), len(p.Containers))
	}
	prep, build := p.InitContainers[0], p.Containers[0]
	if prep.Name != ContainerPrepare || prep.Image != cfg.AgentImage || strings.Join(prep.Command, " ") != "odyssey-agent builder prepare" {
		t.Fatalf("prepare: %+v", prep)
	}
	if sc := prep.SecurityContext; *sc.AllowPrivilegeEscalation || !*sc.ReadOnlyRootFilesystem || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("prepare must be restricted: %+v", sc)
	}
	pe := env(prep)
	if pe["ODYSSEY_SOURCE_URL"] != "http://src:8099/builds/"+id+"/source" || pe["ODYSSEY_SOURCE_SHA256"] != strings.Repeat("b", 64) || pe["ODYSSEY_SOURCE_SIZE"] != "1234" {
		t.Fatalf("prepare env: %v", pe)
	}
	if build.Name != ContainerBuild || build.Image != cfg.BuildKitImage || build.Command[0] != "/work/bin/odyssey-agent" {
		t.Fatalf("build: %+v", build)
	}
	be := env(build)
	if be["ODYSSEY_IMAGE"] != "registry.test/o/p:"+id || be["ODYSSEY_CACHE_REF"] != "registry.test/o/p:buildcache" ||
		be["ODYSSEY_CACHE_KEY"] != newBuild().Spec.Project || be["ODYSSEY_FRONTEND"] != cfg.Frontend || be["ODYSSEY_REGISTRY_INSECURE"] != "true" ||
		be["BUILDKITD_FLAGS"] != "--oci-worker-no-process-sandbox" {
		t.Fatalf("build env: %v", be)
	}
	if l := build.Resources.Limits; l.Cpu().String() != "2" || l.Memory().String() != "4Gi" || l.StorageEphemeral().String() != "10Gi" {
		t.Fatalf("build limits: %v", l)
	}
	if sc := build.SecurityContext; sc.Privileged != nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" ||
		strings.Join(func() []string {
			var s []string
			for _, c := range sc.Capabilities.Add {
				s = append(s, string(c))
			}
			return s
		}(), ",") != "SETUID,SETGID" {
		t.Fatalf("build must not be privileged (only newuidmap's SETUID/SETGID): %+v", build.SecurityContext)
	}
	// the token is mounted only in prepare
	for _, m := range build.VolumeMounts {
		if m.Name == "token" {
			t.Fatal("the build container must not see the source token")
		}
	}
	var token, reg bool
	for _, v := range p.Volumes {
		if v.Name == "token" && v.Secret != nil && v.Secret.SecretName == "build-"+id {
			token = true
		}
		if v.Name == "registry" && v.Secret != nil && v.Secret.SecretName == RegistrySecret && *v.Secret.Optional {
			reg = true
		}
	}
	if !token || !reg {
		t.Fatalf("volumes: %+v", p.Volumes)
	}
}

func terminated(name string, code int32, reason string, r *builder.Result) corev1.ContainerStatus {
	msg := ""
	if r != nil {
		msg = string(r.Encode())
	}
	return corev1.ContainerStatus{Name: name, Image: "img", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason, Message: msg}}}
}

func running(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func waiting(name, reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Image: "img", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "boom"}}}
}

func podWith(phase corev1.PodPhase, init []corev1.ContainerStatus, main []corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase, InitContainerStatuses: init, ContainerStatuses: main}}
}

func jobWith(conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{Status: batchv1.JobStatus{Conditions: conds}}
}

func TestObserve(t *testing.T) {
	failedCond := func(reason string) batchv1.JobCondition {
		return batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: "m"}
	}
	cases := []struct {
		name   string
		job    *batchv1.Job
		pod    *corev1.Pod
		phase  v1alpha1.BuildPhase
		reason v1alpha1.BuildReason
		digest string
		msg    string
	}{
		{name: "no job", phase: v1alpha1.BuildPending},
		{name: "no pod", job: jobWith(), phase: v1alpha1.BuildPending},
		{name: "scheduled", job: jobWith(), pod: podWith(corev1.PodPending, nil, nil), phase: v1alpha1.BuildPending},
		{name: "preparing", job: jobWith(), pod: podWith(corev1.PodPending, []corev1.ContainerStatus{running(ContainerPrepare)}, nil), phase: v1alpha1.BuildRunning},
		{name: "building", job: jobWith(), pod: podWith(corev1.PodRunning, []corev1.ContainerStatus{terminated(ContainerPrepare, 0, "Completed", &builder.Result{})},
			[]corev1.ContainerStatus{running(ContainerBuild)}), phase: v1alpha1.BuildRunning},
		{name: "pushed", job: jobWith(), pod: podWith(corev1.PodSucceeded, []corev1.ContainerStatus{terminated(ContainerPrepare, 0, "Completed", nil)},
			[]corev1.ContainerStatus{terminated(ContainerBuild, 0, "Completed", &builder.Result{Digest: digest})}), phase: v1alpha1.BuildSucceeded, digest: digest},
		{name: "exit 0 without a digest", job: jobWith(), pod: podWith(corev1.PodSucceeded, nil,
			[]corev1.ContainerStatus{terminated(ContainerBuild, 0, "Completed", &builder.Result{Digest: "sha256:nope"})}), phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed},
		{name: "unsupported app", job: jobWith(failedCond("BackoffLimitExceeded")), pod: podWith(corev1.PodFailed,
			[]corev1.ContainerStatus{terminated(ContainerPrepare, 1, "Error", &builder.Result{Reason: builder.ReasonUnsupported, Message: "No start command"})}, nil),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonUnsupported, msg: "No start command"},
		{name: "bad source", job: jobWith(), pod: podWith(corev1.PodFailed,
			[]corev1.ContainerStatus{terminated(ContainerPrepare, 1, "Error", &builder.Result{Reason: builder.ReasonSource, Message: "digest differs"})}, nil),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonSource, msg: "digest differs"},
		{name: "prepare crashed", job: jobWith(), pod: podWith(corev1.PodFailed,
			[]corev1.ContainerStatus{terminated(ContainerPrepare, 2, "Error", nil)}, nil), phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonSource},
		{name: "push failed", job: jobWith(), pod: podWith(corev1.PodFailed, nil,
			[]corev1.ContainerStatus{terminated(ContainerBuild, 1, "Error", &builder.Result{Reason: builder.ReasonPush, Message: "denied"})}),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonPush, msg: "denied"},
		{name: "unknown reason", job: jobWith(), pod: podWith(corev1.PodFailed, nil,
			[]corev1.ContainerStatus{terminated(ContainerBuild, 1, "Error", &builder.Result{Reason: "weird", Message: "x"})}),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed},
		{name: "oom", job: jobWith(), pod: podWith(corev1.PodFailed, nil,
			[]corev1.ContainerStatus{terminated(ContainerBuild, 137, "OOMKilled", nil)}), phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed, msg: "out of memory"},
		{name: "deadline", job: jobWith(failedCond(batchv1.JobReasonDeadlineExceeded)), pod: podWith(corev1.PodRunning, nil, []corev1.ContainerStatus{running(ContainerBuild)}),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonTimeout},
		{name: "evicted", job: jobWith(), pod: &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Message: "low disk"}},
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed, msg: "low disk"},
		{name: "image pull (retried)", job: jobWith(), pod: podWith(corev1.PodPending, []corev1.ContainerStatus{waiting(ContainerPrepare, "ImagePullBackOff")}, nil),
			phase: v1alpha1.BuildPending},
		{name: "pull error (retried)", job: jobWith(), pod: podWith(corev1.PodPending, nil, []corev1.ContainerStatus{waiting(ContainerBuild, "ErrImagePull")}),
			phase: v1alpha1.BuildPending},
		{name: "invalid image", job: jobWith(), pod: podWith(corev1.PodPending, []corev1.ContainerStatus{waiting(ContainerPrepare, "InvalidImageName")}, nil),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed, msg: "boom"},
		{name: "deadline while pulling", job: jobWith(failedCond(batchv1.JobReasonDeadlineExceeded)),
			pod:   podWith(corev1.PodPending, []corev1.ContainerStatus{waiting(ContainerPrepare, "ImagePullBackOff")}, nil),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonTimeout, msg: "boom"},
		{name: "starting", job: jobWith(), pod: podWith(corev1.PodPending, []corev1.ContainerStatus{waiting(ContainerPrepare, "PodInitializing")}, nil),
			phase: v1alpha1.BuildPending},
		{name: "job failed, pod gone", job: jobWith(failedCond("BackoffLimitExceeded")), phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed},
		{name: "job complete, pod gone", job: jobWith(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}),
			phase: v1alpha1.BuildFailed, reason: v1alpha1.BuildReasonFailed, msg: "lost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Observe(c.job, c.pod)
			if got.Phase != c.phase || got.Reason != c.reason || got.ImageDigest != c.digest || !strings.Contains(got.Message, c.msg) {
				t.Fatalf("got %+v", got)
			}
			if got.Phase == v1alpha1.BuildFailed && got.Message == "" {
				t.Fatal("a failure explains itself")
			}
		})
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	b := newBuild()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
	r := &Reconciler{Client: c, API: c, Config: cfg}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "build-" + id}, &job); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "build-" + id}, &sec); err != nil {
		t.Fatal(err)
	}
	token := string(sec.Data[TokenKey])
	if len(token) != 64 || !owned(job.OwnerReferences, b) || !owned(sec.OwnerReferences, b) {
		t.Fatalf("token %q, owners %v %v", token, job.OwnerReferences, sec.OwnerReferences)
	}
	var got v1alpha1.OdysseyBuild
	_ = c.Get(ctx, req.NamespacedName, &got)
	if got.Status.Phase != v1alpha1.BuildPending {
		t.Fatalf("status %+v", got.Status)
	}

	// Again: the same Job and token (nothing is recreated).
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(&sec), &sec)
	if string(sec.Data[TokenKey]) != token {
		t.Fatal("the token changed")
	}

	// The pod pushes: succeeded, and that never changes again.
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "build-" + id + "-x", Namespace: Namespace, Labels: map[string]string{LabelBuild: id}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{terminated(ContainerBuild, 0, "Completed", &builder.Result{Digest: digest})}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, req.NamespacedName, &got)
	if got.Status.Phase != v1alpha1.BuildSucceeded || got.Status.ImageDigest != digest {
		t.Fatalf("status %+v", got.Status)
	}
	p.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, req.NamespacedName, &got)
	if got.Status.Phase != v1alpha1.BuildSucceeded {
		t.Fatalf("a final status changed: %+v", got.Status)
	}
	if got.Status.FinishedAt == nil {
		t.Fatal("a final status records when it ended")
	}
	// Once over, its token goes (nothing may fetch its source) ...
	if err := c.Get(ctx, client.ObjectKeyFromObject(&sec), &sec); !apierrors.IsNotFound(err) {
		t.Fatalf("the token outlived the build: %v", err)
	}
	// ... and the build itself after Retention (the tunnel reported it).
	res, err := r.Reconcile(ctx, req)
	if err != nil || res.RequeueAfter <= 0 {
		t.Fatalf("kept until its retention: %+v %v", res, err)
	}
	r.Now = func() time.Time { return time.Now().Add(cfg.withDefaults().Retention + time.Minute) }
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("a build past its retention is deleted: %v", err)
	}

	// A deleted (cancelled) build is nothing to do.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: Namespace, Name: "gone"}}); err != nil {
		t.Fatal(err)
	}
}

// A running build whose Job disappears (deleted, or past its TTL while the
// agent was down) failed: it is not run (and pushed) a second time.
func TestReconcileLostJob(t *testing.T) {
	ctx := context.Background()
	b := newBuild()
	b.Status = v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildRunning}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
	r := &Reconciler{Client: c, API: c, Config: cfg}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.OdysseyBuild
	_ = c.Get(ctx, req.NamespacedName, &got)
	if got.Status.Phase != v1alpha1.BuildFailed || !strings.Contains(got.Status.Message, "disappeared") {
		t.Fatalf("status %+v", got.Status)
	}
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "build-" + id}, &job); !apierrors.IsNotFound(err) {
		t.Fatalf("the Job was created again: %v", err)
	}
}

// A running build does not go back to pending when its pod leaves the
// cache for a moment.
func TestReconcileNeverGoesBack(t *testing.T) {
	ctx := context.Background()
	b := newBuild()
	b.Status = v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildRunning}
	job := Job(b, cfg)
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(b, job).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
	r := &Reconciler{Client: c, API: c, Config: cfg}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.OdysseyBuild
	_ = c.Get(ctx, req.NamespacedName, &got)
	if got.Status.Phase != v1alpha1.BuildRunning {
		t.Fatalf("status %+v", got.Status)
	}
}

func owned(refs []metav1.OwnerReference, b *v1alpha1.OdysseyBuild) bool {
	for _, r := range refs {
		if r.UID == b.UID && r.Controller != nil && *r.Controller {
			return true
		}
	}
	return false
}
