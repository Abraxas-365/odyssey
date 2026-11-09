package resource

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/externalsecret"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// esoRec is a reconciler on a cluster that serves ExternalSecrets (eso) or not.
func esoRec(t *testing.T, eso bool, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(externalsecret.GVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(externalsecret.GVK.GroupVersion().WithKind(externalsecret.GVK.Kind+"List"), &unstructured.UnstructuredList{})
	mapper := meta.NewDefaultRESTMapper(nil)
	if eso {
		mapper.Add(externalsecret.GVK, meta.RESTScopeNamespace)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&v1alpha1.OdysseyResource{}).Build()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	return &Reconciler{Client: c, API: c, Mapper: mapper, Now: func() time.Time { return at }}, c
}

// later moves r's clock by d.
func later(r *Reconciler, d time.Duration) {
	at := r.Now().Add(d)
	r.Now = func() time.Time { return at }
}

// existingRes is a resource of kind the company already has, read as e.
func existingRes(name string, kind v1alpha1.ResourceKind, gen int64, e resourcespec.Existing) *v1alpha1.OdysseyResource {
	o := res(name, kind, gen, "")
	o.Spec.Existing = &v1alpha1.ExistingConnection{}
	for _, v := range e.Values {
		x := v1alpha1.ExistingValue{Variable: v.Variable, Value: v.Value}
		if v.Ref != nil {
			x.Ref = &v1alpha1.ExistingRef{Store: v.Ref.Store, Key: v.Ref.Key, Property: v.Ref.Property}
		}
		o.Spec.Existing.Values = append(o.Spec.Existing.Values, x)
	}
	return o
}

func written(values ...resourcespec.Value) resourcespec.Existing {
	return resourcespec.Existing{Values: values}
}

func lit(variable, value string) resourcespec.Value {
	return resourcespec.Value{Variable: variable, Value: value}
}

func ref(variable, store, key, property string) resourcespec.Value {
	return resourcespec.Value{Variable: variable, Ref: &resourcespec.Ref{Store: store, Key: key, Property: property}}
}

func getES(t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	es := externalsecret.New()
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, es); err != nil {
		t.Fatal(err)
	}
	return es
}

// setReady writes the status ESO would: Ready status, and whether it synced
// es's current generation.
func setReady(t *testing.T, c client.Client, es *unstructured.Unstructured, status string, current bool) {
	t.Helper()
	st := map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": status, "reason": "x", "message": "the store said: secret/rds is gone"}},
	}
	if current {
		st["syncedResourceVersion"] = strconv.FormatInt(es.GetGeneration(), 10) + "-abc"
	}
	es.Object["status"] = st
	if err := c.Update(context.Background(), es); err != nil {
		t.Fatal(err)
	}
}

// render runs a key's template the way ESO's engine v2 does, with the
// functions it uses (sprig's replace and contains, Go's urlquery).
func render(t *testing.T, tpl string, data map[string]string) string {
	t.Helper()
	funcs := template.FuncMap{
		"replace":  func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
		"contains": func(sub, s string) bool { return strings.Contains(s, sub) },
	}
	tm, err := template.New("k").Funcs(funcs).Parse(tpl)
	if err != nil {
		t.Fatalf("%q: %v", tpl, err)
	}
	var b strings.Builder
	if err := tm.Execute(&b, data); err != nil {
		t.Fatalf("%q: %v", tpl, err)
	}
	return b.String()
}

func TestAWrittenConnectionIsAPlainSecret(t *testing.T) {
	ctx := context.Background()
	e := written(lit("PGHOST", "10.0.0.5"), lit("PGPORT", "5432"), lit("PGDATABASE", "ventas"),
		lit("PGUSER", "app"), lit("PGSSLMODE", "require"))
	// A credential is never written: PGPASSWORD must be a reference, so a
	// connection without one is refused by the contract. Use a kind where
	// all values may be written: object storage with the pod's identity.
	if err := (resourcespec.Spec{Existing: &e}).Validate(resourcekind.Postgres); err == nil {
		t.Fatal("a postgres connection without a password reference was accepted")
	}
	s3 := written(lit("S3_BUCKET", "facturas"), lit("S3_REGION", "us-east-1"))
	o := existingRes("archivos", v1alpha1.ResourceObjectStorage, 1, s3)
	r, c := esoRec(t, false, o)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("status %+v", got.Status)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ObjectStorageSecret("archivos")}, &sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data["bucket"]) != "facturas" || string(sec.Data["region"]) != "us-east-1" || len(sec.Data) != 2 {
		t.Errorf("only what is given, optional keys absent: %v", sec.Data)
	}
	if !metav1.IsControlledBy(&sec, got) {
		t.Error("the Secret goes with the resource")
	}
}

