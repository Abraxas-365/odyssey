package desired

import (
	"context"
	"errors"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
	"slices"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestApplyMapsAccess(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		mode string
		want v1alpha1.AccessMode
		ok   bool
	}{
		{"", v1alpha1.AccessPublic, true}, // revisions created before the gate
		{"public", v1alpha1.AccessPublic, true},
		{"team", v1alpha1.AccessTeam, true},
		{"organization", v1alpha1.AccessOrganization, true},
		{"everyone", "", false},
	} {
		s, cl := newSyncer(t, interceptor.Funcs{})
		a := app(1)
		a.Access = &fleetv1.Access{Mode: c.mode}
		err := s.Apply(ctx, a)
		if !c.ok {
			if !IsRejected(err) {
				t.Errorf("mode %q: want a rejection, got %v", c.mode, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got := get(t, cl).Spec.Access
		if got == nil || got.Mode != c.want || got.Organization != org {
			t.Errorf("mode %q: %+v", c.mode, got)
		}
	}
	s, cl := newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(ctx, app(1)); err != nil { // an old control plane sends no access at all
		t.Fatal(err)
	}
	if got := get(t, cl).Spec.Access; got == nil || got.Mode != v1alpha1.AccessPublic {
		t.Errorf("no access message: %+v", got)
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

func retired(rev int64) *fleetv1.App {
	return &fleetv1.App{OrganizationId: org, ProjectId: proj, EnvironmentId: env, Name: "portal", Revision: rev, Deleted: true}
}

func TestRetireRemovesTheAppAndLeavesTheNamespace(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	// Nothing to remove: done (a retirement resent after the app went).
	if err := s.Apply(ctx, retired(3)); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, app(4)); err != nil {
		t.Fatal(err)
	}
	// An older retirement never removes a newer revision.
	if err := s.Apply(ctx, retired(3)); err != nil {
		t.Fatal(err)
	}
	get(t, c)
	if err := s.Apply(ctx, retired(5)); err != nil {
		t.Fatal(err)
	}
	var a v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace(env), Name: "portal"}, &a); !apierrors.IsNotFound(err) {
		t.Fatalf("the retired app must be gone: %v", err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: Namespace(env)}, &ns); err != nil {
		t.Errorf("the environment's namespace (and its resources' data) stays: %v", err)
	}
	if err := s.Apply(ctx, retired(5)); err != nil {
		t.Errorf("a retirement is idempotent: %v", err)
	}
}

// The cache may not have seen an app created a moment ago (a deployment
// applied right before the archive): retire reads the API server, or the app
// would be reported "deleted", forgotten, and left running.
func TestRetireReadsTheAPIServerNotTheCache(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "odyssey.dev", Resource: "odysseyapps"}
	s, api := newSyncer(t, interceptor.Funcs{})
	if err := s.Apply(ctx, app(1)); err != nil {
		t.Fatal(err)
	}
	stale, _ := newSyncer(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.OdysseyApp); ok {
			return apierrors.NewNotFound(gr, key.Name) // the cache has not seen it yet
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	// writes go to the real store; reads of the app through Client are stale
	s.Client = &staleReads{Client: api, stale: stale.Client}
	s.API = api
	if err := s.Apply(ctx, retired(2)); err != nil {
		t.Fatal(err)
	}
	var a v1alpha1.OdysseyApp
	if err := api.Get(ctx, client.ObjectKey{Namespace: Namespace(env), Name: "portal"}, &a); !apierrors.IsNotFound(err) {
		t.Fatalf("an app the cache had not seen yet survived its retirement: %v", err)
	}
}

// staleReads is a Client whose reads come from another (stale) client.
type staleReads struct {
	client.Client
	stale client.Client
}

func (s *staleReads) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return s.stale.Get(ctx, key, obj, opts...)
}

func TestRetireErrors(t *testing.T) {
	gr := schema.GroupResource{Group: "odyssey.dev", Resource: "odysseyapps"}
	for _, c := range []struct {
		name     string
		err      error
		rejected bool
	}{
		{"changed meanwhile", apierrors.NewConflict(gr, "portal", errors.New("precondition")), false},
		{"gone meanwhile", apierrors.NewNotFound(gr, "portal"), false},
		{"not allowed", apierrors.NewForbidden(gr, "portal", errors.New("rbac")), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, cl := newSyncer(t, interceptor.Funcs{Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return c.err
			}})
			if err := s.Apply(context.Background(), app(1)); err != nil {
				t.Fatal(err)
			}
			get(t, cl)
			err := s.Apply(context.Background(), retired(2))
			switch {
			case apierrors.IsNotFound(c.err):
				if err != nil {
					t.Errorf("an app gone meanwhile is retired: %v", err)
				}
			case err == nil || IsRejected(err) != c.rejected:
				t.Errorf("err %v, rejected %v", err, IsRejected(err))
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

func TestAccessCapabilityNeedsTheGateAndTraefik(t *testing.T) {
	groups := func(names ...string) *fakediscovery.FakeDiscovery {
		fd := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
		for _, n := range names {
			fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: n + "/v1alpha1"})
		}
		return fd
	}
	for name, c := range map[string]struct {
		gate bool
		fd   *fakediscovery.FakeDiscovery
		want bool
	}{
		"gate and traefik": {true, groups("traefik.io"), true},
		"no gate":          {false, groups("traefik.io"), false},
		"no traefik":       {true, groups("cert-manager.io"), false},
	} {
		s, _ := newSyncer(t, interceptor.Funcs{})
		s.Discovery, s.AccessGate = c.fd, c.gate
		if got := slices.Contains(s.Capabilities(context.Background()).GetApis(), CapabilityAccess); got != c.want {
			t.Errorf("%s: odicea.access reported = %v", name, got)
		}
	}
}

func TestStatusIsStaleWhenTheSpecChangedAfterIt(t *testing.T) {
	a := &v1alpha1.OdysseyApp{}
	a.Name, a.Namespace, a.Generation = "portal", Namespace(env), 2
	a.Labels = map[string]string{LabelEnvironment: env}
	a.Status = v1alpha1.OdysseyAppStatus{ObservedRevision: 1, ObservedGeneration: 1, Phase: v1alpha1.PhaseFailed}
	if statusOf(a) != nil {
		t.Error("a status of an older generation must not be reported")
	}
	a.Status.ObservedGeneration = 2
	if statusOf(a) == nil {
		t.Error("a fresh status is reported")
	}
}

func TestValidSecrets(t *testing.T) {
	managed := func(name string, version int64) *fleetv1.SecretRef {
		return &fleetv1.SecretRef{Name: name, Source: "managed", Version: version}
	}
	external := func(name, store, key string) *fleetv1.SecretRef {
		return &fleetv1.SecretRef{Name: name, Source: "external", Store: store, Key: key}
	}
	many := make([]*fleetv1.SecretRef, 101)
	for i := range many {
		many[i] = managed("S"+strings.Repeat("x", i%5)+string(rune('a'+i/26))+string(rune('a'+i%26)), 1)
	}
	for name, c := range map[string]struct {
		refs []*fleetv1.SecretRef
		want string // "" = valid
	}{
		"none":                    {nil, ""},
		"managed and external":    {[]*fleetv1.SecretRef{managed("DATABASE_URL", 3), external("API_KEY", "vault", "prod/api")}, ""},
		"underscore first":        {[]*fleetv1.SecretRef{managed("_X1", 1)}, ""},
		"name starts with digit":  {[]*fleetv1.SecretRef{managed("1X", 1)}, "environment variable"},
		"name with dash":          {[]*fleetv1.SecretRef{managed("A-B", 1)}, "environment variable"},
		"empty name":              {[]*fleetv1.SecretRef{managed("", 1)}, "environment variable"},
		"name too long":           {[]*fleetv1.SecretRef{managed(strings.Repeat("A", 129), 1)}, "environment variable"},
		"name of 128":             {[]*fleetv1.SecretRef{managed(strings.Repeat("A", 128), 1)}, ""},
		"duplicate":               {[]*fleetv1.SecretRef{managed("A", 1), external("A", "vault", "k")}, "repeated"},
		"unknown source":          {[]*fleetv1.SecretRef{{Name: "A", Source: "vault", Version: 1}}, "not managed or external"},
		"empty source":            {[]*fleetv1.SecretRef{{Name: "A", Version: 1}}, "not managed or external"},
		"managed without version": {[]*fleetv1.SecretRef{managed("A", 0)}, "version"},
		"external without store":  {[]*fleetv1.SecretRef{external("A", "", "k")}, "store and a key"},
		"external without key":    {[]*fleetv1.SecretRef{external("A", "vault", "")}, "store and a key"},
		"store is not a name":     {[]*fleetv1.SecretRef{external("A", "Not A Name", "k")}, "store"},
		"more than 100":           {many, "at most 100"},
	} {
		a := app(1)
		a.Secrets = c.refs
		err := Valid(a)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestApplyMapsSecretsWithoutValues(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	a := app(1)
	a.Secrets = []*fleetv1.SecretRef{
		{Name: "B_EXT", Source: "external", Store: "vault", Key: "prod/b", Property: "password", Version: 9},
		{Name: "A_MAN", Source: "managed", Version: 4, Store: "ignored", Key: "ignored"},
	}
	if err := s.Apply(ctx, a); err != nil {
		t.Fatal(err)
	}
	got := get(t, c).Spec.Secrets
	want := []v1alpha1.SecretRefSpec{
		{Name: "A_MAN", Source: v1alpha1.SecretManaged, Version: 4},
		{Name: "B_EXT", Source: v1alpha1.SecretExternal, Store: "vault", Key: "prod/b", Property: "password"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("secrets: %+v, want %+v", got, want)
	}

	// A new version is a spec change at the same revision: it is applied.
	a.Secrets[1].Version = 5
	if err := s.Apply(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := get(t, c).Spec.Secrets; got[0].Version != 5 {
		t.Errorf("version not updated: %+v", got)
	}

	bad := app(2)
	bad.Secrets = []*fleetv1.SecretRef{{Name: "9", Source: "managed", Version: 1}}
	if err := s.Apply(ctx, bad); !IsRejected(err) {
		t.Errorf("an invalid secret must reject the app: %v", err)
	}
}

func TestValidSecretDelivery(t *testing.T) {
	ok := &fleetv1.Secret{OrganizationId: org, ProjectId: proj, EnvironmentId: env, App: "portal", Name: "A", Version: 1, KeyId: "k", Sealed: []byte{1}}
	if err := ValidSecret(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*fleetv1.Secret){
		"organization": func(s *fleetv1.Secret) { s.OrganizationId = "x" },
		"project":      func(s *fleetv1.Secret) { s.ProjectId = "" },
		"environment":  func(s *fleetv1.Secret) { s.EnvironmentId = strings.ToUpper(s.EnvironmentId) },
		"app":          func(s *fleetv1.Secret) { s.App = "a/b" },
		"name":         func(s *fleetv1.Secret) { s.Name = "a-b" },
		"version":      func(s *fleetv1.Secret) { s.Version = -1 },
		"sealed":       func(s *fleetv1.Secret) { s.Sealed = nil },
	} {
		c := &fleetv1.Secret{}
		c.OrganizationId, c.ProjectId, c.EnvironmentId, c.App, c.Name, c.Version, c.KeyId, c.Sealed =
			ok.OrganizationId, ok.ProjectId, ok.EnvironmentId, ok.App, ok.Name, ok.Version, ok.KeyId, ok.Sealed
		mutate(c)
		if ValidSecret(c) == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestSecretStoresCapability(t *testing.T) {
	store := func(name string) client.Object {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ClusterSecretStore"})
		u.SetName(name)
		return u
	}
	groups := func(names ...string) *fakediscovery.FakeDiscovery {
		fd := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
		for _, n := range names {
			fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: n + "/v1"})
		}
		return fd
	}

	s, _ := newSyncer(t, interceptor.Funcs{}, store("vault"), store("aws"))
	s.Discovery = groups("external-secrets.io")
	if got := s.Capabilities(context.Background()).GetSecretStores(); strings.Join(got, ",") != "aws,vault" {
		t.Errorf("stores: %v", got)
	}

	// Without the operator nothing is listed (and nothing is asked).
	listed := false
	s, _ = newSyncer(t, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if u, ok := l.(*unstructured.UnstructuredList); ok && u.GroupVersionKind().Group == "external-secrets.io" {
			listed = true
		}
		return c.List(ctx, l, o...)
	}}, store("vault"))
	s.Discovery = groups("cert-manager.io")
	if got := s.Capabilities(context.Background()).GetSecretStores(); got == nil || len(got) != 0 || listed {
		t.Errorf("no operator: %#v listed=%v", got, listed)
	}

	// The operator is there but the list is refused: empty, the agent still connects.
	s, _ = newSyncer(t, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if u, ok := l.(*unstructured.UnstructuredList); ok && u.GroupVersionKind().Group == "external-secrets.io" {
			return apierrors.NewForbidden(schema.GroupResource{Group: "external-secrets.io", Resource: "clustersecretstores"}, "", errors.New("no"))
		}
		return c.List(ctx, l, o...)
	}})
	s.Discovery = groups("external-secrets.io")
	if got := s.Capabilities(context.Background()); got.GetSecretStores() == nil || len(got.GetSecretStores()) != 0 || !slices.Contains(got.GetApis(), "external-secrets.io") {
		t.Errorf("forbidden: %+v", got)
	}
}
