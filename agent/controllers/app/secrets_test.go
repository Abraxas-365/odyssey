package app

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/agent/secretstore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func esoMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(externalSecretGVK, meta.RESTScopeNamespace)
	return m
}

func managedRef(name string, version int64) v1alpha1.SecretRefSpec {
	return v1alpha1.SecretRefSpec{Name: name, Source: v1alpha1.SecretManaged, Version: version}
}

func externalRef(name, store, key, property string) v1alpha1.SecretRefSpec {
	return v1alpha1.SecretRefSpec{Name: name, Source: v1alpha1.SecretExternal, Store: store, Key: key, Property: property}
}

func managedSecret(versions map[string]int64, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "portal-secrets", Namespace: "odyssey-env1",
		Labels: map[string]string{desired.LabelManagedBy: desired.ManagedBy, secretstore.LabelApp: "portal"},
	}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	secretstore.SetVersions(s, versions)
	return s
}

// deliveredAt records when each key of s was delivered.
func deliveredAt(s *corev1.Secret, at map[string]time.Time) *corev1.Secret {
	secretstore.SetDelivered(s, at)
	return s
}

// foreignSecret is a Secret of the name <app>-secrets that Odyssey did not make.
func foreignSecret(data map[string]string) *corev1.Secret {
	s := managedSecret(map[string]int64{}, data)
	s.Labels = nil
	delete(s.Annotations, secretstore.AnnotationVersions)
	return s
}

func externalTarget(store string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "portal-external-" + store, Namespace: "odyssey-env1"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// withSecrets is an app (revision rev) with the given references.
func withSecrets(rev int64, refs ...v1alpha1.SecretRefSpec) *v1alpha1.OdysseyApp {
	a := newApp(rev)
	a.Spec.Secrets = refs
	return a
}

// secretsRec reconciles with the clock at *at and the given mapper deciding
// whether the External Secrets Operator's kind exists.
func secretsRec(t *testing.T, mapper meta.RESTMapper, at *time.Time, a *v1alpha1.OdysseyApp, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(append(objs, a)...).WithStatusSubresource(&v1alpha1.OdysseyApp{}).Build()
	return &Reconciler{Client: c, API: c, Config: cfg, HealthGrace: time.Minute, ExternalSecrets: mapper, Now: func() time.Time { return *at }}, c
}

func deploymentOf(t *testing.T, c client.Client, a *v1alpha1.OdysseyApp) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), &d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return &d
}

func envOf(d *appsv1.Deployment) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e
	}
	return out
}

