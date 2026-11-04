package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const verifyBase = "http://odyssey-access.odyssey-system.svc:8098/verify"

func traefikMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(middlewareGVK, meta.RESTScopeNamespace)
	return m
}

func protectedApp(mode v1alpha1.AccessMode) *v1alpha1.OdysseyApp {
	a := newApp(1)
	a.Labels = map[string]string{LabelEnvironment: "env-1"}
	a.Spec.Access = &v1alpha1.AccessSpec{Mode: mode, Organization: "org-1"}
	return a
}

func middleware(t *testing.T, c client.Client, ns, name string) *unstructured.Unstructured {
	t.Helper()
	m := &unstructured.Unstructured{}
	m.SetGroupVersionKind(middlewareGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, m); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return m
}

func exposed(t *testing.T, mapper meta.RESTMapper, a *v1alpha1.OdysseyApp, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(append(objs, a)...).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	r := &Reconciler{Client: c, API: c, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now },
		Exposure: &Traefik{Client: c, VerifyURL: verifyBase, Mapper: mapper}}
	return r, c
}

func TestProtectedAppGetsMiddlewaresAndAnnotation(t *testing.T) {
	for _, mode := range []v1alpha1.AccessMode{v1alpha1.AccessTeam, v1alpha1.AccessOrganization} {
		a := protectedApp(mode)
		r, c := exposed(t, traefikMapper(), a)
		got, _ := runWith(t, r, c, a)
		if got.Status.Phase == v1alpha1.PhaseFailed {
			t.Fatalf("%s: %+v", mode, got.Status)
		}
		strip, auth := middleware(t, c, a.Namespace, "portal-odicea-strip"), middleware(t, c, a.Namespace, "portal-odicea-access")
		if strip == nil || auth == nil {
			t.Fatalf("%s: middlewares missing: %v %v", mode, strip, auth)
		}
		hdrs, _, _ := unstructured.NestedStringMap(strip.Object, "spec", "headers", "customRequestHeaders")
		for _, h := range []string{"X-Odicea-User", "X-Odicea-Email", "X-Odicea-Name", "X-Odicea-Org", "X-Odicea-Assertion", "X_Odicea_User", "X_Odicea_Email", "X_Odicea_Name", "X_Odicea_Org", "X_Odicea_Assertion"} {
			if v, ok := hdrs[h]; !ok || v != "" {
				t.Errorf("strip must set %s to empty: %v", h, hdrs)
			}
		}
		fa, _, _ := unstructured.NestedMap(auth.Object, "spec", "forwardAuth")
		want := verifyBase + "?env=env-1&host=portal.apps.test&mode=" + string(mode) + "&org=org-1&port=8088&scheme=http"
		if fa["address"] != want || fa["trustForwardHeader"] != false {
			t.Errorf("forwardAuth: %v, want address %s", fa, want)
		}
		resp, _ := fa["authResponseHeaders"].([]any)
		if len(resp) != 5 || resp[4] != "X-Odicea-Assertion" {
			t.Errorf("authResponseHeaders: %v", fa["authResponseHeaders"])
		}
		for _, m := range []*unstructured.Unstructured{strip, auth} {
			if refs := m.GetOwnerReferences(); len(refs) != 1 || refs[0].Name != "portal" || m.GetLabels()[LabelManagedBy] != ManagedBy {
				t.Errorf("%s: owner/labels: %v %v", m.GetName(), m.GetOwnerReferences(), m.GetLabels())
			}
		}
		var ing networkingv1.Ingress
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &ing); err != nil {
			t.Fatal(err)
		}
		wantAnn := "odyssey-env1-portal-odicea-strip@kubernetescrd,odyssey-env1-portal-odicea-access@kubernetescrd"
		if ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] != wantAnn {
			t.Errorf("annotation: %v", ing.Annotations)
		}
	}
}

