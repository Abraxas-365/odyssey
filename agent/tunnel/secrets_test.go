package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/secretbox"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeSecrets records what the tunnel hands it.
type fakeSecrets struct {
	key     secretbox.Key
	cluster string
	got     []*fleetv1.Secret
	err     error
}

func (f *fakeSecrets) Receive(_ context.Context, key secretbox.Key, cluster string, s *fleetv1.Secret) error {
	f.key, f.cluster = key, cluster
	f.got = append(f.got, s)
	return f.err
}

func secretClient(store Store, s Secrets) *Client {
	return New(Options{Store: store, Secrets: s, Log: logr.Discard()})
}

func TestSecretKeyIsMadeOnceAndAnnounced(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(Enrollment{})
	c := secretClient(store, &fakeSecrets{})

	first := c.newSecretSession(ctx)
	if !first.have {
		t.Fatal("a key is made when there is none")
	}
	caps := first.capabilities(&fleetv1.Capabilities{Apis: []string{"traefik.io"}})
	k := caps.GetSecretKey()
	if k.GetKeyId() != first.key.ID() || k.GetSuite() != secretbox.Suite || !secretbox.ValidPublic(k.GetPublicKey()) || secretbox.KeyID(k.GetPublicKey()) != k.GetKeyId() {
		t.Fatalf("secret_key: %+v", k)
	}
	if len(caps.GetApis()) != 1 {
		t.Errorf("the rest of the capabilities are kept: %+v", caps)
	}
	if raw, _ := store.SecretKey(ctx); len(raw) == 0 {
		t.Fatal("the key must be saved")
	}

	// The next session (a reconnect, a restart) announces the same key.
	again := secretClient(store, &fakeSecrets{}).newSecretSession(ctx)
	if !again.have || again.key.ID() != first.key.ID() {
		t.Errorf("the key changed: %s -> %s", first.key.ID(), again.key.ID())
	}

	// The first key kept wins: a racing replica's key is dropped.
	other, _ := secretbox.GenerateKey()
	raw, _ := other.Bytes()
	if err := store.SaveSecretKey(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SecretKey(ctx); string(got) == string(raw) {
		t.Error("SaveSecretKey must not replace a key")
	}
}

func TestNoSecretKeyWithoutAStoreOrAKey(t *testing.T) {
	ctx := context.Background()
	if ss := secretClient(NewMemoryStore(Enrollment{}), nil).newSecretSession(ctx); ss.have || ss.capabilities(&fleetv1.Capabilities{}).GetSecretKey() != nil {
		t.Error("an agent that stores no secrets announces no key")
	}
	if got := (secretSession{}).capabilities(nil); got == nil || got.GetSecretKey() != nil {
		t.Errorf("capabilities: %+v", got)
	}

	// A garbled key is never replaced behind the operator's back.
	bad := NewMemoryStore(Enrollment{})
	if err := bad.SaveSecretKey(ctx, []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if ss := secretClient(bad, &fakeSecrets{}).newSecretSession(ctx); ss.have {
		t.Error("an unreadable key means no capability")
	}
	if got, _ := bad.SecretKey(ctx); string(got) != "garbage" {
		t.Errorf("the stored key was replaced: %q", got)
	}

	// A store that cannot keep the key: still no capability, the agent runs.
	if ss := secretClient(failingStore{NewMemoryStore(Enrollment{})}, &fakeSecrets{}).newSecretSession(ctx); ss.have {
		t.Error("a key that was not kept must not be announced")
	}
}

type failingStore struct{ *MemoryStore }

func (failingStore) SaveSecretKey(context.Context, []byte) error { return errors.New("read-only") }

func ackOf(t *testing.T, sent []*fleetv1.AgentMessage) *fleetv1.SecretAck {
	t.Helper()
	if len(sent) != 1 || sent[0].GetSecretAck() == nil {
		t.Fatalf("sent: %+v", sent)
	}
	return sent[0].GetSecretAck()
}

func TestReceiveSecretAcksEitherWay(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		secrets *fakeSecrets
		have    bool
		want    string
	}{
		"stored":         {&fakeSecrets{}, true, ""},
		"refused":        {&fakeSecrets{err: errors.New("sealed for another cluster key")}, true, "sealed for another cluster key"},
		"no key":         {&fakeSecrets{}, false, "cannot take managed secrets"},
		"long error cut": {&fakeSecrets{err: errors.New(strings.Repeat("é", 2000))}, true, strings.Repeat("é", maxAckError)},
	} {
		key, _ := secretbox.GenerateKey()
		cl := secretClient(NewMemoryStore(Enrollment{}), c.secrets)
		var sent []*fleetv1.AgentMessage
		send := func(m *fleetv1.AgentMessage) error { sent = append(sent, m); return nil }
		err := cl.receiveSecret(ctx, secretSession{key: key, have: c.have}, "cluster-1", &fleetv1.Secret{DeliveryId: "d-7", App: "portal", Name: "A", Sealed: []byte("sealed-bytes")}, send)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ack := ackOf(t, sent)
		if ack.GetDeliveryId() != "d-7" || !strings.Contains(ack.GetError(), c.want) || (c.want == "" && ack.GetError() != "") {
			t.Errorf("%s: ack %+v", name, ack)
		}
		if c.have && (len(c.secrets.got) != 1 || c.secrets.cluster != "cluster-1" || c.secrets.key.ID() != key.ID()) {
			t.Errorf("%s: the writer must get the key and the cluster id: %+v", name, c.secrets)
		}
		if !c.have && len(c.secrets.got) != 0 {
			t.Errorf("%s: nothing may be stored without a key", name)
		}
	}

	// A broken stream is the caller's: the send error is returned.
	cl := secretClient(NewMemoryStore(Enrollment{}), &fakeSecrets{})
	key, _ := secretbox.GenerateKey()
	if err := cl.receiveSecret(ctx, secretSession{key: key, have: true}, "c", &fleetv1.Secret{DeliveryId: "d"}, func(*fleetv1.AgentMessage) error { return errors.New("closed") }); err == nil {
		t.Error("a failed ack ends the stream")
	}
}

func TestSecretStoreKeepsTheKeyBesideTheIdentity(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "odyssey-system", Name: "odyssey-agent"},
		Data: map[string][]byte{KeyToken: []byte("tok"), KeyGateway: []byte("gw:8443"), KeyCAPin: []byte("sha256:x")}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()
	store := &SecretStore{Reader: cl, Writer: cl, Namespace: "odyssey-system", Name: "odyssey-agent"}

	if raw, err := store.SecretKey(ctx); err != nil || raw != nil {
		t.Fatalf("no key yet: %v %v", raw, err)
	}
	k, _ := secretbox.GenerateKey()
	raw, _ := k.Bytes()
	if err := store.SaveSecretKey(ctx, raw); err != nil {
		t.Fatal(err)
	}
	other, _ := secretbox.GenerateKey()
	otherRaw, _ := other.Bytes()
	if err := store.SaveSecretKey(ctx, otherRaw); err != nil {
		t.Fatal(err)
	}
	got, err := store.SecretKey(ctx)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("the first key wins: %v", err)
	}

	// Saving the identity (and spending the token) leaves the key and what else was there.
	if err := store.Save(ctx, Identity{Organization: "o", Cluster: "c", KeyPEM: []byte("k"), CertPEM: []byte("c"), CAPEM: []byte("ca"), Gateway: "gw:8443", CAPin: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	var back corev1.Secret
	if err := cl.Get(ctx, client.ObjectKeyFromObject(sec), &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Data[KeySecret]) != string(raw) || back.Data[KeyToken] != nil || string(back.Data[KeyCluster]) != "c" {
		t.Errorf("secret keys: %v", back.Data)
	}
	if KeySecret != "secret.key" {
		t.Errorf("the key's name is part of the install contract: %q", KeySecret)
	}
}