func TestEnvReadsSecretsFromSecrets(t *testing.T) {
	a := withSecrets(1,
		managedRef("DATABASE_URL", 2),
		externalRef("API_KEY", "vault", "prod/api", "key"),
		externalRef("AWS_TOKEN", "aws", "tok", ""),
		managedRef("A", 1)) // replaces the plain A=1
	got := env(a)
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "B,A,API_KEY,AWS_TOKEN,DATABASE_URL,PORT" {
		t.Fatalf("env order: %v", names)
	}
	by := map[string]corev1.EnvVar{}
	for _, e := range got {
		by[e.Name] = e
	}
	for name, want := range map[string]string{"A": "portal-secrets", "DATABASE_URL": "portal-secrets", "API_KEY": "portal-external-vault", "AWS_TOKEN": "portal-external-aws"} {
		ref := by[name].ValueFrom.SecretKeyRef
		if by[name].Value != "" || ref == nil || ref.Name != want || ref.Key != name {
			t.Errorf("%s: %+v, want a reference to %s", name, by[name], want)
		}
	}
	if by["B"].Value != "2" || by["PORT"].Value != "8080" {
		t.Errorf("plain variables: %+v %+v", by["B"], by["PORT"])
	}

	// A secret named PORT is the app's PORT: no plain one is added.
	p := withSecrets(1, managedRef("PORT", 1))
	n := 0
	for _, e := range env(p) {
		if e.Name == "PORT" {
			n++
			if e.ValueFrom == nil {
				t.Errorf("PORT must come from the secret: %+v", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("PORT appears %d times", n)
	}
}

func TestSecretsRollThePods(t *testing.T) {
	d1, d2, d3, none := &appsv1.Deployment{}, &appsv1.Deployment{}, &appsv1.Deployment{}, &appsv1.Deployment{}
	mutateDeployment(d1, withSecrets(1, managedRef("A", 1), externalRef("B", "vault", "k", "")), cfg)
	mutateDeployment(d2, withSecrets(1, externalRef("B", "vault", "k", ""), managedRef("A", 1)), cfg) // same set, other order
	mutateDeployment(d3, withSecrets(1, managedRef("A", 2), externalRef("B", "vault", "k", "")), cfg)
	mutateDeployment(none, newApp(1), cfg)
	h1, h2, h3 := d1.Spec.Template.Annotations[AnnotationSecrets], d2.Spec.Template.Annotations[AnnotationSecrets], d3.Spec.Template.Annotations[AnnotationSecrets]
	if h1 == "" || h1 != h2 || h1 == h3 {
		t.Errorf("hashes: %q %q %q", h1, h2, h3)
	}
	if _, ok := none.Spec.Template.Annotations[AnnotationSecrets]; ok {
		t.Errorf("an app without secrets keeps its template: %v", none.Spec.Template.Annotations)
	}
	for _, change := range []func(*v1alpha1.SecretRefSpec){
		func(s *v1alpha1.SecretRefSpec) { s.Store = "other" },
		func(s *v1alpha1.SecretRefSpec) { s.Key = "other" },
		func(s *v1alpha1.SecretRefSpec) { s.Property = "p" },
	} {
		a := withSecrets(1, externalRef("B", "vault", "k", ""))
		before := secretsHash(a)
		change(&a.Spec.Secrets[0])
		if secretsHash(a) == before {
			t.Errorf("a changed reference must change the hash: %+v", a.Spec.Secrets)
		}
	}
}

func TestExternalSecretObjects(t *testing.T) {
	a := withSecrets(1, externalRef("API_KEY", "vault", "prod/api", "key"), externalRef("TOKEN", "vault", "tok", ""), externalRef("OTHER", "aws", "o", ""))
	stores := externalStores(a)
	if len(stores) != 2 || len(stores["vault"]) != 2 || len(stores["aws"]) != 1 {
		t.Fatalf("stores: %+v", stores)
	}
	e := externalSecret(a, "vault", stores["vault"])
	if e.GetName() != "portal-external-vault" || e.GetNamespace() != "odyssey-env1" || e.GetAPIVersion() != "external-secrets.io/v1" || e.GetKind() != "ExternalSecret" {
		t.Errorf("identity: %v", e.Object)
	}
	spec, _, _ := unstructured.NestedMap(e.Object, "spec")
	if spec["refreshInterval"] != "1h" {
		t.Errorf("refreshInterval: %v", spec["refreshInterval"])
	}
	ref, _, _ := unstructured.NestedStringMap(e.Object, "spec", "secretStoreRef")
	if ref["kind"] != "ClusterSecretStore" || ref["name"] != "vault" {
		t.Errorf("secretStoreRef: %v", ref)
	}
	target, _, _ := unstructured.NestedStringMap(e.Object, "spec", "target")
	if target["name"] != "portal-external-vault" || target["creationPolicy"] != "Owner" {
		t.Errorf("target: %v", target)
	}
	data, _, _ := unstructured.NestedSlice(e.Object, "spec", "data")
	if len(data) != 2 {
		t.Fatalf("data: %v", data)
	}
	first, second := data[0].(map[string]any), data[1].(map[string]any)
	if first["secretKey"] != "API_KEY" || first["remoteRef"].(map[string]any)["key"] != "prod/api" || first["remoteRef"].(map[string]any)["property"] != "key" {
		t.Errorf("first: %v", first)
	}
	if _, has := second["remoteRef"].(map[string]any)["property"]; second["secretKey"] != "TOKEN" || has {
		t.Errorf("an empty property is omitted: %v", second)
	}
}

func TestManagedSecretGatesTheRollout(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(1, managedRef("DATABASE_URL", 2))
	r, c := secretsRec(t, esoMapper(), &at, a, managedSecret(map[string]int64{"DATABASE_URL": 1}, map[string]string{"DATABASE_URL": "old"}))

	got, res := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseProgressing || got.Status.Message != "Esperando el secreto DATABASE_URL" || res.RequeueAfter != progressEvery {
		t.Fatalf("waiting: %+v %+v", got.Status, res)
	}
	if deploymentOf(t, c, a) != nil {
		t.Fatal("no Deployment before its secrets are there")
	}
	if got.Status.ObservedRevision != 1 || got.Status.Reason != "" {
		t.Errorf("status: %+v", got.Status)
	}

	// The first moment is recorded on the object: a restart does not restart the window.
	if !strings.HasSuffix(got.Annotations[annotationSecretsWaiting], "/"+at.UTC().Format(time.RFC3339)) {
		t.Fatalf("waiting annotation: %v", got.Annotations)
	}
	at = at.Add(secretWait - time.Second)
	r2 := &Reconciler{Client: c, API: c, Config: cfg, HealthGrace: time.Minute, ExternalSecrets: esoMapper(), Now: func() time.Time { return at }} // "restarted"
	if got, _ = runWith(t, r2, c, a); got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("still within the window: %+v", got.Status)
	}
	at = at.Add(2 * time.Second)
	got, res = runWith(t, r2, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || got.Status.Message != "Falta el secreto DATABASE_URL: cárgalo en la consola" || res.RequeueAfter != 0 {
		t.Fatalf("after the window: %+v %+v", got.Status, res)
	}
	if deploymentOf(t, c, a) != nil {
		t.Fatal("still no Deployment")
	}

	// The value arrives late: the failed revision stays failed (its verdict
	// is final; a new revision, which the control plane makes after the
	// person loads the value, applies).
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-secrets"}, &sec); err != nil {
		t.Fatal(err)
	}
	sec.Data["DATABASE_URL"] = []byte("new")
	secretstore.SetVersions(&sec, map[string]int64{"DATABASE_URL": 2})
	if err := c.Update(ctx, &sec); err != nil {
		t.Fatal(err)
	}
	if got, _ = runWith(t, r2, c, a); got.Status.Phase != v1alpha1.PhaseFailed || deploymentOf(t, c, a) != nil {
		t.Fatalf("a failed revision stays failed: %+v", got.Status)
	}
	got.Spec.Revision = 2
	got.Spec.Secrets = []v1alpha1.SecretRefSpec{managedRef("DATABASE_URL", 2)}
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = runWith(t, r2, c, a)
	d := deploymentOf(t, c, a)
	if d == nil {
		t.Fatalf("with the secret there, the next revision is applied: %+v", got.Status)
	}
	if ref := envOf(d)["DATABASE_URL"].ValueFrom.SecretKeyRef; ref.Name != "portal-secrets" || ref.Key != "DATABASE_URL" {
		t.Errorf("env: %+v", envOf(d)["DATABASE_URL"])
	}
	if _, ok := got.Annotations[annotationSecretsWaiting]; ok {
		t.Errorf("the waiting mark is cleared once the secrets are there: %v", got.Annotations)
	}
}

func TestMissingSecretLeavesTheRunningVersionAlone(t *testing.T) {
	ctx := context.Background()
	at := now
	running := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: "odyssey-env1", Generation: 2}}
	mutateDeployment(running, newApp(1), cfg)
	a := withSecrets(2, managedRef("TOKEN", 1))
	a.Status = v1alpha1.OdysseyAppStatus{ObservedRevision: 1, Phase: v1alpha1.PhaseReady}
	r, c := secretsRec(t, esoMapper(), &at, a, running)

	got, _ := runWith(t, r, c, a)
	if got.Status.ObservedRevision != 2 || got.Status.Phase != v1alpha1.PhaseProgressing || !strings.Contains(got.Status.Message, "TOKEN") {
		t.Fatalf("status: %+v", got.Status)
	}
	var d appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &d); err != nil {
		t.Fatal(err)
	}
	if revisionOf(&d) != 1 || d.Spec.Template.Annotations[AnnotationSecrets] != "" || len(d.Spec.Template.Spec.Containers[0].Env) != 3 {
		t.Errorf("the running Deployment must not change while a secret is missing: %+v", d.Spec.Template)
	}
}

