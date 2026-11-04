package secretstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const cluster = "cluster-1"

var org, proj, env = uuid.NewString(), uuid.NewString(), uuid.NewString()

func newWriter(t *testing.T, objs ...client.Object) (*Writer, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &Writer{Client: c}, c
}

func key(t *testing.T) secretbox.Key {
	t.Helper()
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// delivery seals value for k as the control plane would.
func delivery(t *testing.T, k secretbox.Key, name string, version int64, value string) *fleetv1.Secret {
	t.Helper()
	sealed, err := secretbox.Seal(k.Public(), secretbox.Target{Cluster: cluster, Environment: env, App: "portal", Name: name, Version: version}, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return &fleetv1.Secret{DeliveryId: "d1", OrganizationId: org, ProjectId: proj, EnvironmentId: env, App: "portal",
		Name: name, Version: version, KeyId: k.ID(), Sealed: sealed}
}

func stored(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: desired.Namespace(env), Name: "portal-secrets"}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestReceiveCreatesTheNamespaceAndTheSecret(t *testing.T) {
	ctx := context.Background()
	w, c := newWriter(t)
	k := key(t)
	if err := w.Receive(ctx, k, cluster, delivery(t, k, "DATABASE_URL", 1, "postgres://x")); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: desired.Namespace(env)}, &ns); err != nil {
		t.Fatalf("the namespace is created like an app's: %v", err)
	}
	if ns.Labels[desired.LabelOrganization] != org || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("namespace labels: %v", ns.Labels)
	}
	s := stored(t, c)
	if string(s.Data["DATABASE_URL"]) != "postgres://x" || Versions(s)["DATABASE_URL"] != 1 {
		t.Errorf("secret: %v %v", s.Data, s.Annotations)
	}
	if s.Labels["app.kubernetes.io/managed-by"] != "odyssey" || s.Labels["odyssey.dev/app"] != "portal" {
		t.Errorf("labels: %v", s.Labels)
	}
	if s.Annotations[AnnotationVersions] != `{"DATABASE_URL":1}` {
		t.Errorf("versions annotation: %q", s.Annotations[AnnotationVersions])
	}
}

func TestReceiveKeepsOtherKeysAndOnlyTakesNewerVersions(t *testing.T) {
	ctx := context.Background()
	w, c := newWriter(t)
	k := key(t)
	for _, d := range []struct {
		name    string
		version int64
		value   string
	}{{"A", 1, "a1"}, {"B", 1, "b1"}, {"A", 2, "a2"}} {
		if err := w.Receive(ctx, k, cluster, delivery(t, k, d.name, d.version, d.value)); err != nil {
			t.Fatal(err)
		}
	}
	s := stored(t, c)
	if string(s.Data["A"]) != "a2" || string(s.Data["B"]) != "b1" || Versions(s)["A"] != 2 || Versions(s)["B"] != 1 {
		t.Fatalf("secret: %v %v", s.Data, s.Annotations)
	}

	// The same version again, and an older one arriving late: success, nothing changes.
	if err := w.Receive(ctx, k, cluster, delivery(t, k, "A", 2, "other")); err != nil {
		t.Fatalf("same version: %v", err)
	}
	if err := w.Receive(ctx, k, cluster, delivery(t, k, "A", 1, "old")); err != nil {
		t.Fatalf("older version: %v", err)
	}
	s = stored(t, c)
	if string(s.Data["A"]) != "a2" || Versions(s)["A"] != 2 {
		t.Errorf("a same or older version must not replace the value: %v %v", s.Data, s.Annotations)
	}
}

func TestReceiveFillsAKeyThatLostItsValue(t *testing.T) {
	// The annotation says version 3 but the key is not there (someone edited
	// the Secret): the value is written again, not trusted to be there.
	ctx := context.Background()
	w, c := newWriter(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: desired.Namespace(env), Name: "portal-secrets",
			Labels:      map[string]string{desired.LabelManagedBy: desired.ManagedBy},
			Annotations: map[string]string{AnnotationVersions: `{"A":3}`}},
		Data: map[string][]byte{"OTHER": []byte("kept")},
	})
	k := key(t)
	if err := w.Receive(ctx, k, cluster, delivery(t, k, "A", 3, "back")); err != nil {
		t.Fatal(err)
	}
	if s := stored(t, c); string(s.Data["A"]) != "back" || string(s.Data["OTHER"]) != "kept" {
		t.Errorf("secret: %v", s.Data)
	}
}

