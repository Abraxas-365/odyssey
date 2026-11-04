package fleetsvc_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

func secretKey(t *testing.T) (secretbox.Key, *fleet.SecretKey) {
	t.Helper()
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k, &fleet.SecretKey{KeyID: k.ID(), Public: k.Public(), Suite: secretbox.Suite}
}

// The whole managed-secret path through the service: the target offers the
// cluster's key, a value sealed to it waits for the agent, the newest
// version replaces an older one, the ack ends it (from the current tunnel
// only), and one nobody took in time is dropped as failed.
func TestSecretDeliveryLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	env, proj := project.NewEnvironmentID(), project.NewID()

	if st, err := e.svc.SecretTarget(ctx, e.org, env); err != nil || !st.Cluster.IsZero() || st.Key != nil {
		t.Fatalf("no cluster: %+v %v", st, err)
	}
	agent := e.enrolled(t, "lima-1")
	session := fleet.NewSession()
	connect := func(caps fleet.Capabilities) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1", Capabilities: caps}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	connect(fleet.Capabilities{})
	if st, err := e.svc.SecretTarget(ctx, e.org, env); err != nil || st.Cluster != agent.Cluster || st.Key != nil || st.External || st.Stores == nil {
		t.Fatalf("cluster without a key: %+v %v", st, err)
	}
	delivery := func(version int64, keyID string) fleet.SecretDelivery {
		return fleet.SecretDelivery{Organization: e.org, Project: proj, Environment: env, App: "web", Name: "STRIPE_API_KEY",
			Version: version, KeyID: keyID, Sealed: bytes.Repeat([]byte{byte(version)}, 64), CreatedBy: e.user}
	}
	if err := e.svc.DeliverSecret(ctx, delivery(1, "k")); code(err) != fleet.CodeSecretsUnavailable {
		t.Fatalf("no key: %v", err)
	}

	_, key := secretKey(t)
	connect(fleet.Capabilities{SecretKey: key, APIs: []string{fleet.CapabilityExternalSecrets}, SecretStores: []string{"vault-prod"}})
	st, err := e.svc.SecretTarget(ctx, e.org, env)
	if err != nil || st.Key == nil || st.Key.KeyID != key.KeyID || !st.External || !slices.Equal(st.Stores, []string{"vault-prod"}) {
		t.Fatalf("target %+v %v", st, err)
	}
	if err := e.svc.DeliverSecret(ctx, delivery(1, "another-key")); code(err) != fleet.CodeSecretKeyChanged {
		t.Fatalf("stale key: %v", err)
	}
	bad := delivery(1, key.KeyID)
	bad.Sealed = []byte("short")
	if err := e.svc.DeliverSecret(ctx, bad); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("short ciphertext: %v", err)
	}

	e.events.Reset()
	if err := e.svc.DeliverSecret(ctx, delivery(1, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeliverSecret(ctx, delivery(2, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeliverSecret(ctx, delivery(1, key.KeyID)); code(err) != fleet.CodeSecretVersionConflict { // older: refused, no event
		t.Fatalf("older version: %v", err)
	}
	if got := e.events.Types(); !slices.Equal(got, []string{fleet.EventSecretSent, fleet.EventSecretSent}) {
		t.Fatalf("events %v", got)
	}
	waiting, err := e.svc.DesiredSecrets(ctx, agent)
	if err != nil || len(waiting) != 1 || waiting[0].Version != 2 || waiting[0].Cluster != agent.Cluster {
		t.Fatalf("waiting %+v %v", waiting, err)
	}
	id := waiting[0].ID
	if d, ok, err := e.svc.DesiredSecret(ctx, agent, id); !ok || err != nil || d.Version != 2 {
		t.Fatalf("one %+v %v %v", d, ok, err)
	}
	// another organization's agent sees none of it
	stranger := fleet.AgentRef{Organization: identity.NewOrganizationID(), Cluster: agent.Cluster, Serial: agent.Serial}
	if got, err := e.svc.DesiredSecrets(ctx, stranger); err != nil || len(got) != 0 {
		t.Fatalf("cross-tenant %+v %v", got, err)
	}
	if _, ok, _ := e.svc.DesiredSecret(ctx, stranger, id); ok {
		t.Fatal("cross-tenant one")
	}

	// an ack from a superseded tunnel changes nothing; the current one ends it
	e.events.Reset()
	if err := e.svc.AckSecret(ctx, agent, fleet.NewSession(), fleet.SecretAck{Delivery: id}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.svc.DesiredSecret(ctx, agent, id); !ok || len(e.events.Events()) != 0 {
		t.Fatal("a stale tunnel's ack deleted the delivery")
	}
	if err := e.svc.AckSecret(ctx, agent, session, fleet.SecretAck{Delivery: id, Error: "  could not open\x07 it  "}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.svc.DesiredSecret(ctx, agent, id); ok {
		t.Fatal("acked delivery still waits")
	}
	if got := e.events.Types(); !slices.Equal(got, []string{fleet.EventSecretFailed}) {
		t.Fatalf("ack events %v", got)
	}
	// the failure shows in the target until a newer value is sent
	if st, err := e.svc.SecretTarget(ctx, e.org, env); err != nil || len(st.Failures) != 1 || st.Failures[0].Version != 2 ||
		st.Failures[0].Error != "el clúster no pudo guardarlo" || st.Failures[0].Name != "STRIPE_API_KEY" {
		t.Fatalf("failures %+v %v", st.Failures, err)
	}
	if err := e.svc.AckSecret(ctx, agent, session, fleet.SecretAck{Delivery: id}); err != nil || len(e.events.Events()) != 1 {
		t.Fatalf("second ack: %v %v", err, e.events.Types())
	}

	// a value nobody takes within SecretDeliveryTTL is dropped as failed
	if err := e.svc.DeliverSecret(ctx, delivery(3, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.SecretTarget(ctx, e.org, env); len(st.Failures) != 0 {
		t.Fatalf("a newer delivery left the failure %+v", st.Failures)
	}
	e.events.Reset()
	e.clock.add(fleet.SecretDeliveryTTL + time.Hour)
	if got, err := e.svc.DesiredSecrets(ctx, agent); err != nil || len(got) != 0 {
		t.Fatalf("after the TTL %+v %v", got, err)
	}
	if got := e.events.Types(); !slices.Equal(got, []string{fleet.EventSecretFailed}) {
		t.Fatalf("expiry events %v", got)
	}

	// a stored value clears its failure
	if err := e.svc.DeliverSecret(ctx, delivery(4, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	waiting, err = e.svc.DesiredSecrets(ctx, agent)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("v4 %+v %v", waiting, err)
	}
	if err := e.svc.AckSecret(ctx, agent, session, fleet.SecretAck{Delivery: waiting[0].ID}); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.SecretTarget(ctx, e.org, env); len(st.Failures) != 0 {
		t.Fatalf("stored, still failed %+v", st.Failures)
	}

	// the sweep drops what a cluster that never came back left waiting
	if err := e.svc.DeliverSecret(ctx, delivery(5, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.SweepSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("early sweep %d %v", n, err)
	}
	e.events.Reset()
	e.clock.add(fleet.SecretDeliveryTTL + time.Hour)
	if n, err := e.svc.SweepSecrets(ctx); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if got := e.events.Types(); !slices.Equal(got, []string{fleet.EventSecretFailed}) {
		t.Fatalf("sweep events %v", got)
	}
	if st, _ := e.svc.SecretTarget(ctx, e.org, env); len(st.Failures) != 1 || st.Failures[0].Version != 5 {
		t.Fatalf("swept, not failed %+v", st.Failures)
	}

	// revoking the cluster drops its deliveries (nobody else can open them)
	// and the target says why it offers nothing
	if err := e.svc.DeliverSecret(ctx, delivery(6, key.KeyID)); err != nil {
		t.Fatal(err)
	}
	e.events.Reset()
	if err := e.svc.Revoke(ctx, e.org, agent.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if got := e.events.Types(); !slices.Equal(got, []string{fleet.EventRevoked, fleet.EventSecretFailed}) {
		t.Fatalf("revoke events %v", got)
	}
	if st, err := e.svc.SecretTarget(ctx, e.org, env); err != nil || !st.Revoked || st.Key != nil || st.Cluster != agent.Cluster {
		t.Fatalf("revoked target %+v %v", st, err)
	}
}

// Apply refuses an app whose secrets its cluster cannot hold: a managed one
// sealed to another key (or no key at all), an external one without the
// operator or its store.
func TestApplyChecksSecrets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima-1")
	_, key := secretKey(t)
	connect := func(caps fleet.Capabilities) {
		t.Helper()
		if err := e.svc.Connect(ctx, agent, fleet.NewSession(), fleet.Agent{Version: "v1", Capabilities: caps}, "10.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	app := func(secrets ...fleet.AppSecret) fleet.App {
		return fleet.App{Organization: e.org, Project: project.NewID(), Environment: project.NewEnvironmentID(), Name: "web", Revision: 1,
			Image: "r.local/web@sha256:00", Port: 8080, HealthPath: "/health", Replicas: 1, Access: fleet.AccessPublic, Secrets: secrets}
	}
	managed := fleet.AppSecret{Name: "STRIPE_API_KEY", Source: fleet.SecretManaged, Version: 1, KeyID: key.KeyID}
	external := fleet.AppSecret{Name: "DB_PASSWORD", Source: fleet.SecretExternal, Store: "vault-prod", Key: "apps/db"}

	connect(fleet.Capabilities{})
	if err := e.svc.Apply(ctx, app(managed)); code(err) != fleet.CodeSecretsUnavailable {
		t.Fatalf("managed without a key: %v", err)
	}
	if err := e.svc.Apply(ctx, app(external)); code(err) != fleet.CodeExternalSecretsUnavailable {
		t.Fatalf("external without the operator: %v", err)
	}
	connect(fleet.Capabilities{SecretKey: key, APIs: []string{fleet.CapabilityExternalSecrets}, SecretStores: []string{"vault-dev"}})
	stale := managed
	stale.KeyID = "old-key"
	if err := e.svc.Apply(ctx, app(stale)); code(err) != fleet.CodeSecretKeyChanged {
		t.Fatalf("managed sealed to an old key: %v", err)
	}
	if err := e.svc.Apply(ctx, app(external)); code(err) != fleet.CodeSecretStoreUnknown {
		t.Fatalf("unknown store: %v", err)
	}
	connect(fleet.Capabilities{SecretKey: key, APIs: []string{fleet.CapabilityExternalSecrets}, SecretStores: []string{"vault-prod"}})
	a := app(managed, external)
	if err := e.svc.Apply(ctx, a); err != nil {
		t.Fatalf("both: %v", err)
	}
	if got, ok, err := e.svc.DesiredApp(ctx, agent, a.Environment, "web"); !ok || err != nil || len(got.Secrets) != 2 {
		t.Fatalf("stored %+v %v %v", got, ok, err)
	}
}