func TestSecretsGateNewVersionsAndKeys(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("A", 2), managedRef("B", 1))
	for name, c := range map[string]struct {
		secret  *corev1.Secret
		waiting bool
	}{
		"no Secret":           {nil, true},
		"an older version":    {managedSecret(map[string]int64{"A": 1, "B": 1}, map[string]string{"A": "x", "B": "y"}), true},
		"key without value":   {managedSecret(map[string]int64{"A": 2, "B": 1}, map[string]string{"A": "x"}), true},
		"no versions at all":  {managedSecret(nil, map[string]string{"A": "x", "B": "y"}), true},
		"exact versions":      {managedSecret(map[string]int64{"A": 2, "B": 1}, map[string]string{"A": "x", "B": "y"}), false},
		"newer than wanted":   {managedSecret(map[string]int64{"A": 5, "B": 3}, map[string]string{"A": "x", "B": "y"}), false},
		"extra keys are fine": {managedSecret(map[string]int64{"A": 2, "B": 1, "C": 1}, map[string]string{"A": "x", "B": "y", "C": "z"}), false},
	} {
		app := a.DeepCopy()
		var objs []client.Object
		if c.secret != nil {
			objs = append(objs, c.secret)
		}
		r, cl := secretsRec(t, esoMapper(), &at, app, objs...)
		got, _ := runWith(t, r, cl, app)
		if waiting := got.Status.Phase == v1alpha1.PhaseProgressing && strings.HasPrefix(got.Status.Message, "Esperando el secreto"); waiting != c.waiting {
			t.Errorf("%s: %+v", name, got.Status)
		}
		if (deploymentOf(t, cl, app) == nil) != c.waiting {
			t.Errorf("%s: deployment present = %v", name, deploymentOf(t, cl, app) != nil)
		}
	}
}