func TestReceiveRefusesWhatItCannotOpen(t *testing.T) {
	ctx := context.Background()
	k := key(t)
	good := delivery(t, k, "A", 1, "secret-value")

	other := key(t)
	wrongKey := delivery(t, other, "A", 1, "secret-value") // sealed to a key that is not this cluster's
	wrongKey.KeyId = other.ID()
	renamed := delivery(t, k, "A", 1, "secret-value")
	renamed.Name = "B" // sealed for A
	bumped := delivery(t, k, "A", 1, "secret-value")
	bumped.Version = 2
	moved := delivery(t, k, "A", 1, "secret-value")
	moved.EnvironmentId = uuid.NewString()
	redirected := delivery(t, k, "A", 1, "secret-value")
	redirected.App = "other" // sealed for portal: the control plane cannot redirect it
	tampered := delivery(t, k, "A", 1, "secret-value")
	tampered.Sealed[len(tampered.Sealed)-1] ^= 1
	liar := delivery(t, other, "A", 1, "secret-value")
	liar.KeyId = k.ID() // claims this cluster's key id but is sealed to another key

	for name, c := range map[string]struct {
		s    *fleetv1.Secret
		want string
	}{
		"another cluster key":  {wrongKey, "sealed for another cluster key"},
		"another name":         {renamed, "cannot open"},
		"another version":      {bumped, "cannot open"},
		"another environment":  {moved, "cannot open"},
		"another app":          {redirected, "cannot open"},
		"tampered bytes":       {tampered, "cannot open"},
		"key id of this key":   {liar, "cannot open"},
		"no ids":               {&fleetv1.Secret{Name: "A", Version: 1, Sealed: good.Sealed, KeyId: k.ID()}, "ids"},
		"app is not a label":   {withApp(good, "Not_A_Label"), "DNS label"},
		"name is not env var":  {withName(good, "1-bad"), "environment variable"},
		"version not positive": {withVersion(good, 0), "version"},
		"no value":             {&fleetv1.Secret{OrganizationId: org, ProjectId: proj, EnvironmentId: env, App: "portal", Name: "A", Version: 1, KeyId: k.ID()}, "no value"},
		"nothing":              {nil, "no secret"},
	} {
		w, cl := newWriter(t)
		err := w.Receive(ctx, k, cluster, c.s)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("%s: the error carries the value: %v", name, err)
		}
		var s corev1.Secret
		if err := cl.Get(ctx, client.ObjectKey{Namespace: desired.Namespace(env), Name: "portal-secrets"}, &s); !apierrors.IsNotFound(err) {
			t.Errorf("%s: nothing may be written: %v", name, err)
		}
	}

	// Sealed for another cluster id: the key is right, the target is not.
	w, _ := newWriter(t)
	if err := w.Receive(ctx, k, "cluster-2", good); err == nil || !strings.Contains(err.Error(), "cannot open") {
		t.Errorf("another cluster: %v", err)
	}
}

func withApp(s *fleetv1.Secret, v string) *fleetv1.Secret {
	c := proto.Clone(s).(*fleetv1.Secret)
	c.App = v
	return c
}

func withName(s *fleetv1.Secret, v string) *fleetv1.Secret {
	c := proto.Clone(s).(*fleetv1.Secret)
	c.Name = v
	return c
}

func withVersion(s *fleetv1.Secret, v int64) *fleetv1.Secret {
	c := proto.Clone(s).(*fleetv1.Secret)
	c.Version = v
	return c
}

func TestPutCopiesTheValue(t *testing.T) {
	w, c := newWriter(t)
	v := []byte("value")
	if err := w.Put(context.Background(), Delivery{Organization: org, Project: proj, Environment: env, App: "portal", Name: "A", Version: 1}, v); err != nil {
		t.Fatal(err)
	}
	clear(v) // what Receive does after the write
	if got := stored(t, c).Data["A"]; string(got) != "value" {
		t.Errorf("stored value shares the caller's buffer: %q", got)
	}
}

func TestVersionsToleratesAGarbledAnnotation(t *testing.T) {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationVersions: "{not json"}}}
	if v := Versions(s); len(v) != 0 {
		t.Errorf("versions: %v", v)
	}
	if v := Versions(nil); len(v) != 0 {
		t.Errorf("versions of nothing: %v", v)
	}
}