func TestPublicAppDropsMiddlewaresAndAnnotation(t *testing.T) {
	a := protectedApp(v1alpha1.AccessTeam)
	r, c := exposed(t, traefikMapper(), a)
	runWith(t, r, c, a)

	// the next revision is public
	var cur v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Revision, cur.Spec.Access.Mode = 2, v1alpha1.AccessPublic
	if err := c.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	runWith(t, r, c, a)
	if middleware(t, c, a.Namespace, "portal-odicea-strip") != nil || middleware(t, c, a.Namespace, "portal-odicea-access") != nil {
		t.Error("public app keeps its middlewares")
	}
	var ing networkingv1.Ingress
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &ing); err != nil {
		t.Fatal(err)
	}
	if _, ok := ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]; ok {
		t.Errorf("public app keeps the annotation: %v", ing.Annotations)
	}
}

func TestAppWithoutAccessIsPublic(t *testing.T) {
	a := newApp(1) // written before access existed
	r, c := exposed(t, traefikMapper(), a)
	if got, _ := runWith(t, r, c, a); got.Status.Phase == v1alpha1.PhaseFailed {
		t.Fatalf("%+v", got.Status)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); err != nil {
		t.Errorf("public app is exposed: %v", err)
	}
}

func TestProtectedAppFailsClosedWithoutTheGate(t *testing.T) {
	cases := map[string]func(*v1alpha1.OdysseyApp) (*Reconciler, client.Client){
		"no exposure configured": func(a *v1alpha1.OdysseyApp) (*Reconciler, client.Client) {
			r, c := exposed(t, traefikMapper(), a)
			r.Exposure = nil
			return r, c
		},
		"no traefik.io in the cluster": func(a *v1alpha1.OdysseyApp) (*Reconciler, client.Client) {
			return exposed(t, meta.NewDefaultRESTMapper(nil), a)
		},
	}
	for name, setup := range cases {
		a := protectedApp(v1alpha1.AccessTeam)
		// an Ingress left by an earlier public revision must go
		old := &networkingv1.Ingress{}
		old.Name, old.Namespace = a.Name, a.Namespace
		mutateIngress(old, a, cfg)
		r, c := setup(a)
		if err := c.Create(context.Background(), old); err != nil {
			t.Fatal(err)
		}
		got, _ := runWith(t, r, c, a)
		if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || got.Status.Message != UnavailableMessage {
			t.Fatalf("%s: %+v", name, got.Status)
		}
		if !strings.Contains(UnavailableMessage, "inicio de sesión") {
			t.Fatal("message is Spanish")
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s: the app must not be exposed: %v", name, err)
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s: nothing runs for it: %v", name, err)
		}
	}
}

func TestVerifyAddressEscapes(t *testing.T) {
	got := verifyAddress(verifyBase, Policy{Mode: "team", Environment: "a&b=c", Organization: "o", Host: "Portal.Apps.Test", Scheme: "https"})
	if got != verifyBase+"?env=a%26b%3Dc&host=portal.apps.test&mode=team&org=o&scheme=https" {
		t.Error(got)
	}
}

func ingressOf(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp) *networkingv1.Ingress {
	t.Helper()
	var ing networkingv1.Ingress
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &ing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return &ing
}

func TestPublicToProtectedAnnotatesTheIngressInOnePass(t *testing.T) {
	a := newApp(1)
	a.Labels = map[string]string{LabelEnvironment: "env-1"}
	r, c := exposed(t, traefikMapper(), a)
	runWith(t, r, c, a)
	if ing := ingressOf(t, c, a); ing == nil || ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] != "" {
		t.Fatalf("setup: %v", ing)
	}
	var cur v1alpha1.OdysseyApp
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Revision, cur.Spec.Access = 2, &v1alpha1.AccessSpec{Mode: v1alpha1.AccessTeam, Organization: "org-1"}
	if err := c.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	runWith(t, r, c, a)
	ing := ingressOf(t, c, a)
	if ing == nil || !strings.Contains(ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"], "portal-odicea-access@kubernetescrd") {
		t.Fatalf("the Ingress must carry the gate after one reconcile: %v", ing)
	}
}

