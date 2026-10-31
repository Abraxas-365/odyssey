package builds

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	id  = "0b5c4a5e-3d4f-4a7b-9c1d-2e3f4a5b6c7d"
	org = "11111111-1111-1111-1111-111111111111"
	prj = "22222222-2222-2222-2222-222222222222"
	env = "33333333-3333-3333-3333-333333333333"
)

func build() *fleetv1.Build {
	return &fleetv1.Build{BuildId: id, OrganizationId: org, ProjectId: prj, EnvironmentId: env,
		Source:          &fleetv1.SourceRef{Sha256: strings.Repeat("a", 64), Size: 100},
		ImageRepository: "registry.test/odyssey/" + org + "/" + prj, CacheRef: "registry.test/odyssey/" + org + "/" + prj + ":buildcache",
		TimeoutSeconds: 1200}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&v1alpha1.OdysseyBuild{}).Build()
}

func TestCheck(t *testing.T) {
	m := &Manager{Registry: "registry.test/odyssey"}
	if err := m.Check(build()); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*fleetv1.Build){
		"id":             func(b *fleetv1.Build) { b.BuildId = "../x" },
		"project":        func(b *fleetv1.Build) { b.ProjectId = "P" },
		"sha":            func(b *fleetv1.Build) { b.Source.Sha256 = "zz" },
		"size":           func(b *fleetv1.Build) { b.Source.Size = 0 },
		"other registry": func(b *fleetv1.Build) { b.ImageRepository = "evil.test/odyssey/" + org + "/" + prj; b.CacheRef = "" },
		"prefix trick": func(b *fleetv1.Build) {
			b.ImageRepository = "registry.test/odyssey-evil/" + org + "/" + prj
			b.CacheRef = ""
		},
		"another project": func(b *fleetv1.Build) {
			b.ImageRepository = "registry.test/odyssey/" + org + "/" + env
			b.CacheRef = ""
		},
		"double slash": func(b *fleetv1.Build) {
			b.ImageRepository = "registry.test/odyssey//" + org + "/" + prj
			b.CacheRef = ""
		},
		"tag in repo": func(b *fleetv1.Build) { b.ImageRepository += ":x"; b.CacheRef = "" },
		"cache":       func(b *fleetv1.Build) { b.CacheRef = "registry.test/odyssey/" + org + "/" + prj + ":other" },
		"timeout":     func(b *fleetv1.Build) { b.TimeoutSeconds = 5 },
	}
	for name, mutate := range bad {
		b := build()
		mutate(b)
		if err := m.Check(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&Manager{}).Check(build()); err == nil {
		t.Error("an agent without a registry builds nothing")
	}
}

func TestStartCancelStatuses(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	m := &Manager{Client: c, Registry: "registry.test/odyssey"}
	for range 2 { // again: the same build
		if st, err := m.Start(ctx, build()); err != nil || st != nil {
			t.Fatalf("%v %v", st, err)
		}
	}
	var ob v1alpha1.OdysseyBuild
	if err := c.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &ob); err != nil {
		t.Fatal(err)
	}
	if ob.Spec.Project != prj || ob.Spec.Source.Size != 100 || ob.Spec.TimeoutSeconds != 1200 || ob.Labels[buildctl.LabelManagedBy] != buildctl.ManagedBy {
		t.Fatalf("%+v", ob)
	}
	st, err := m.Status(ctx, id)
	if err != nil || st.GetPhase() != "pending" || st.GetBuildId() != id {
		t.Fatalf("%v %v", st, err)
	}
	ob.Status = v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildFailed, Reason: v1alpha1.BuildReasonTimeout, Message: "too slow"}
	if err := c.Status().Update(ctx, &ob); err != nil {
		t.Fatal(err)
	}
	all, err := m.Statuses(ctx)
	if err != nil || len(all) != 1 || all[0].GetReason() != "timeout" || all[0].GetMessage() != "too slow" {
		t.Fatalf("%v %v", all, err)
	}

	bad := build()
	bad.BuildId = "44444444-4444-4444-4444-444444444444"
	bad.ImageRepository = "evil.test/x"
	if st, err := m.Start(ctx, bad); err != nil || st.GetPhase() != "failed" || st.GetReason() != "failed" || !strings.Contains(st.GetMessage(), "registry") {
		t.Fatalf("refused: %v %v", st, err)
	}

	for range 2 { // again: already gone
		if err := m.Cancel(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := m.Status(ctx, id); err != nil || st != nil {
		t.Fatalf("after cancel: %v %v", st, err)
	}
}

// logs serves container logs: each open of a container takes its next
// answer (the last one repeats).
type logs struct {
	mu      sync.Mutex
	answers map[string][]func() (io.ReadCloser, error)
	opened  map[string]int
}

func (l *logs) open(_ context.Context, ns, pod, container string) (io.ReadCloser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ns != buildctl.Namespace || pod != "build-"+id+"-abc" {
		return nil, errors.New("wrong pod")
	}
	a := l.answers[container]
	i := min(l.opened[container], len(a)-1)
	l.opened[container]++
	return a[i]()
}

func text(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// broken reads s, then fails (a stream cut mid-line).
func broken(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader(s), errReader{})), nil
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func pod(init, main corev1.ContainerState) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "build-" + id + "-abc", Namespace: buildctl.Namespace, Labels: map[string]string{buildctl.LabelBuild: id}},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: buildctl.ContainerPrepare, State: init}}}}
	if main != (corev1.ContainerState{}) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: buildctl.ContainerBuild, State: main}}
	}
	return p
}