func TestExternalSecretsAreCreatedAndGated(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(1, externalRef("API_KEY", "vault", "prod/api", "key"), externalRef("TOKEN", "aws", "tok", ""))
	r, c := secretsRec(t, esoMapper(), &at, a)

	got, res := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseProgressing || got.Status.Message != "Esperando el secreto API_KEY" || res.RequeueAfter == 0 {
		t.Fatalf("waiting for ESO: %+v %+v", got.Status, res)
	}
	for _, store := range []string{"vault", "aws"} {
		e := NewExternalSecret()
		if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-external-" + store}, e); err != nil {
			t.Fatalf("%s: %v", store, err)
		}
		if len(e.GetOwnerReferences()) != 1 || e.GetOwnerReferences()[0].Name != "portal" || e.GetLabels()[LabelManagedBy] != ManagedBy || e.GetLabels()[LabelApp] != "portal" {
			t.Errorf("%s: owner %v labels %v", store, e.GetOwnerReferences(), e.GetLabels())
		}
	}
	if deploymentOf(t, c, a) != nil {
		t.Fatal("no Deployment before the external secrets are read")
	}

	// ESO makes one target Secret: the other is still awaited.
	if err := c.Create(ctx, externalTarget("vault", map[string]string{"API_KEY": "v"})); err != nil {
		t.Fatal(err)
	}
	if got, _ = runWith(t, r, c, a); got.Status.Message != "Esperando el secreto TOKEN" {
		t.Fatalf("second store: %+v", got.Status)
	}
	// ESO made it but without the key: not enough.
	if err := c.Create(ctx, externalTarget("aws", map[string]string{"OTHER": "v"})); err != nil {
		t.Fatal(err)
	}
	if got, _ = runWith(t, r, c, a); got.Status.Message != "Esperando el secreto TOKEN" {
		t.Fatalf("missing key: %+v", got.Status)
	}
	at = at.Add(secretWait)
	got, _ = runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig ||
		got.Status.Message != "No se pudo leer el secreto externo TOKEN (revisa la referencia en el almacén aws)" {
		t.Fatalf("timeout: %+v", got.Status)
	}
	if deploymentOf(t, c, a) != nil {
		t.Fatal("no Deployment after failing either")
	}

	// A new revision whose secrets are readable is applied, each variable
	// read from its store's Secret.
	var aws corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-external-aws"}, &aws); err != nil {
		t.Fatal(err)
	}
	aws.Data["TOKEN"] = []byte("t")
	if err := c.Update(ctx, &aws); err != nil {
		t.Fatal(err)
	}
	got.Spec.Revision = 2
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	runWith(t, r, c, a)
	d := deploymentOf(t, c, a)
	if d == nil {
		t.Fatal("applied once readable")
	}
	e := envOf(d)
	if e["API_KEY"].ValueFrom.SecretKeyRef.Name != "portal-external-vault" || e["TOKEN"].ValueFrom.SecretKeyRef.Name != "portal-external-aws" {
		t.Errorf("env: %+v", e)
	}
}

