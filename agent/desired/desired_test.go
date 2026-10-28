package desired

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	org, proj, env = uuid.NewString(), uuid.NewString(), uuid.NewString()
)

func app(rev int64) *fleetv1.App {
	return &fleetv1.App{
		OrganizationId: org, ProjectId: proj, EnvironmentId: env, Name: "portal", Revision: rev,
		Image: "r.test/portal@sha256:" + strings.Repeat("a", 64), Port: 8080, HealthPath: "/healthz", Replicas: 2,
		Env:    map[string]string{"A": "1"},
		Labels: map[string]string{"odyssey.dev/deployment": "d" + string(rune('0'+rev)), LabelOrganization: "spoofed"},
	}
}

func newSyncer(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) (*Syncer, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&v1alpha1.OdysseyApp{}).WithInterceptorFuncs(funcs).Build()
	return &Syncer{Client: c}, c
}

func get(t *testing.T, c client.Client) v1alpha1.OdysseyApp {
	t.Helper()
	var a v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace(env), Name: "portal"}, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestApplyWritesTheAppInTheEnvironmentNamespace(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(ctx, app(2)); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: "odyssey-" + env}, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels[LabelOrganization] != org || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("namespace labels: %v", ns.Labels)
	}
	a := get(t, c)
	if a.Spec.Revision != 2 || a.Spec.Port != 8080 || a.Spec.Replicas != 2 || a.Spec.HealthPath != "/healthz" || a.Spec.Env["A"] != "1" || a.Labels["odyssey.dev/deployment"] != "d2" {
		t.Fatalf("spec: %+v labels %v", a.Spec, a.Labels)
	}
	if a.Labels[LabelOrganization] != org || a.Labels[LabelEnvironment] != env {
		t.Errorf("the agent's own labels must win over the sent ones: %v", a.Labels)
	}

	// Idempotent, and an older revision never wins late.
	if err := s.Apply(ctx, app(2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, app(1)); err != nil {
		t.Fatal(err)
	}
	if a := get(t, c); a.Spec.Revision != 2 {
		t.Errorf("an older revision replaced a newer one: %d", a.Spec.Revision)
	}
	if err := s.Apply(ctx, app(3)); err != nil {
		t.Fatal(err)
	}
	if a := get(t, c); a.Spec.Revision != 3 || a.Labels["odyssey.dev/deployment"] != "d3" {
		t.Errorf("newer revision: %+v", a.Spec)
	}
}

func TestApplyRefusesWhatItCannotName(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	for name, mutate := range map[string]func(*fleetv1.App){
		"bad name":        func(a *fleetv1.App) { a.Name = "Not_A_Label" },
		"no environment":  func(a *fleetv1.App) { a.EnvironmentId = "" },
		"env not a uuid":  func(a *fleetv1.App) { a.EnvironmentId = "../kube-system" },
		"zero revision":   func(a *fleetv1.App) { a.Revision = 0 },
		"uppercase uuid":  func(a *fleetv1.App) { a.EnvironmentId = strings.ToUpper(env) },
		"no organization": func(a *fleetv1.App) { a.OrganizationId = "" },
	} {
		a := app(1)
		mutate(a)
		if err := s.Apply(context.Background(), a); !IsRejected(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestApplyErrors(t *testing.T) {
	gr := schema.GroupResource{Group: "odyssey.dev", Resource: "odysseyapps"}
	for _, c := range []struct {
		name     string
		err      error
		rejected bool // reported failed instead of retried
	}{
		{"rejected spec", apierrors.NewInvalid(schema.GroupKind{Group: "odyssey.dev", Kind: "OdysseyApp"}, "portal", nil), true},
		{"concurrent writer", apierrors.NewConflict(gr, "portal", errors.New("modified")), false},
		{"CRD not installed", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "odyssey.dev", Kind: "OdysseyApp"}}, true},
		{"not allowed", apierrors.NewForbidden(gr, "portal", errors.New("rbac")), true},
		{"api down", errors.New("connection refused"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newSyncer(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.OdysseyApp); ok {
					return c.err
				}
				return cl.Create(ctx, obj, opts...)
			}})
			err := s.Apply(context.Background(), app(1))
			if err == nil || IsRejected(err) != c.rejected {
				t.Errorf("err %v, rejected %v", err, IsRejected(err))
			}
			if IsRejected(err) {
				st := RejectedStatus(app(1), err)
				if st.GetPhase() != "failed" || st.GetReason() != "config" || st.GetObservedRevision() != 1 || st.GetMessage() == "" {
					t.Errorf("rejected status: %+v", st)
				}
			}
		})
	}
}

func TestStatuses(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if st, err := s.Status(ctx, env, "portal"); err != nil || st != nil {
		t.Fatalf("unknown app: %+v %v", st, err)
	}
	if err := s.Apply(ctx, app(4)); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx, env, "portal"); st != nil {
		t.Fatalf("not yet observed: %+v", st)
	}
	a := get(t, c)
	a.Status = v1alpha1.OdysseyAppStatus{ObservedRevision: 4, Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonImagePull,
		Message: "no such image\x00\x1b[31m", URL: "http://portal.apps.test"}
	if err := c.Status().Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx, env, "portal")
	if err != nil || st.GetObservedRevision() != 4 || st.GetPhase() != "failed" || st.GetReason() != "image_pull" ||
		st.GetMessage() != "no such image[31m" || st.GetUrl() != "http://portal.apps.test" || st.GetEnvironmentId() != env {
		t.Fatalf("status: %+v %v", st, err)
	}

	// An OdysseyApp someone else wrote (no environment label) is not reported.
	foreign := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec: v1alpha1.OdysseyAppSpec{Revision: 1, Image: a.Spec.Image, Port: 80, HealthPath: "/", Replicas: 1}}
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	foreign.Status = v1alpha1.OdysseyAppStatus{ObservedRevision: 1, Phase: v1alpha1.PhaseReady}
	if err := c.Status().Update(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	all, err := s.Statuses(ctx)
	if err != nil || len(all) != 1 || all[0].GetName() != "portal" {
		t.Fatalf("statuses: %+v %v", all, err)
	}
}

func TestClipBoundsMessages(t *testing.T) {
	if got := clip(strings.Repeat("é", 3000), maxMessage); len([]rune(got)) != maxMessage {
		t.Errorf("clip: %d runes", len([]rune(got)))
	}
	if got := clip("a\tb\nc\rd\x7f", 10); got != "a\tb\ncd" {
		t.Errorf("clip control: %q", got)
	}
}

func TestCapabilities(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "traefik", Annotations: map[string]string{networkingv1.AnnotationIsDefaultIngressClass: "true"}}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}, Provisioner: "rancher.io/local-path"},
	)
	c := s.Capabilities(context.Background())
	if strings.Join(c.GetIngressClasses(), ",") != "nginx,traefik (default)" || strings.Join(c.GetStorageClasses(), ",") != "local-path" {
		t.Errorf("capabilities: %+v", c)
	}
	if c.GetApis() == nil {
		t.Error("apis must be an empty list, not nil")
	}
}
