package desired

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func res(gen int64) *fleetv1.Resource {
	return &fleetv1.Resource{OrganizationId: org, ProjectId: proj, EnvironmentId: env, Name: "main", Kind: "postgres", Generation: gen, Storage: "2Gi"}
}

func getRes(t *testing.T, c client.Client) v1alpha1.OdysseyResource {
	t.Helper()
	var o v1alpha1.OdysseyResource
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace(env), Name: "main"}, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestApplyResourceWritesTheCRAndEnsuresTheNamespace(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(1)); err != nil {
		t.Fatal(err)
	}
	o := getRes(t, c)
	if o.Spec.Kind != "postgres" || o.Spec.Generation != 1 || o.Spec.Storage != "2Gi" || o.Spec.Deleted {
		t.Errorf("spec: %+v", o.Spec)
	}
	if o.Labels[LabelEnvironment] != env || o.Labels[LabelOrganization] != org || o.Labels[LabelProject] != proj {
		t.Errorf("labels: %v", o.Labels)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: Namespace(env)}, &ns); err != nil || ns.Labels[LabelManagedBy] != ManagedBy {
		t.Errorf("namespace: %v %v", err, ns.Labels)
	}
}

func TestApplyResourceNeverLowersTheGeneration(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	for _, g := range []int64{2, 1} {
		if err := s.ApplyResource(ctx, res(g)); err != nil {
			t.Fatal(err)
		}
	}
	if o := getRes(t, c); o.Spec.Generation != 2 {
		t.Fatalf("older generation won: %+v", o.Spec)
	}
	// Growth is taken, shrinking is not, and the delete flag is set.
	bigger := res(3)
	bigger.Storage = "10Gi"
	if err := s.ApplyResource(ctx, bigger); err != nil {
		t.Fatal(err)
	}
	smaller := res(4)
	smaller.Storage = "1Gi"
	if err := s.ApplyResource(ctx, smaller); err != nil {
		t.Fatal(err)
	}
	if o := getRes(t, c); o.Spec.Storage != "10Gi" || o.Spec.Generation != 4 {
		t.Fatalf("storage only grows: %+v", o.Spec)
	}
	del := res(5)
	del.Deleted = true
	if err := s.ApplyResource(ctx, del); err != nil {
		t.Fatal(err)
	}
	if o := getRes(t, c); !o.Spec.Deleted || o.Spec.Generation != 5 {
		t.Fatalf("deleted: %+v", o.Spec)
	}
	// A resource being deleted cannot be brought back by a later generation yet.
	if err := s.ApplyResource(ctx, res(6)); err == nil || errors.As(err, new(*Rejected)) {
		t.Fatalf("retry later: %v", err)
	}
}

func TestApplyResourceRefusesWhatItCannotName(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	for name, mut := range map[string]func(*fleetv1.Resource){
		"kind":       func(r *fleetv1.Resource) { r.Kind = "mongo" },
		"name":       func(r *fleetv1.Resource) { r.Name = "Bad_Name" },
		"storage":    func(r *fleetv1.Resource) { r.Storage = "lots" },
		"no storage": func(r *fleetv1.Resource) { r.Storage = "" },
		// object storage has no engine of its own: only a connection of the
		// company's (spec.existing)
		"object storage": func(r *fleetv1.Resource) { r.Kind = "object-storage" },
		"env":            func(r *fleetv1.Resource) { r.EnvironmentId = "../x" },
		"generation":     func(r *fleetv1.Resource) { r.Generation = 0 },
	} {
		r := res(1)
		mut(r)
		var rej *Rejected
		if err := s.ApplyResource(context.Background(), r); !errors.As(err, &rej) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.ApplyResource(context.Background(), nil); err == nil {
		t.Error("nil")
	}
}

func TestApplyResourceErrorsAreRetriedUnlessFinal(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.OdysseyResource); ok {
			return errors.New("connection refused")
		}
		return c.Create(ctx, obj, o...)
	}})
	var rej *Rejected
	if err := s.ApplyResource(context.Background(), res(1)); err == nil || errors.As(err, &rej) {
		t.Fatalf("transient: %v", err)
	}
}