func TestExternalSecretIsUpdatedWhenTheReferenceChanges(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(1, externalRef("API_KEY", "vault", "prod/api", ""))
	r, c := secretsRec(t, esoMapper(), &at, a)
	runWith(t, r, c, a)

	var cur v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Revision = 2
	cur.Spec.Secrets = []v1alpha1.SecretRefSpec{externalRef("API_KEY", "vault", "prod/other", "k")}
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	runWith(t, r, c, a)
	e := NewExternalSecret()
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-external-vault"}, e); err != nil {
		t.Fatal(err)
	}
	data, _, _ := unstructured.NestedSlice(e.Object, "spec", "data")
	remote := data[0].(map[string]any)["remoteRef"].(map[string]any)
	if remote["key"] != "prod/other" || remote["property"] != "k" {
		t.Errorf("remoteRef: %v", remote)
	}
}

func TestExternalSecretNeedsTheOperator(t *testing.T) {
	at := now
	a := withSecrets(1, externalRef("API_KEY", "vault", "prod/api", ""))
	r, c := secretsRec(t, meta.NewDefaultRESTMapper(nil), &at, a)
	got, res := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || got.Status.Message != NoExternalSecretsMessage || res.RequeueAfter != 0 {
		t.Fatalf("status: %+v %+v", got.Status, res)
	}
	if deploymentOf(t, c, a) != nil {
		t.Error("no Deployment without the operator")
	}
	if !strings.Contains(NoExternalSecretsMessage, "External Secrets Operator") {
		t.Error("message")
	}

	// Managed secrets need no operator.
	m := withSecrets(1, managedRef("A", 1))
	r, c = secretsRec(t, meta.NewDefaultRESTMapper(nil), &at, m, managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "x"}))
	if runWith(t, r, c, m); deploymentOf(t, c, m) == nil {
		t.Error("a managed secret is applied without the operator")
	}
}

// oldExternalSecret is an ExternalSecret an earlier revision of portal made.
func oldExternalSecret(name, app string) *unstructured.Unstructured {
	e := NewExternalSecret()
	e.SetName(name)
	e.SetNamespace("odyssey-env1")
	e.SetLabels(map[string]string{LabelManagedBy: ManagedBy, LabelApp: app})
	if app == "portal" {
		e.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "odyssey.dev/v1alpha1", Kind: "OdysseyApp", Name: "portal", Controller: ptr(true)}})
	}
	return e
}

func TestReadyRevisionPrunesWhatItNoLongerLists(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(2, managedRef("KEEP", 1))
	stale := oldExternalSecret("portal-external-old", "portal")
	r, c := secretsRec(t, esoMapper(), &at, a,
		managedSecret(map[string]int64{"KEEP": 1, "DROP": 3}, map[string]string{"KEEP": "k", "DROP": "d"}), stale)
	// The owner reference needs the app's UID to count as ours.
	var cur v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	stale.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "odyssey.dev/v1alpha1", Kind: "OdysseyApp", Name: "portal", UID: cur.UID, Controller: ptr(true)}})
	if err := c.Update(ctx, stale); err != nil {
		t.Fatal(err)
	}

	// The rollout is not done: nothing is pruned yet (the previous version's
	// pods may restart on the old references).
	got, _ := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("status: %+v", got.Status)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-secrets"}, &s); err != nil || len(s.Data) != 2 {
		t.Fatalf("not pruned while rolling out: %v %v", s.Data, err)
	}

	// The rollout finishes.
	d := deploymentOf(t, c, a)
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2, ReadyReplicas: 2}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portal-a", "portal-b"} {
		p := pod("2", running(time.Minute, true))
		p.Name, p.Namespace = name, a.Namespace
		if err := c.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("ready: %+v", got.Status)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: "portal-secrets"}, &s); err != nil {
		t.Fatal(err)
	}
	if _, has := s.Data["DROP"]; has || string(s.Data["KEEP"]) != "k" {
		t.Errorf("data: %v", s.Data)
	}
	if v := secretstore.Versions(&s); len(v) != 1 || v["KEEP"] != 1 {
		t.Errorf("versions: %v", v)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(stale), NewExternalSecret()); !apierrors.IsNotFound(err) {
		t.Errorf("an ExternalSecret the app no longer needs is deleted: %v", err)
	}
}

