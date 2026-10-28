package releasekube

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var target = release.Target{
	Organization: identity.NewOrganizationID(),
	Project:      project.NewID(),
	Environment:  project.NewEnvironmentID(),
}

func desired(rev int64) release.DesiredApp {
	return release.DesiredApp{
		Name: "portal", Revision: rev, Image: "r.test/portal@sha256:" + strings.Repeat("a", 64),
		Port: 8080, HealthPath: "/healthz", Replicas: 2, Env: map[string]string{"A": "1"},
		Labels: map[string]string{"odyssey.dev/deployment": "d" + string(rune('0'+rev))},
	}
}

func newCluster(t *testing.T, funcs interceptor.Funcs) (*Cluster, client.Client) {
	s, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.OdysseyApp{}).WithInterceptorFuncs(funcs).Build()
	return New(c), c
}

func get(t *testing.T, c client.Client) v1alpha1.OdysseyApp {
	t.Helper()
	var a v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace(target), Name: "portal"}, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestApplyWritesTheAppInTheEnvironmentNamespace(t *testing.T) {
	ctx := context.Background()
	k, c := newCluster(t, interceptor.Funcs{})
	if err := k.Apply(ctx, target, desired(2)); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: "odyssey-" + target.Environment.String()}, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels[labelOrganization] != target.Organization.String() || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("namespace labels: %v", ns.Labels)
	}
	a := get(t, c)
	if a.Spec.Revision != 2 || a.Spec.Port != 8080 || a.Spec.Replicas != 2 || a.Spec.HealthPath != "/healthz" || a.Spec.Env["A"] != "1" || a.Labels["odyssey.dev/deployment"] != "d2" {
		t.Fatalf("spec: %+v labels %v", a.Spec, a.Labels)
	}

	// Idempotent, and an older revision never wins late.
	if err := k.Apply(ctx, target, desired(2)); err != nil {
		t.Fatal(err)
	}
	if err := k.Apply(ctx, target, desired(1)); err != nil {
		t.Fatal(err)
	}
	if a := get(t, c); a.Spec.Revision != 2 {
		t.Errorf("an older revision replaced a newer one: %d", a.Spec.Revision)
	}
	if err := k.Apply(ctx, target, desired(3)); err != nil {
		t.Fatal(err)
	}
	if a := get(t, c); a.Spec.Revision != 3 || a.Labels["odyssey.dev/deployment"] != "d3" {
		t.Errorf("newer revision: %+v", a.Spec)
	}
}

func TestApplyErrors(t *testing.T) {
	gr := schema.GroupResource{Group: "odyssey.dev", Resource: "odysseyapps"}
	for _, c := range []struct {
		name  string
		err   error
		coded bool // fails the deployment instead of retrying
	}{
		{"rejected spec", apierrors.NewInvalid(schema.GroupKind{Group: "odyssey.dev", Kind: "OdysseyApp"}, "portal", nil), true},
		{"concurrent writer", apierrors.NewConflict(gr, "portal", errors.New("modified")), false},
		{"agent not installed", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "odyssey.dev", Kind: "OdysseyApp"}}, true},
		{"not allowed", apierrors.NewForbidden(gr, "portal", errors.New("rbac")), true},
		{"api down", errors.New("connection refused"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, _ := newCluster(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.OdysseyApp); ok {
					return c.err
				}
				return cl.Create(ctx, obj, opts...)
			}})
			err := k.Apply(context.Background(), target, desired(1))
			var e *errx.Error
			if coded := errors.As(err, &e) && e.Code == release.CodeApplyFailed; err == nil || coded != c.coded {
				t.Errorf("err %v, coded %v", err, coded)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	k, c := newCluster(t, interceptor.Funcs{})
	if s, err := k.Status(ctx, target, "portal"); err != nil || s.ObservedRevision != 0 {
		t.Fatalf("unknown app: %+v %v", s, err)
	}
	if err := k.Apply(ctx, target, desired(4)); err != nil {
		t.Fatal(err)
	}
	if s, _ := k.Status(ctx, target, "portal"); s.ObservedRevision != 0 || s.Phase != release.PhaseProgressing {
		t.Fatalf("not yet observed: %+v", s)
	}
	for _, x := range []struct {
		in     v1alpha1.OdysseyAppStatus
		phase  release.Phase
		reason release.FailureReason
	}{
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseProgressing}, release.PhaseProgressing, ""},
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseReady, ReadyReplicas: 2}, release.PhaseReady, ""},
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonImagePull, Message: "no such image"}, release.PhaseFailed, release.FailureImagePull},
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonCrash}, release.PhaseFailed, release.FailureCrash},
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig}, release.PhaseFailed, release.FailureConfig},
		{v1alpha1.OdysseyAppStatus{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonUnhealthy}, release.PhaseFailed, release.FailureUnhealthy},
	} {
		a := get(t, c)
		a.Status = x.in
		a.Status.ObservedRevision, a.Status.URL = 4, "http://portal.apps.test"
		if err := c.Status().Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
		s, err := k.Status(ctx, target, "portal")
		if err != nil || s.ObservedRevision != 4 || s.Phase != x.phase || s.Reason != x.reason || s.URL != "http://portal.apps.test" || s.Message != x.in.Message || s.ReadyReplicas != int(x.in.ReadyReplicas) {
			t.Errorf("%+v → %+v %v", x.in, s, err)
		}
	}
}