func TestApplyResourceCannotChangeKind(t *testing.T) {
	ctx := context.Background()
	s, _ := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(1)); err != nil {
		t.Fatal(err)
	}
	r := res(2)
	r.Kind = "redis"
	var rej *Rejected
	if err := s.ApplyResource(ctx, r); !errors.As(err, &rej) || !strings.Contains(err.Error(), "cannot become") {
		t.Fatalf("kind change: %v", err)
	}
}

func TestResourceStatuses(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(2)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ResourceStatuses(ctx); len(got) != 0 {
		t.Fatalf("nothing observed yet: %+v", got)
	}
	o := getRes(t, c)
	o.Status = v1alpha1.OdysseyResourceStatus{ObservedGeneration: 1, Phase: v1alpha1.ResourceReady}
	if err := c.Update(ctx, &o); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ResourceStatuses(ctx); len(got) != 0 {
		t.Fatalf("a status of an older generation is stale: %+v", got)
	}
	o.Status = v1alpha1.OdysseyResourceStatus{ObservedGeneration: 2, Phase: v1alpha1.ResourceFailed, Message: strings.Repeat("é", 2000)}
	if err := c.Update(ctx, &o); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResourceStatuses(ctx)
	if err != nil || len(got) != 1 || got[0].GetPhase() != "failed" || got[0].GetObservedGeneration() != 2 || got[0].GetEnvironmentId() != env || got[0].GetName() != "main" {
		t.Fatalf("statuses: %v %v", got, err)
	}
	if len([]rune(got[0].GetMessage())) > maxMessage {
		t.Errorf("message not clipped: %d", len([]rune(got[0].GetMessage())))
	}
	one, err := s.ResourceStatus(ctx, env, "main")
	if err != nil || one.GetPhase() != "failed" {
		t.Fatalf("one: %v %v", one, err)
	}
	if none, err := s.ResourceStatus(ctx, uuid.NewString(), "main"); err != nil || none != nil {
		t.Fatalf("absent: %v %v", none, err)
	}
}

func TestResourceReportedMarksOnlyDeleted(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(1)); err != nil {
		t.Fatal(err)
	}
	st := &fleetv1.ResourceStatus{EnvironmentId: env, Name: "main", ObservedGeneration: 1, Phase: "ready"}
	if err := s.ResourceReported(ctx, st); err != nil || getRes(t, c).Annotations[resource.AnnotationReported] != "" {
		t.Fatalf("only deleted is marked: %v", err)
	}
	st.Phase = "deleted"
	if err := s.ResourceReported(ctx, st); err != nil || getRes(t, c).Annotations[resource.AnnotationReported] != "1" {
		t.Fatalf("mark: %v %v", err, getRes(t, c).Annotations)
	}
	st.Name = "gone"
	if err := s.ResourceReported(ctx, st); err != nil {
		t.Fatalf("a resource already gone is fine: %v", err)
	}
}

func TestResourceKindsCapability(t *testing.T) {
	cases := map[string]struct {
		apis, classes []string
		want          string
	}{
		"everything":       {[]string{"postgresql.cnpg.io"}, []string{"local-path (default)"}, "postgres,redis,files"},
		"no operator":      {nil, []string{"local-path (default)"}, "redis,files"},
		"no default class": {[]string{"postgresql.cnpg.io"}, []string{"local-path"}, ""},
		"no class at all":  {[]string{"postgresql.cnpg.io"}, nil, ""},
	}
	for name, tc := range cases {
		got := resourceKinds(&fleetv1.Capabilities{Apis: tc.apis, StorageClasses: tc.classes})
		if strings.Join(got, ",") != tc.want || got == nil {
			t.Errorf("%s: %v", name, got)
		}
	}
	s, _ := newSyncer(t, interceptor.Funcs{},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "std", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}})
	if got := s.Capabilities(context.Background()).GetResourceKinds(); strings.Join(got, ",") != "redis,files" {
		t.Errorf("capabilities: %v", got)
	}
	if got := s.Capabilities(context.Background()).GetResourceCapabilities(); strings.Join(got, ",") != resourcespec.ConnectExisting+","+resourcespec.SpecV1 {
		t.Errorf("without databases only the spec version and existing connections: %v", got)
	}
}