func TestPruneKeepsExternalSecretsOfOtherApps(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(1, managedRef("A", 1))
	other := oldExternalSecret("other-external-vault", "other")
	r, c := secretsRec(t, esoMapper(), &at, a, managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "x"}), other)
	if _, err := r.pruneSecrets(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(other), NewExternalSecret()); err != nil {
		t.Errorf("another app's ExternalSecret must stay: %v", err)
	}
}

// ready runs r until a's revision is Ready: it applies the Deployment, then
// reports its pods up.
func ready(t *testing.T, r *Reconciler, c client.Client, a *v1alpha1.OdysseyApp) (*v1alpha1.OdysseyApp, ctrl.Result) {
	t.Helper()
	ctx := context.Background()
	if got, _ := runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseProgressing {
		t.Fatalf("first pass: %+v", got.Status)
	}
	d := deploymentOf(t, c, a)
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2, ReadyReplicas: 2}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portal-a", "portal-b"} {
		p := pod(strconv.FormatInt(a.Spec.Revision, 10), running(time.Minute, true))
		p.Name, p.Namespace = name, a.Namespace
		if err := c.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	got, res := runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("ready: %+v", got.Status)
	}
	return got, res
}

func secretOf(t *testing.T, c client.Client) (*corev1.Secret, error) {
	t.Helper()
	var s corev1.Secret
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "odyssey-env1", Name: "portal-secrets"}, &s)
	return &s, err
}

func TestPruneKeepsAFreshlyDeliveredKeyUntilTheGrace(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("KEEP", 1))
	// NEW was delivered a minute ago for a revision still queued; OLD was
	// delivered long ago; GONE predates the delivery times.
	sec := deliveredAt(
		managedSecret(map[string]int64{"KEEP": 1, "NEW": 1, "OLD": 1, "GONE": 1}, map[string]string{"KEEP": "k", "NEW": "n", "OLD": "o", "GONE": "g"}),
		map[string]time.Time{"KEEP": at.Add(-48 * time.Hour), "NEW": at.Add(-time.Minute), "OLD": at.Add(-2 * secretGrace)})
	r, c := secretsRec(t, esoMapper(), &at, a, sec)

	_, res := ready(t, r, c, a)
	s, err := secretOf(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Data["NEW"]) != "n" || secretstore.Versions(s)["NEW"] != 1 {
		t.Fatalf("a key delivered within the grace survives: %v %v", s.Data, s.Annotations)
	}
	if _, has := s.Data["OLD"]; has {
		t.Errorf("an old key the spec no longer lists is pruned promptly: %v", s.Data)
	}
	if _, has := s.Data["GONE"]; has {
		t.Errorf("a key with no recorded time counts as old: %v", s.Data)
	}
	if v := secretstore.Versions(s); len(v) != 2 || v["KEEP"] != 1 || v["NEW"] != 1 {
		t.Errorf("versions follow the data: %v", v)
	}
	if d := secretstore.Delivered(s); len(d) != 2 || !d["NEW"].Equal(at.Add(-time.Minute)) {
		t.Errorf("delivery times follow the data: %v", d)
	}
	if want := secretGrace - time.Minute; res.RequeueAfter != want {
		t.Errorf("a kept key must be looked at again when its grace ends: %v, want %v", res.RequeueAfter, want)
	}

	// Another pass inside the grace changes nothing.
	at = at.Add(30 * time.Minute)
	if _, res = runWith(t, r, c, a); res.RequeueAfter != secretGrace-31*time.Minute {
		t.Errorf("requeue: %v", res.RequeueAfter)
	}
	if s, _ = secretOf(t, c); string(s.Data["NEW"]) != "n" {
		t.Fatalf("still inside the grace: %v", s.Data)
	}

	// Past the grace the next pass prunes it.
	at = at.Add(31 * time.Minute)
	_, res = runWith(t, r, c, a)
	if s, err = secretOf(t, c); err != nil {
		t.Fatal(err)
	}
	if _, has := s.Data["NEW"]; has || string(s.Data["KEEP"]) != "k" {
		t.Errorf("after the grace: %v", s.Data)
	}
	if v := secretstore.Versions(s); len(v) != 1 {
		t.Errorf("versions: %v", v)
	}
	if d := secretstore.Delivered(s); len(d) != 1 {
		t.Errorf("delivery times: %v", d)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("nothing left to wait for: %v", res.RequeueAfter)
	}
}