func TestProtectedAppFailsClosedOnErrors(t *testing.T) {
	cases := map[string]func(*interceptor.Funcs){
		"protect fails": func(f *interceptor.Funcs) {
			f.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.CreateOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return errors.New("boom")
				}
				return c.Create(ctx, obj, o...)
			}
		},
		"ingress update fails": func(f *interceptor.Funcs) {
			f.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.UpdateOption) error {
				if _, ok := obj.(*networkingv1.Ingress); ok {
					return errors.New("boom")
				}
				return c.Update(ctx, obj, o...)
			}
		},
	}
	for name, setup := range cases {
		a := newApp(1)
		a.Labels = map[string]string{LabelEnvironment: "env-1"}
		// the app was public: its Ingress exists
		old := &networkingv1.Ingress{}
		old.Name, old.Namespace = a.Name, a.Namespace
		mutateIngress(old, a, cfg)
		old.Labels = map[string]string{LabelManagedBy: ManagedBy}
		a.Spec.Revision, a.Spec.Access = 2, &v1alpha1.AccessSpec{Mode: v1alpha1.AccessTeam, Organization: "org-1"}
		var f interceptor.Funcs
		setup(&f)
		base := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, old).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
		c := interceptor.NewClient(base.(client.WithWatch), f)
		r := &Reconciler{Client: c, API: base, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now },
			Exposure: &Traefik{Client: c, VerifyURL: verifyBase, Mapper: traefikMapper()}}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(a)}); err == nil {
			t.Errorf("%s: the error must surface", name)
		}
		if ing := ingressOf(t, base, a); ing != nil {
			t.Errorf("%s: a protected app must not stay exposed: %v", name, ing.Annotations)
		}
	}
}

func TestUnprotectSkipsDeletesWhenNothingIsThere(t *testing.T) {
	a := newApp(1)
	a.Labels = map[string]string{LabelEnvironment: "env-1"}
	deletes := 0
	base := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.DeleteOption) error {
		deletes++
		return c.Delete(ctx, obj, o...)
	}})
	r := &Reconciler{Client: c, API: base, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now },
		Exposure: &Traefik{Client: c, VerifyURL: verifyBase, Mapper: traefikMapper()}}
	runWith(t, r, base, a)
	runWith(t, r, base, a)
	if deletes != 0 {
		t.Errorf("a public app that never had middlewares sent %d DELETEs", deletes)
	}
}

func TestPublicAppDropsAStaleAnnotationWithoutExposure(t *testing.T) {
	a := newApp(1)
	old := &networkingv1.Ingress{}
	old.Name, old.Namespace = a.Name, a.Namespace
	mutateIngress(old, a, cfg)
	old.Labels = map[string]string{LabelManagedBy: ManagedBy}
	old.Annotations = map[string]string{annotationMiddlewares: "odyssey-env1-portal-odicea-strip@kubernetescrd,odyssey-env1-portal-odicea-access@kubernetescrd", "keep": "me"}
	r, c := exposed(t, traefikMapper(), a, old)
	r.Exposure = nil // the gate is disabled
	runWith(t, r, c, a)
	ing := ingressOf(t, c, a)
	if ing == nil {
		t.Fatal("a public app stays exposed")
	}
	if _, ok := ing.Annotations[annotationMiddlewares]; ok || ing.Annotations["keep"] != "me" {
		t.Errorf("annotations: %v", ing.Annotations)
	}
}

func TestSameRevisionSpecChangeMakesTheStatusStale(t *testing.T) {
	// Failed/config for generation 1 (no gate), then the access mode changes
	// at the same revision (generation 2) and the gate is there: the old
	// final verdict must not stick.
	a := protectedApp(v1alpha1.AccessTeam)
	a.Generation = 1
	r, c := exposed(t, traefikMapper(), a)
	exp := r.Exposure
	r.Exposure = nil
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.ObservedGeneration != 1 {
		t.Fatalf("setup: %+v", got.Status)
	}
	got.Generation = 2
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	r.Exposure = exp
	got, _ = runWith(t, r, c, a)
	if got.Status.ObservedGeneration != 2 || got.Status.Phase == v1alpha1.PhaseFailed {
		t.Errorf("status still describes the old spec: %+v", got.Status)
	}
}