var done = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}

func ob(phase v1alpha1.BuildPhase) *v1alpha1.OdysseyBuild {
	return &v1alpha1.OdysseyBuild{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: buildctl.Namespace}, Status: v1alpha1.OdysseyBuildStatus{Phase: phase}}
}

func collect(t *testing.T, m *Manager) []*fleetv1.LogLine {
	t.Helper()
	var mu sync.Mutex
	var got []*fleetv1.LogLine
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := m.FollowLogs(ctx, id, func(lines []*fleetv1.LogLine) error {
		mu.Lock()
		defer mu.Unlock()
		if len(lines) > batchLines {
			t.Errorf("a batch of %d lines", len(lines))
		}
		got = append(got, lines...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func render(lines []*fleetv1.LogLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.GetStream() + ":" + strconv.FormatInt(l.GetSeq()>>32, 10) + "." + strconv.FormatInt(l.GetSeq()&(1<<32-1), 10) + ":" + l.GetText() + "\n")
	}
	return b.String()
}

func TestFollowLogsOfAFinishedBuild(t *testing.T) {
	l := &logs{opened: map[string]int{}, answers: map[string][]func() (io.ReadCloser, error){
		buildctl.ContainerPrepare: {text("Fetching\nVerified\n")},
		// the first read breaks mid-line: the partial line is not sent, and
		// the next read skips what was sent
		buildctl.ContainerBuild: {broken("#1 a\n#2 b\n#3 hal"), text("#1 a\n#2 b\n#3 half\nPushed")},
	}}
	m := &Manager{Client: newClient(t, ob(v1alpha1.BuildSucceeded), pod(done, done)), Logs: l.open}
	got := render(collect(t, m))
	want := "prepare:1.1:Fetching\nprepare:1.2:Verified\nbuild:2.1:#1 a\nbuild:2.2:#2 b\nbuild:2.3:#3 half\nbuild:2.4:Pushed\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// A container still running whose stream ends cleanly mid-line (it is
// writing it): the half line is not sent; the next read sends it whole
// under the same number.
func TestFollowLogsHoldsAPartialLineWhileRunning(t *testing.T) {
	c := newClient(t, ob(v1alpha1.BuildRunning), pod(done, corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}))
	opened := 0
	var m *Manager
	m = &Manager{Client: c, Logs: func(ctx context.Context, ns, p, container string) (io.ReadCloser, error) {
		if container == buildctl.ContainerPrepare {
			return io.NopCloser(strings.NewReader("ok\n")), nil
		}
		if opened++; opened == 1 {
			return io.NopCloser(strings.NewReader("#1 a\n#2 hal")), nil
		}
		// it ended meanwhile
		var b v1alpha1.OdysseyBuild
		_ = c.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &b)
		b.Status.Phase = v1alpha1.BuildSucceeded
		_ = c.Status().Update(ctx, &b)
		po := pod(done, done)
		var cur corev1.Pod
		_ = c.Get(ctx, client.ObjectKeyFromObject(po), &cur)
		cur.Status = po.Status
		_ = c.Status().Update(ctx, &cur)
		return io.NopCloser(strings.NewReader("#1 a\n#2 half\n#3 done")), nil
	}}
	got := render(collect(t, m))
	want := "prepare:1.1:ok\nbuild:2.1:#1 a\nbuild:2.2:#2 half\nbuild:2.3:#3 done\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// A build that is over whose container still shows running (its node is
// gone) is not followed forever.
func TestFollowLogsGivesUpOnAStaleContainer(t *testing.T) {
	m := &Manager{Client: newClient(t, ob(v1alpha1.BuildFailed), pod(done, corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})),
		Logs: func(_ context.Context, ns, p, container string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("line\n")), nil
		}}
	if got := render(collect(t, m)); got != "prepare:1.1:line\nbuild:2.1:line\n" {
		t.Fatal(got)
	}
}