func TestResourceCapabilitiesFollowTheKinds(t *testing.T) {
	// A connection the company already has needs no engine of the cluster
	// (one that reads a secret store needs the store, listed apart).
	if got := resourceCapabilities(nil); !slices.Equal(got, []string{resourcespec.ConnectExisting}) {
		t.Errorf("nothing to provision, only existing connections: %v", got)
	}
	got := resourceCapabilities([]string{"postgres", "redis", "files"})
	want := []string{resourcespec.ConnectExisting, resourcespec.SpecV1, resourcespec.PostgresInstances, resourcespec.PostgresResources, resourcespec.PostgresVersion}
	if !slices.IsSorted(got) || len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func produccion() *fleetv1.Resource {
	r := res(1)
	r.Storage, r.Recipe, r.SpecSchemaVersion = "10Gi", "postgres-produccion@1", resourcespec.SchemaVersion
	r.Spec = &fleetv1.Resource_BuiltinPostgres{BuiltinPostgres: &fleetv1.BuiltinPostgres{Instances: 2, Major: 17, Cpu: "1", Memory: "2Gi"}}
	return r
}

func TestApplyResourceCarriesTheRecipeAndItsSettings(t *testing.T) {
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(context.Background(), produccion()); err != nil {
		t.Fatal(err)
	}
	o := getRes(t, c)
	p := o.Spec.Postgres
	if o.Spec.Recipe != "postgres-produccion@1" || p == nil || p.Instances != 2 || p.Major != 17 || p.CPU != "1" || p.Memory != "2Gi" {
		t.Fatalf("spec: %+v %+v", o.Spec, p)
	}
	// A basic resource says no settings.
	if err := s.ApplyResource(context.Background(), res(2)); err != nil {
		t.Fatal(err)
	}
	if o := getRes(t, c); o.Spec.Postgres != nil || o.Spec.Recipe != "" {
		t.Errorf("basic: %+v", o.Spec)
	}
}

// reportes is a database the company already has (no size, a connection
// read partly from a store).
func reportes() *fleetv1.Resource {
	r := res(1)
	r.Storage, r.Recipe, r.SpecSchemaVersion = "", "org-rds-reportes@1", resourcespec.SchemaVersion
	var values []*fleetv1.ExistingValue
	for _, v := range resourcespec.ExamplePostgres("vault").Existing.Values {
		x := &fleetv1.ExistingValue{Variable: v.Variable, Value: v.Value}
		if v.Ref != nil {
			x.Ref = &fleetv1.ExistingRef{Store: v.Ref.Store, Key: v.Ref.Key, Property: v.Ref.Property}
		}
		values = append(values, x)
	}
	r.Spec = &fleetv1.Resource_Existing{Existing: &fleetv1.Existing{Values: values}}
	return r
}

func TestApplyResourceCarriesAConnectionOfTheCompany(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, reportes()); err != nil {
		t.Fatal(err)
	}
	o := getRes(t, c)
	if o.Spec.Storage != "" || o.Spec.Postgres != nil || o.Spec.Existing == nil || o.Spec.Recipe != "org-rds-reportes@1" {
		t.Fatalf("spec: %+v", o.Spec)
	}
	want := resourcespec.ExamplePostgres("vault").Existing.Values
	if len(o.Spec.Existing.Values) != len(want) {
		t.Fatalf("values %+v", o.Spec.Existing.Values)
	}
	for i, w := range want {
		g := o.Spec.Existing.Values[i]
		if g.Variable != w.Variable || g.Value != w.Value || (g.Ref == nil) != (w.Ref == nil) ||
			(w.Ref != nil && (g.Ref.Store != w.Ref.Store || g.Ref.Key != w.Ref.Key || g.Ref.Property != w.Ref.Property)) {
			t.Errorf("value %d: %+v, want %+v", i, g, w)
		}
	}
	// One name never swaps Odicea's data for the company's resource.
	built := res(2)
	var rej *Rejected
	if err := s.ApplyResource(ctx, built); !errors.As(err, &rej) {
		t.Errorf("existing → built-in: %v", err)
	}
	// Object storage is always the company's.
	s3 := reportes()
	s3.Name, s3.Kind = "archivos", "object-storage"
	s3.Spec = &fleetv1.Resource_Existing{Existing: &fleetv1.Existing{Values: []*fleetv1.ExistingValue{
		{Variable: "S3_BUCKET", Value: "facturas"}, {Variable: "S3_REGION", Value: "us-east-1"},
	}}}
	if err := s.ApplyResource(ctx, s3); err != nil {
		t.Errorf("object storage: %v", err)
	}
	// What the contract refuses, the agent refuses too.
	for name, mut := range map[string]func(*fleetv1.Resource){
		"written credential": func(r *fleetv1.Resource) {
			r.GetExisting().Values[4] = &fleetv1.ExistingValue{Variable: "PGPASSWORD", Value: "hunter2"}
		},
		"missing variable": func(r *fleetv1.Resource) { r.GetExisting().Values = r.GetExisting().Values[1:] },
		"files":            func(r *fleetv1.Resource) { r.Kind = "files" },
		"bad size":         func(r *fleetv1.Resource) { r.Storage = "lots" },
		"a size":           func(r *fleetv1.Resource) { r.Storage = "5Gi" },
	} {
		r := reportes()
		r.Name = "otro"
		mut(r)
		if err := s.ApplyResource(ctx, r); !errors.As(err, &rej) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestApplyResourceRefusesASpecItDoesNotKnow(t *testing.T) {
	s, _ := newSyncer(t, interceptor.Funcs{})
	for name, mut := range map[string]func(*fleetv1.Resource){
		"newer spec":      func(r *fleetv1.Resource) { r.SpecSchemaVersion = resourcespec.SchemaVersion + 1 },
		"no spec version": func(r *fleetv1.Resource) { r.SpecSchemaVersion = 0 },
		"unknown spec":    func(r *fleetv1.Resource) { r.Spec = nil }, // a oneof arm newer than this agent
		"kind mismatch":   func(r *fleetv1.Resource) { r.Kind = "redis" },
		"major":           func(r *fleetv1.Resource) { r.GetBuiltinPostgres().Major = 15 },
		"replicas":        func(r *fleetv1.Resource) { r.GetBuiltinPostgres().Instances = 9 },
		"quantity":        func(r *fleetv1.Resource) { r.GetBuiltinPostgres().Memory = "much" },
		"recipe":          func(r *fleetv1.Resource) { r.Recipe = strings.Repeat("x", MaxRecipe+1) },
	} {
		r := produccion()
		mut(r)
		var rej *Rejected
		if err := s.ApplyResource(context.Background(), r); !errors.As(err, &rej) {
			t.Errorf("%s: %v", name, err)
		}
	}
	newer := produccion()
	newer.SpecSchemaVersion = 2
	if err := s.ApplyResource(context.Background(), newer); err == nil || !strings.Contains(err.Error(), "update the agent") {
		t.Errorf("says the agent needs an update: %v", err)
	}
}

func TestApplyMapsResourcesAndRefusesBadOnes(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	a := app(1)
	a.Resources = []*fleetv1.ResourceRef{{Name: "main", Kind: "postgres"}, {Name: "cache", Kind: "redis", Prefix: "CACHE"}}
	if err := s.Apply(ctx, a); err != nil {
		t.Fatal(err)
	}
	got := get(t, c)
	if len(got.Spec.Resources) != 2 || got.Spec.Resources[0].Name != "cache" || got.Spec.Resources[0].Prefix != "CACHE" || got.Spec.Resources[1].Kind != v1alpha1.ResourcePostgres {
		t.Errorf("resources: %+v", got.Spec.Resources)
	}
	for name, refs := range map[string][]*fleetv1.ResourceRef{
		"kind":     {{Name: "x", Kind: "mongo"}},
		"prefix":   {{Name: "x", Kind: "redis", Prefix: "bad prefix"}},
		"repeated": {{Name: "x", Kind: "redis"}, {Name: "x", Kind: "redis"}},
	} {
		b := app(2)
		b.Resources = refs
		var rej *Rejected
		if err := s.Apply(ctx, b); !errors.As(err, &rej) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// terminate puts a finalizer on the resource and deletes it: it stays,
// terminating, as the controller's finalizer leaves it.
func terminate(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	o := getRes(t, c)
	o.Finalizers = []string{resource.Finalizer}
	if err := c.Update(ctx, &o); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, &o); err != nil {
		t.Fatal(err)
	}
	if o = getRes(t, c); o.DeletionTimestamp.IsZero() {
		t.Fatal("the resource is not terminating")
	}
}

// The control plane restarts generations at 1 when a name is made again after
// the old resource was forgotten: while the old one is still being erased, the
// new spec must wait (not be dropped as "older"), and then be created.
func TestApplyResourceOfARecreatedNameWaitsForTheOldOneToGo(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(5)); err != nil {
		t.Fatal(err)
	}
	del := res(6)
	del.Deleted = true
	if err := s.ApplyResource(ctx, del); err != nil {
		t.Fatal(err)
	}
	terminate(t, c)

	err := s.ApplyResource(ctx, res(1))
	if err == nil || errors.As(err, new(*Rejected)) || !strings.Contains(err.Error(), "still being deleted") {
		t.Fatalf("a new generation 1 while the old generation 6 is being erased must be retried: %v", err)
	}
	if o := getRes(t, c); o.Spec.Generation != 6 || !o.Spec.Deleted {
		t.Fatalf("the old one is untouched: %+v", o.Spec)
	}

	// The controller finishes: the finalizer goes and so does the object.
	o := getRes(t, c)
	o.Finalizers = nil
	if err := c.Update(ctx, &o); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyResource(ctx, res(1)); err != nil {
		t.Fatal(err)
	}
	if o := getRes(t, c); o.Spec.Generation != 1 || o.Spec.Deleted || !o.DeletionTimestamp.IsZero() {
		t.Fatalf("created again at generation 1: %+v", o)
	}
}

// Deleted in the cluster by hand: the data is kept, a spec that is not a
// deletion is a no-op (not an endless retry), and the control plane's deletion
// is taken, even on the terminating object, so the controller erases it.
func TestApplyResourceOfAResourceDeletedByHand(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	if err := s.ApplyResource(ctx, res(2)); err != nil {
		t.Fatal(err)
	}
	terminate(t, c)

	for _, g := range []int64{1, 2, 3} {
		if err := s.ApplyResource(ctx, res(g)); err != nil {
			t.Fatalf("generation %d: a spec for a resource deleted by hand is a no-op, got %v", g, err)
		}
	}
	if o := getRes(t, c); o.Spec.Generation != 2 || o.Spec.Deleted {
		t.Fatalf("untouched: %+v", o.Spec)
	}

	del := res(4)
	del.Deleted = true
	if err := s.ApplyResource(ctx, del); err != nil {
		t.Fatal(err)
	}
	o := getRes(t, c)
	if !o.Spec.Deleted || o.Spec.Generation != 4 || o.DeletionTimestamp.IsZero() || len(o.Finalizers) != 1 {
		t.Fatalf("deleted=true set on the terminating object: %+v", o)
	}
	// And now it is the control plane's deletion in progress: retry, as usual.
	if err := s.ApplyResource(ctx, res(1)); err == nil || errors.As(err, new(*Rejected)) {
		t.Fatalf("retry later: %v", err)
	}
}

func TestApplyMapsTheAIAndTheAgentReportsTheCapability(t *testing.T) {
	ctx := context.Background()
	s, c := newSyncer(t, interceptor.Funcs{})
	a := app(1)
	a.Ai = &fleetv1.AI{BaseUrl: "https://llm.test", KeySecret: "ODICEA_AI_KEY"}
	if err := s.Apply(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := get(t, c).Spec.AI; got == nil || got.BaseURL != "https://llm.test" || got.KeySecret != "ODICEA_AI_KEY" {
		t.Errorf("ai: %+v", got)
	}
	// turning it off removes it from the spec
	b := app(2)
	if err := s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got := get(t, c).Spec.AI; got != nil {
		t.Errorf("ai after disabling: %+v", got)
	}
	if !slices.Contains(s.Capabilities(ctx).GetApis(), CapabilityAI) {
		t.Errorf("capabilities do not list %s: %v", CapabilityAI, s.Capabilities(ctx).GetApis())
	}
}