func TestPutRecordsWhenEachKeyWasDelivered(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.FixedZone("x", 3600))
	w, c := newWriter(t)
	w.Now = func() time.Time { return at }
	d := Delivery{Organization: org, Project: proj, Environment: env, App: "portal", Name: "A", Version: 1}
	if err := w.Put(ctx, d, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, c).Annotations[AnnotationDelivered]; got != `{"A":"2026-10-27T11:00:00Z"}` {
		t.Fatalf("delivered annotation (RFC 3339, UTC): %q", got)
	}

	// Another key, later: both are there, each with its own time.
	later := at.Add(5 * time.Minute)
	w.Now = func() time.Time { return later }
	d.Name = "B"
	if err := w.Put(ctx, d, []byte("b")); err != nil {
		t.Fatal(err)
	}
	// A newer version of A refreshes its time.
	evenLater := at.Add(time.Hour)
	w.Now = func() time.Time { return evenLater }
	d.Name, d.Version = "A", 2
	if err := w.Put(ctx, d, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	got := Delivered(stored(t, c))
	if len(got) != 2 || !got["A"].Equal(evenLater) || !got["B"].Equal(later) {
		t.Errorf("delivered: %v", got)
	}

	// A repeated delivery writes nothing, so it does not move the time.
	w.Now = func() time.Time { return evenLater.Add(time.Hour) }
	if err := w.Put(ctx, d, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	if got = Delivered(stored(t, c)); !got["A"].Equal(evenLater) {
		t.Errorf("an ignored delivery must not refresh the time: %v", got)
	}
}

func TestPutAddsATimeToASecretWrittenBeforeDeliveryTimesExisted(t *testing.T) {
	ctx := context.Background()
	w, c := newWriter(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: desired.Namespace(env), Name: "portal-secrets",
			Labels:      map[string]string{desired.LabelManagedBy: desired.ManagedBy},
			Annotations: map[string]string{AnnotationVersions: `{"OLD":1}`}},
		Data: map[string][]byte{"OLD": []byte("o")},
	})
	if err := w.Put(ctx, Delivery{Organization: org, Project: proj, Environment: env, App: "portal", Name: "NEW", Version: 1}, []byte("n")); err != nil {
		t.Fatal(err)
	}
	got := Delivered(stored(t, c))
	if _, ok := got["OLD"]; ok || got["NEW"].IsZero() {
		t.Errorf("only the written key gets a time (the old one counts as old): %v", got)
	}
}

func TestDeliveredToleratesAGarbledAnnotation(t *testing.T) {
	for _, raw := range []string{"{not json", `{"A":"yesterday"}`, ""} {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationDelivered: raw}}}
		if d := Delivered(s); len(d) != 0 {
			t.Errorf("%q: %v", raw, d)
		}
	}
	if d := Delivered(nil); len(d) != 0 {
		t.Errorf("nothing: %v", d)
	}
	s := &corev1.Secret{}
	SetDelivered(s, nil)
	if _, ok := s.Annotations[AnnotationDelivered]; ok {
		t.Errorf("no entries, no annotation: %v", s.Annotations)
	}
}

func TestPutRefusesASecretThatIsNotOurs(t *testing.T) {
	ctx := context.Background()
	for name, labels := range map[string]map[string]string{
		"no labels":       nil,
		"another manager": {desired.LabelManagedBy: "helm"},
		"only the app":    {LabelApp: "portal"},
	} {
		t.Run(name, func(t *testing.T) {
			theirs := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: desired.Namespace(env), Name: "portal-secrets", Labels: labels},
				Data:       map[string][]byte{"THEIRS": []byte("keep")},
			}
			w, c := newWriter(t, theirs)
			k := key(t)
			err := w.Receive(ctx, k, cluster, delivery(t, k, "A", 1, "secret-value"))
			if err == nil || !strings.Contains(err.Error(), "not managed by Odyssey") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Errorf("the error carries the value: %v", err)
			}
			s := stored(t, c)
			if len(s.Data) != 1 || string(s.Data["THEIRS"]) != "keep" || len(s.Annotations) != 0 || s.Labels[desired.LabelManagedBy] != labels[desired.LabelManagedBy] {
				t.Errorf("a foreign Secret is left as it is: %+v %v", s.ObjectMeta, s.Data)
			}
		})
	}
}

func TestOurs(t *testing.T) {
	if Ours(&corev1.Secret{}) || !Ours(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{desired.LabelManagedBy: desired.ManagedBy}}}) {
		t.Error("Ours follows the managed-by label")
	}
}