func TestAReferencedConnectionIsOneExternalSecretAcrossStores(t *testing.T) {
	ctx := context.Background()
	e := written(lit("PGHOST", "db.corp.example"), lit("PGPORT", "5432"), lit("PGDATABASE", "ventas {{ .v4 }}"),
		ref("PGUSER", "vault", "rds/ventas", "username"), ref("PGPASSWORD", "aws", "rds/ventas", ""))
	o := existingRes("ventas", v1alpha1.ResourcePostgres, 1, e)
	r, c := esoRec(t, true, o)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("waits for ESO: %+v", got.Status)
	}
	es := getES(t, c, PostgresSecret("ventas"))
	if !metav1.IsControlledBy(es, got) {
		t.Error("the ExternalSecret goes with the resource")
	}
	spec, _ := es.Object["spec"].(map[string]any)
	if _, ok := spec["secretStoreRef"]; ok {
		t.Error("each reference names its own store")
	}
	data, _ := spec["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("one entry per reference: %v", data)
	}
	stores := map[string]string{}
	for _, d := range data {
		m := d.(map[string]any)
		store := m["sourceRef"].(map[string]any)["storeRef"].(map[string]any)
		if store["kind"] != "ClusterSecretStore" {
			t.Errorf("store kind %v", store["kind"])
		}
		stores[m["secretKey"].(string)] = store["name"].(string)
	}
	// v<i>: the variable's place in the contract (PGUSER 4, PGPASSWORD 5).
	if stores["v4"] != "vault" || stores["v5"] != "aws" {
		t.Errorf("entries %v", stores)
	}
	target := spec["target"].(map[string]any)
	if target["name"] != PostgresSecret("ventas") || target["creationPolicy"] != "Owner" {
		t.Errorf("target %v", target)
	}
	tpl := target["template"].(map[string]any)
	if tpl["engineVersion"] != "v2" {
		t.Errorf("engine %v", tpl["engineVersion"])
	}
	keys := tpl["data"].(map[string]any)
	// What ESO would write, with a password that needs every escape and a
	// written value that looks like a template: text, never run.
	values := map[string]string{"v4": "app@inedge", "v5": "p a/s%s@:?"}
	out := map[string]string{}
	for k, v := range keys {
		out[k] = render(t, v.(string), values)
	}
	want := map[string]string{
		"uri":  "postgresql://app%40inedge:p%20a%2Fs%25s%40%3A%3F@db.corp.example:5432/ventas%20%7B%7B%20.v4%20%7D%7D",
		"host": "db.corp.example", "port": "5432", "dbname": "ventas {{ .v4 }}", "user": "app@inedge", "password": "p a/s%s@:?",
	}
	if len(out) != len(want) {
		t.Errorf("keys %v (no sslmode: not given)", out)
	}
	for k, w := range want {
		if out[k] != w {
			t.Errorf("%s = %q, want %q", k, out[k], w)
		}
	}
	u, err := url.Parse(out["uri"])
	if err != nil || u.User.Username() != "app@inedge" || u.Path != "/ventas {{ .v4 }}" {
		t.Errorf("uri does not parse back: %v %v", u, err)
	} else if pw, _ := u.User.Password(); pw != "p a/s%s@:?" {
		t.Errorf("password back %q", pw)
	}

	// ESO's verdict: ready, or failed in a fixed phrase (never its own words).
	// A failure right after the spec was written may be about the previous
	// one: the resource waits for ESO's verdict on this one.
	setReady(t, c, es, "False", false)
	got, _ = pass(t, r, c, got)
	if got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Errorf("a failure within the grace %+v", got.Status)
	}
	later(r, externalsecret.Grace)
	got, _ = pass(t, r, c, got)
	if got.Status.Phase != v1alpha1.ResourceFailed || got.Status.Message != SyncFailedMessage {
		t.Errorf("failure %+v", got.Status)
	}
	setReady(t, c, getES(t, c, PostgresSecret("ventas")), "True", true)
	got, _ = pass(t, r, c, got)
	if got.Status.Phase != v1alpha1.ResourceReady {
		t.Errorf("ready %+v", got.Status)
	}
	// A refresh that fails later (the store briefly unreachable) leaves the
	// Secret with the values it read: still ready.
	setReady(t, c, getES(t, c, PostgresSecret("ventas")), "False", true)
	got, _ = pass(t, r, c, got)
	if got.Status.Phase != v1alpha1.ResourceReady {
		t.Errorf("stale %+v", got.Status)
	}
	_ = ctx
}

