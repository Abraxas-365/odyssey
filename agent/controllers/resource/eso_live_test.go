package resource

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/externalsecret"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestExistingConnectionOnRealExternalSecrets runs the existing engine
// against a real External Secrets Operator (ODYSSEY_TEST_ESO_KUBECONFIG: a
// throwaway cluster with the agent's CRDs and ESO installed; it creates one
// namespace and one ClusterSecretStore with the dev-only fake provider).
// What a fake client cannot tell: that ESO's webhook accepts the
// ExternalSecret as the agent writes it, that its template escapes a
// password that needs every escape and never runs a written value, that a
// missing key turns into the fixed failure, and that deleting the resource
// takes the Secret too.
func TestExistingConnectionOnRealExternalSecrets(t *testing.T) {
	path := os.Getenv("ODYSSEY_TEST_ESO_KUBECONFIG")
	if path == "" {
		t.Skip("set ODYSSEY_TEST_ESO_KUBECONFIG to a throwaway cluster with the External Secrets Operator")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, API: c}

	const password = `p@ss w/rd%+:?#{{x}}`
	stamp := time.Now().Format("150405")
	space, storeName := "odyssey-eso-"+stamp, "fake-"+stamp
	store := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "external-secrets.io/v1", "kind": "ClusterSecretStore",
		"metadata": map[string]any{"name": storeName},
		"spec": map[string]any{"provider": map[string]any{"fake": map[string]any{"data": []any{
			map[string]any{"key": "rds/reportes", "value": `{"username":"app@inedge","password":` + jsonString(password) + `}`},
		}}}},
	}}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: space}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, store); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		var o v1alpha1.OdysseyResource
		if c.Get(bg, client.ObjectKey{Namespace: space, Name: "reportes"}, &o) == nil {
			o.Finalizers = nil
			_ = c.Update(bg, &o)
		}
		_ = c.Delete(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: space}})
		_ = c.Delete(bg, store)
	})

	e := written(lit("PGHOST", "reportes.corp.example"), lit("PGPORT", "5432"), lit("PGDATABASE", `ventas {{ .v4 }}"`),
		ref("PGUSER", storeName, "rds/reportes", "username"), ref("PGPASSWORD", storeName, "rds/reportes", "password"),
		lit("PGSSLMODE", "require"))
	o := existingRes("reportes", v1alpha1.ResourcePostgres, 1, e)
	o.Namespace = space
	o.Labels = map[string]string{"odyssey.dev/environment": "env1"}
	if err := c.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	// until passes until the resource reports want for its generation. An
	// error is retried: ESO's webhook may still be getting its certificate.
	until := func(want v1alpha1.ResourcePhase, why string) *v1alpha1.OdysseyResource {
		t.Helper()
		for {
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)}); err != nil {
				t.Logf("%s: %v (retrying)", why, err)
			}
			var got v1alpha1.OdysseyResource
			if err := c.Get(ctx, client.ObjectKeyFromObject(o), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase == want && got.Status.ObservedGeneration == got.Spec.Generation {
				return &got
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: still %+v", why, got.Status)
			case <-time.After(2 * time.Second):
			}
		}
	}

	got := until(v1alpha1.ResourceReady, "ESO writes the connection")
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: space, Name: PostgresSecret("reportes")}, &sec); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"host": "reportes.corp.example", "port": "5432", "dbname": `ventas {{ .v4 }}"`,
		"user": "app@inedge", "password": password, "sslmode": "require",
	}
	for k, w := range want {
		if string(sec.Data[k]) != w {
			t.Errorf("%s = %q, want %q", k, sec.Data[k], w)
		}
	}
	u, err := url.Parse(string(sec.Data["uri"]))
	if err != nil {
		t.Fatalf("uri %q: %v", sec.Data["uri"], err)
	}
	pw, _ := u.User.Password()
	if u.Scheme != "postgresql" || u.User.Username() != "app@inedge" || pw != password || u.Host != "reportes.corp.example:5432" ||
		u.Path != `/ventas {{ .v4 }}"` || u.Query().Get("sslmode") != "require" {
		t.Errorf("uri %q reads back as %+v (password %q)", sec.Data["uri"], u, pw)
	}
	if owner := metav1.GetControllerOf(&sec); owner == nil || owner.Kind != externalsecret.GVK.Kind {
		t.Errorf("ESO owns the Secret: %+v", owner)
	}

	// The CRD keeps a resource the company's: no size, no built-in settings,
	// and never turned into one Odicea makes under the same name.
	for name, change := range map[string]func(*v1alpha1.OdysseyResource){
		"a size":            func(x *v1alpha1.OdysseyResource) { x.Spec.Storage = "5Gi" },
		"built-in settings": func(x *v1alpha1.OdysseyResource) { x.Spec.Postgres = &v1alpha1.PostgresSettings{Instances: 1} },
		"made by Odicea": func(x *v1alpha1.OdysseyResource) {
			x.Spec.Existing, x.Spec.Storage = nil, "5Gi"
		},
	} {
		x := got.DeepCopy()
		x.Spec.Generation++
		change(x)
		if err := c.Update(ctx, x); !apierrors.IsInvalid(err) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}

	// A key the store does not have: the fixed phrase, never ESO's words.
	got.Spec.Generation = 2
	got.Spec.Existing.Values[4].Ref.Key = "rds/no-existe"
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got = until(v1alpha1.ResourceFailed, "a missing key fails")
	if got.Status.Message != SyncFailedMessage {
		t.Errorf("message %q", got.Status.Message)
	}

	// Deleting the resource takes the ExternalSecret and, with it, the Secret.
	got.Spec.Generation, got.Spec.Deleted = 3, true
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(o)}); err != nil && !apierrors.IsConflict(err) {
			t.Fatal(err)
		}
		esGone := apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: space, Name: PostgresSecret("reportes")}, externalsecret.New()))
		secGone := apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: space, Name: PostgresSecret("reportes")}, &corev1.Secret{}))
		if esGone && secGone {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("not removed: ExternalSecret gone %v, Secret gone %v", esGone, secGone)
		case <-time.After(2 * time.Second):
		}
	}
}

// jsonString is s as a JSON string (the fake store keeps a JSON document).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