func TestPruneTreatsAFutureDeliveryTimeAsOld(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("KEEP", 1))
	sec := deliveredAt(managedSecret(map[string]int64{"KEEP": 1, "SKEW": 1}, map[string]string{"KEEP": "k", "SKEW": "s"}),
		map[string]time.Time{"SKEW": at.Add(24 * time.Hour)})
	r, c := secretsRec(t, esoMapper(), &at, a, sec)
	ready(t, r, c, a)
	if s, _ := secretOf(t, c); len(s.Data) != 1 {
		t.Errorf("a time in the future must not keep a value for ever: %v", s.Data)
	}
}

func TestPruneDeletesTheSecretWhenNoKeyIsLeft(t *testing.T) {
	at := now
	a := withSecrets(1) // no secrets any more
	sec := deliveredAt(managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "a"}), map[string]time.Time{"A": at.Add(-2 * secretGrace)})
	r, c := secretsRec(t, esoMapper(), &at, a, sec)
	ready(t, r, c, a)
	if _, err := secretOf(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("an emptied Secret is deleted: %v", err)
	}

	// A key kept for its grace keeps the Secret.
	b := withSecrets(1)
	sec = deliveredAt(managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "a"}), map[string]time.Time{"A": at.Add(-time.Minute)})
	r, c = secretsRec(t, esoMapper(), &at, b, sec)
	ready(t, r, c, b)
	if s, err := secretOf(t, c); err != nil || string(s.Data["A"]) != "a" {
		t.Fatalf("a fresh key keeps the Secret: %v %v", s, err)
	}
}

func TestPruneLeavesAForeignSecretAlone(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("KEEP", 1))
	foreign := foreignSecret(map[string]string{"KEEP": "k", "OTHER": "o"})
	r, c := secretsRec(t, esoMapper(), &at, a, foreign)
	// The spec wants a managed secret, so the app fails before anything is
	// applied; the prune is exercised directly.
	if wait, err := r.pruneManaged(context.Background(), a); err != nil || wait != 0 {
		t.Fatalf("prune: %v %v", wait, err)
	}
	s, err := secretOf(t, c)
	if err != nil || len(s.Data) != 2 || len(s.Annotations) != 0 {
		t.Fatalf("a Secret Odyssey does not manage is not pruned: %v %v", s, err)
	}

	// Nor deleted when no key would be left.
	empty := withSecrets(1)
	r, c = secretsRec(t, esoMapper(), &at, empty, foreignSecret(map[string]string{"OTHER": "o"}))
	if _, err := r.pruneManaged(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	if s, err = secretOf(t, c); err != nil || len(s.Data) != 1 {
		t.Fatalf("a foreign Secret is never deleted: %v %v", s, err)
	}
}