func TestAForeignExternalSecretIsNeverWaitedFor(t *testing.T) {
	foreign := externalsecret.New()
	foreign.SetName(ObjectStorageSecret("archivos"))
	foreign.SetNamespace(ns)
	o := existingRes("archivos", v1alpha1.ResourceObjectStorage, 1, written(lit("S3_BUCKET", "b"), lit("S3_REGION", "r")))
	r, c := esoRec(t, true, o, foreign)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "not managed by Odyssey") {
		t.Errorf("status %+v", got.Status)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), externalsecret.New()); err != nil {
		t.Errorf("someone else's ExternalSecret was touched: %v", err)
	}
}

func TestTheDerivedURLCarriesTheSSLModeAndBracketsIPv6(t *testing.T) {
	k := kinds[v1alpha1.ResourcePostgres]
	e := written(lit("PGHOST", "fd00::5"), lit("PGPORT", "5432"), lit("PGDATABASE", "d"), lit("PGSSLMODE", "verify-full"),
		ref("PGUSER", "v", "k", "u"), ref("PGPASSWORD", "v", "k", "p"))
	c, err := connectionOf(k, resourcekind.Postgres, e)
	if err != nil {
		t.Fatal(err)
	}
	tpl := c.template()
	got := render(t, tpl["uri"].(string), map[string]string{"v4": "u", "v5": "p"})
	if got != "postgresql://u:p@[fd00::5]:5432/d?sslmode=verify-full" {
		t.Errorf("uri %q", got)
	}
	if render(t, tpl["sslmode"].(string), nil) != "verify-full" {
		t.Errorf("sslmode %v", tpl["sslmode"])
	}
	// A host read from the store gets its brackets in the template.
	e.Values[0] = ref("PGHOST", "v", "k", "h")
	c, _ = connectionOf(k, resourcekind.Postgres, e)
	got = render(t, c.template()["uri"].(string), map[string]string{"v1": "fd00::6", "v4": "u", "v5": "p"})
	if got != "postgresql://u:p@[fd00::6]:5432/d?sslmode=verify-full" {
		t.Errorf("uri %q", got)
	}
}

func TestExistingWithoutESOFailsPlainly(t *testing.T) {
	o := existingRes("ventas", v1alpha1.ResourcePostgres, 1, example())
	r, c := esoRec(t, false, o)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceFailed || got.Status.Message != NoExternalSecretsMessage {
		t.Errorf("status %+v", got.Status)
	}
}

func TestExistingNeverTakesAnObjectThatIsNotItsOwn(t *testing.T) {
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: PostgresSecret("ventas"), Namespace: ns}}
	o := existingRes("ventas", v1alpha1.ResourcePostgres, 1, example())
	r, c := esoRec(t, true, o, foreign)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "not managed by Odyssey") {
		t.Errorf("status %+v", got.Status)
	}
}

func TestAnInvalidConnectionFailsWithoutWritingAnything(t *testing.T) {
	// A credential written in the spec (someone edited the CRD by hand).
	o := existingRes("ventas", v1alpha1.ResourcePostgres, 1, written(lit("PGHOST", "db.corp.example"), lit("PGPORT", "5432"),
		lit("PGDATABASE", "d"), lit("PGUSER", "u"), lit("PGPASSWORD", "hunter2")))
	r, c := esoRec(t, true, o)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceFailed || !strings.Contains(got.Status.Message, "credential") {
		t.Errorf("status %+v", got.Status)
	}
	var list corev1.SecretList
	if err := c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Errorf("wrote %v %v", list.Items, err)
	}
}