func TestFollowLogsWhenPrepareFailed(t *testing.T) {
	l := &logs{opened: map[string]int{}, answers: map[string][]func() (io.ReadCloser, error){
		buildctl.ContainerPrepare: {text("Fetching\nERROR: no start command\n")},
		buildctl.ContainerBuild:   {func() (io.ReadCloser, error) { t.Error("build never ran"); return nil, errors.New("x") }},
	}}
	m := &Manager{Client: newClient(t, ob(v1alpha1.BuildFailed), pod(done, corev1.ContainerState{})), Logs: l.open}
	if got := render(collect(t, m)); got != "prepare:1.1:Fetching\nprepare:1.2:ERROR: no start command\n" {
		t.Fatal(got)
	}
}

func TestFollowLogsOfAGoneBuild(t *testing.T) {
	m := &Manager{Client: newClient(t), Logs: func(context.Context, string, string, string) (io.ReadCloser, error) {
		t.Error("nothing to read")
		return nil, errors.New("x")
	}}
	if got := collect(t, m); len(got) != 0 {
		t.Fatal(got)
	}
	// a pod whose log was removed with it
	gone := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "p")
	m = &Manager{Client: newClient(t, ob(v1alpha1.BuildFailed), pod(done, done)), Logs: func(context.Context, string, string, string) (io.ReadCloser, error) {
		return nil, gone
	}}
	if got := collect(t, m); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestFollowLogsBatchesAndCutsLines(t *testing.T) {
	long := strings.Repeat("x", maxLine+100)
	var b strings.Builder
	for range 1200 {
		b.WriteString("line\n")
	}
	b.WriteString(long + "\n")
	l := &logs{opened: map[string]int{}, answers: map[string][]func() (io.ReadCloser, error){
		buildctl.ContainerPrepare: {text(b.String())},
		buildctl.ContainerBuild:   {text("")},
	}}
	m := &Manager{Client: newClient(t, ob(v1alpha1.BuildSucceeded), pod(done, done)), Logs: l.open}
	got := collect(t, m)
	if len(got) != 1201 || len(got[1200].GetText()) != maxLine || got[1200].GetSeq() != 1<<32|1201 {
		t.Fatalf("%d lines, last %d bytes", len(got), len(got[len(got)-1].GetText()))
	}
}

func TestFollowLogsStopsWhenSendFails(t *testing.T) {
	l := &logs{opened: map[string]int{}, answers: map[string][]func() (io.ReadCloser, error){
		buildctl.ContainerPrepare: {text("a\nb\n")},
		buildctl.ContainerBuild:   {text("c\n")},
	}}
	m := &Manager{Client: newClient(t, ob(v1alpha1.BuildSucceeded), pod(done, done)), Logs: l.open}
	boom := errors.New("tunnel down")
	err := m.FollowLogs(context.Background(), id, func([]*fleetv1.LogLine) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}