func TestProtectedInvalidIngressUpdateLeavesNothingOpen(t *testing.T) {
	a := newApp(1)
	a.Labels = map[string]string{LabelEnvironment: "env-1"}
	old := &networkingv1.Ingress{}
	old.Name, old.Namespace = a.Name, a.Namespace
	mutateIngress(old, a, cfg)
	old.Labels = map[string]string{LabelManagedBy: ManagedBy}
	a.Spec.Revision, a.Spec.Access = 2, &v1alpha1.AccessSpec{Mode: v1alpha1.AccessTeam, Organization: "org-1"}
	base := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, old).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.UpdateOption) error {
		if _, ok := obj.(*networkingv1.Ingress); ok {
			return apierrors.NewInvalid(networkingv1.SchemeGroupVersion.WithKind("Ingress").GroupKind(), obj.GetName(), nil)
		}
		return c.Update(ctx, obj, o...)
	}})
	r := &Reconciler{Client: c, API: base, Config: cfg, HealthGrace: time.Minute, Now: func() time.Time { return now },
		Exposure: &Traefik{Client: c, VerifyURL: verifyBase, Mapper: traefikMapper()}}
	got, _ := runWith(t, r, base, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig {
		t.Errorf("status: %+v", got.Status)
	}
	if ing := ingressOf(t, base, a); ing != nil {
		t.Errorf("the old, unprotected Ingress is still live: %v", ing.Annotations)
	}
}

func authEnv(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp) map[string]string {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &d); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "ODICEA_") {
			if _, dup := out[e.Name]; dup {
				t.Errorf("%s is set twice", e.Name)
			}
			out[e.Name] = e.Value
		}
	}
	return out
}

func TestProtectedAppGetsTheAuthEnvAndItWinsOverTheUsers(t *testing.T) {
	for _, mode := range []v1alpha1.AccessMode{v1alpha1.AccessTeam, v1alpha1.AccessOrganization} {
		a := protectedApp(mode)
		a.Spec.Env = map[string]string{"ODICEA_AUTH_AUDIENCE": "evil.test", "ODICEA_AUTH_ISSUER": "evil", "ODICEA_AUTH_JWKS_URL": "http://evil/", "ODICEA_AUTH_HEADER": "X-Evil", "KEEP": "1"}
		r, c := exposed(t, traefikMapper(), a)
		runWith(t, r, c, a)
		got := authEnv(t, c, a)
		want := map[string]string{
			"ODICEA_AUTH_JWKS_URL": DefaultAuthJWKSURL, "ODICEA_AUTH_ISSUER": "odicea-gate",
			"ODICEA_AUTH_AUDIENCE": "portal.apps.test:8088", "ODICEA_AUTH_HEADER": "X-Odicea-Assertion",
		}
		if len(got) != len(want) {
			t.Errorf("%s: %v", mode, got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", mode, k, got[k], v)
			}
		}
	}
	// the JWKS URL is the agent's configuration
	a := protectedApp(v1alpha1.AccessTeam)
	env := withAuthEnv(nil, a, Config{Domain: "apps.test", AuthJWKSURL: "http://gate:8099/jwks"})
	if env[0].Name != "ODICEA_AUTH_JWKS_URL" || env[0].Value != "http://gate:8099/jwks" || env[2].Value != "portal.apps.test" {
		t.Errorf("%v", env)
	}
}

func TestPublicAppGetsNoAuthEnv(t *testing.T) {
	for name, a := range map[string]*v1alpha1.OdysseyApp{"public": protectedApp(v1alpha1.AccessPublic), "no access": newApp(1)} {
		a.Spec.Env = map[string]string{"ODICEA_AUTH_ISSUER": "stale", "ODICEA_AUTH_JWKS_URL": "http://evil/"}
		r, c := exposed(t, traefikMapper(), a)
		runWith(t, r, c, a)
		// a public app has no gate behind it: no ODICEA_AUTH_* reaches it,
		// not even one handed in (only a hand-edited OdysseyApp could)
		if got := authEnv(t, c, a); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}