func TestDeletingAnExistingResourceRemovesOnlyItsConnection(t *testing.T) {
	ctx := context.Background()
	o := existingRes("ventas", v1alpha1.ResourcePostgres, 1, example())
	r, c := esoRec(t, true, o)
	got, _ := pass(t, r, c, o)
	got.Spec.Deleted, got.Spec.Generation = true, 2
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, got)
	if got != nil && got.Status.Phase != v1alpha1.ResourceDeleted && got.Status.Phase != v1alpha1.ResourceProvisioning {
		t.Fatalf("status %+v", got.Status)
	}
	for got != nil && got.Status.Phase != v1alpha1.ResourceDeleted {
		got, _ = pass(t, r, c, got)
	}
	es := externalsecret.New()
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: PostgresSecret("ventas")}, es); err == nil {
		t.Error("the ExternalSecret is gone (its Secret goes with it)")
	}
}

func TestSwitchingBetweenWrittenAndReferencedReplacesTheConnection(t *testing.T) {
	ctx := context.Background()
	o := existingRes("archivos", v1alpha1.ResourceObjectStorage, 1, written(lit("S3_BUCKET", "b"), lit("S3_REGION", "r")))
	r, c := esoRec(t, true, o)
	got, _ := pass(t, r, c, o)
	if got.Status.Phase != v1alpha1.ResourceReady {
		t.Fatalf("status %+v", got.Status)
	}
	// Version 2 reads a key from a store: the agent's Secret makes way.
	next := existingRes("archivos", v1alpha1.ResourceObjectStorage, 2, written(lit("S3_BUCKET", "b"), lit("S3_REGION", "r"),
		ref("AWS_ACCESS_KEY_ID", "vault", "s3", "id"), ref("AWS_SECRET_ACCESS_KEY", "vault", "s3", "secret")))
	got.Spec = next.Spec
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, got)
	if got.Status.Message != switchingMessage {
		t.Fatalf("status %+v", got.Status)
	}
	got, _ = pass(t, r, c, got)
	if got.Status.Message != syncingMessage {
		t.Fatalf("status %+v", got.Status)
	}
	getES(t, c, ObjectStorageSecret("archivos"))
	// And back: the ExternalSecret goes first.
	back := existingRes("archivos", v1alpha1.ResourceObjectStorage, 3, written(lit("S3_BUCKET", "b2"), lit("S3_REGION", "r")))
	got.Spec = back.Spec
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = pass(t, r, c, got)
	if got.Status.Message != switchingMessage {
		t.Fatalf("status %+v", got.Status)
	}
	got, _ = pass(t, r, c, got)
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ObjectStorageSecret("archivos")}, &sec); err != nil || string(sec.Data["bucket"]) != "b2" {
		t.Fatalf("secret %v %v (status %+v)", sec.Data, err, got.Status)
	}
}

func TestEveryKindAnExistingCanBeHasAConnectionOfItsContract(t *testing.T) {
	for _, k := range resourcekind.Kinds {
		c := resourcekind.Of(k)
		e, ok := kinds[v1alpha1.ResourceKind(k)]
		if !ok {
			t.Errorf("%s has no entry in the registry", k)
			continue
		}
		if !c.Existing {
			continue
		}
		if e.secret == nil || len(e.keys) != len(c.Variables) {
			t.Errorf("%s: an existing one needs its connection Secret with one key per variable", k)
		}
		for _, v := range c.Variables {
			if _, ok := e.derived[v.Name]; v.Derived && !ok {
				t.Errorf("%s: nothing builds %s", k, v.Name)
			}
		}
		if c.Builtin != (e.builtin != nil) {
			t.Errorf("%s: builtin %v in the contract, %v in the agent", k, c.Builtin, e.builtin != nil)
		}
	}
}

func example() resourcespec.Existing { return *resourcespec.ExamplePostgres("vault").Existing }