func TestAForeignSecretFailsAnAppThatNeedsManagedSecrets(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("KEEP", 1))
	r, c := secretsRec(t, esoMapper(), &at, a, foreignSecret(map[string]string{"KEEP": "k"}))
	got, res := runWith(t, r, c, a)
	want := "a Secret named portal-secrets already exists in odyssey-env1 and is not managed by Odyssey"
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Reason != v1alpha1.ReasonConfig || got.Status.Message != want || res.RequeueAfter != 0 {
		t.Fatalf("status: %+v %+v", got.Status, res)
	}
	if deploymentOf(t, c, a) != nil {
		t.Error("nothing is applied behind a Secret that is not ours")
	}
	if s, _ := secretOf(t, c); len(s.OwnerReferences) != 0 || s.Labels != nil {
		t.Errorf("a foreign Secret is not adopted nor labelled: %+v", s.ObjectMeta)
	}

	// An app that lists no managed secret does not care about it.
	b := withSecrets(1, externalRef("API_KEY", "vault", "k", ""))
	r, c = secretsRec(t, esoMapper(), &at, b, foreignSecret(map[string]string{"X": "x"}), externalTarget("vault", map[string]string{"API_KEY": "v"}))
	if got, _ = runWith(t, r, c, b); got.Status.Phase == v1alpha1.PhaseFailed {
		t.Errorf("an app without managed secrets is not failed by it: %+v", got.Status)
	}
	if s, _ := secretOf(t, c); len(s.OwnerReferences) != 0 {
		t.Errorf("not adopted: %+v", s.OwnerReferences)
	}
}

func TestAdoptManagedAddsAPlainOwnerReferenceOnce(t *testing.T) {
	ctx := context.Background()
	at := now
	a := withSecrets(1, managedRef("A", 1))
	r, c := secretsRec(t, esoMapper(), &at, a, managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "a"}))
	var cur v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	// The fake client gives no UID: make one, as the API server would.
	cur.UID = "uid-1"
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}

	// The Secret was written before the OdysseyApp could own it (or before it
	// existed): the first pass adopts it.
	if err := r.adoptManaged(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	s, err := secretOf(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.OwnerReferences) != 1 {
		t.Fatalf("owner references: %+v", s.OwnerReferences)
	}
	o := s.OwnerReferences[0]
	if o.Kind != "OdysseyApp" || o.Name != "portal" || o.UID != "uid-1" || o.APIVersion != v1alpha1.GroupVersion.String() {
		t.Errorf("owner: %+v", o)
	}
	if o.Controller != nil && *o.Controller {
		t.Errorf("a controller reference would block the owner's deletion: %+v", o)
	}
	if o.BlockOwnerDeletion != nil && *o.BlockOwnerDeletion {
		t.Errorf("must not block the owner's deletion: %+v", o)
	}
	if string(s.Data["A"]) != "a" || secretstore.Versions(s)["A"] != 1 {
		t.Errorf("adopting keeps the values: %v %v", s.Data, s.Annotations)
	}

	// Idempotent: again changes nothing (not even the resource version).
	rv := s.ResourceVersion
	for range 2 {
		if err := r.adoptManaged(ctx, &cur); err != nil {
			t.Fatal(err)
		}
	}
	if s, _ = secretOf(t, c); len(s.OwnerReferences) != 1 || s.ResourceVersion != rv {
		t.Errorf("adopting twice must not write: %+v rv %s -> %s", s.OwnerReferences, rv, s.ResourceVersion)
	}

	// No Secret yet is not an error.
	r2, _ := secretsRec(t, esoMapper(), &at, withSecrets(1))
	if err := r2.adoptManaged(ctx, a); err != nil {
		t.Errorf("no Secret: %v", err)
	}
}

func TestFirstReconcileAdoptsASecretWrittenBeforeTheApp(t *testing.T) {
	at := now
	a := withSecrets(1, managedRef("A", 1))
	r, c := secretsRec(t, esoMapper(), &at, a, managedSecret(map[string]int64{"A": 1}, map[string]string{"A": "a"}))
	got, _ := runWith(t, r, c, a)
	s, err := secretOf(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].Name != "portal" || s.OwnerReferences[0].UID != got.UID {
		t.Fatalf("the first pass adopts the Secret: %+v", s.OwnerReferences)
	}
	if o := s.OwnerReferences[0]; o.Controller != nil && *o.Controller {
		t.Errorf("not a controller reference: %+v", o)
	}
	if deploymentOf(t, c, a) == nil {
		t.Errorf("adopting does not hold the rollout: %+v", got.Status)
	}
	// A second pass leaves it as it is.
	rv := s.ResourceVersion
	runWith(t, r, c, a)
	if s, _ = secretOf(t, c); s.ResourceVersion != rv || len(s.OwnerReferences) != 1 {
		t.Errorf("second pass: %+v", s.ObjectMeta)
	}
}
