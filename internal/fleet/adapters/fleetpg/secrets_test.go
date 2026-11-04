package fleetpg_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestSecretDeliveries(t *testing.T) {
	e := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")
	session := fleet.NewSession()
	if err := e.repo.SetSession(e.ctx, org, c.ID, session, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	proj, environment := project.NewID(), project.NewEnvironmentID()
	sealed := func(b byte) []byte { return bytes.Repeat([]byte{b}, 64) }
	delivery := func(version int64, b byte) fleet.SecretDelivery {
		return fleet.SecretDelivery{ID: fleet.NewSecretDeliveryID(), Organization: org, Project: proj, Environment: environment, Cluster: c.ID,
			App: "web", Name: "STRIPE_API_KEY", Version: version, KeyID: "k3y", Sealed: sealed(b), CreatedBy: e.user, CreatedAt: time.Now().UTC()}
	}
	since := time.Now().Add(-time.Minute)

	v2 := delivery(2, 2)
	if ok, err := e.repo.PutSecretDelivery(e.ctx, v2); err != nil || !ok {
		t.Fatalf("put v2 %v %v", ok, err)
	}
	// an older (or the same) version never replaces a newer one
	if ok, err := e.repo.PutSecretDelivery(e.ctx, delivery(1, 1)); err != nil || ok {
		t.Fatalf("put v1 over v2 %v %v", ok, err)
	}
	if ok, err := e.repo.PutSecretDelivery(e.ctx, delivery(2, 9)); err != nil || ok {
		t.Fatalf("put v2 again %v %v", ok, err)
	}
	got, err := e.repo.FindSecretDelivery(e.ctx, org, c.ID, v2.ID)
	if err != nil || got.Version != 2 || !bytes.Equal(got.Sealed, sealed(2)) || got.App != "web" || got.CreatedBy != e.user || got.CreatedAt.IsZero() {
		t.Fatalf("find %+v %v", got, err)
	}
	// a newer version replaces it under its own id
	v3 := delivery(3, 3)
	if ok, err := e.repo.PutSecretDelivery(e.ctx, v3); err != nil || !ok {
		t.Fatalf("put v3 %v %v", ok, err)
	}
	notFound(t, "replaced delivery", func() error { _, err := e.repo.FindSecretDelivery(e.ctx, org, c.ID, v2.ID); return err }())
	list, err := e.repo.ListSecretDeliveries(e.ctx, org, c.ID, since)
	if err != nil || len(list) != 1 || list[0].ID != v3.ID || list[0].Version != 3 {
		t.Fatalf("list %+v %v", list, err)
	}
	if list, err := e.repo.ListSecretDeliveries(e.ctx, org, c.ID, time.Now().Add(time.Minute)); err != nil || len(list) != 0 {
		t.Fatalf("list since later %+v %v", list, err)
	}
	// the table refuses what Validate would (a bad name, a short ciphertext)
	bad := delivery(4, 4)
	bad.Name = "1BAD"
	if _, err := e.repo.PutSecretDelivery(e.ctx, bad); err == nil {
		t.Fatal("bad name stored")
	}
	bad = delivery(4, 4)
	bad.Sealed = []byte("short")
	if _, err := e.repo.PutSecretDelivery(e.ctx, bad); err == nil {
		t.Fatal("short ciphertext stored")
	}

	// tenant isolation: another organization neither finds, lists, replaces nor deletes it
	notFound(t, "cross-tenant find", func() error { _, err := e.repo.FindSecretDelivery(e.ctx, other, c.ID, v3.ID); return err }())
	if list, err := e.repo.ListSecretDeliveries(e.ctx, other, c.ID, since); err != nil || len(list) != 0 {
		t.Fatalf("cross-tenant list %+v %v", list, err)
	}
	hijack := delivery(9, 9)
	hijack.Organization = other
	if ok, _ := e.repo.PutSecretDelivery(e.ctx, hijack); ok {
		t.Fatal("another organization replaced the delivery")
	}
	if _, ok, err := e.repo.DeleteSecretDelivery(e.ctx, other, c.ID, session, v3.ID); err != nil || ok {
		t.Fatalf("cross-tenant delete %v %v", ok, err)
	}
	if out, err := e.repo.DeleteSecretDeliveries(e.ctx, other, c.ID, time.Now().Add(time.Hour)); err != nil || len(out) != 0 {
		t.Fatalf("cross-tenant expiry %+v %v", out, err)
	}

	// only the current tunnel's ack deletes it
	if _, ok, err := e.repo.DeleteSecretDelivery(e.ctx, org, c.ID, fleet.NewSession(), v3.ID); err != nil || ok {
		t.Fatalf("stale session delete %v %v", ok, err)
	}
	if out, ok, err := e.repo.DeleteSecretDelivery(e.ctx, org, c.ID, session, v3.ID); err != nil || !ok || out.ID != v3.ID || out.Version != 3 {
		t.Fatalf("delete %+v %v %v", out, ok, err)
	}
	if _, ok, err := e.repo.DeleteSecretDelivery(e.ctx, org, c.ID, session, v3.ID); err != nil || ok {
		t.Fatalf("second delete %v %v", ok, err)
	}

	// expiry deletes what is older than the cutoff, and returns it
	v5 := delivery(5, 5)
	if ok, err := e.repo.PutSecretDelivery(e.ctx, v5); err != nil || !ok {
		t.Fatalf("put v5 %v %v", ok, err)
	}
	if out, err := e.repo.DeleteSecretDeliveries(e.ctx, org, c.ID, time.Now().Add(-time.Hour)); err != nil || len(out) != 0 {
		t.Fatalf("nothing expired yet %+v %v", out, err)
	}
	if out, err := e.repo.DeleteSecretDeliveries(e.ctx, org, c.ID, time.Now().Add(time.Hour)); err != nil || len(out) != 1 || out[0].ID != v5.ID {
		t.Fatalf("expired %+v %v", out, err)
	}

	// the sweep expires across organizations, in batches, oldest first
	v6, v7 := delivery(6, 6), delivery(7, 7)
	v7.Name, v7.Organization = "OTHER", org
	for _, d := range []fleet.SecretDelivery{v6, v7} {
		if ok, err := e.repo.PutSecretDelivery(e.ctx, d); err != nil || !ok {
			t.Fatalf("put %d %v %v", d.Version, ok, err)
		}
	}
	if out, err := e.repo.ExpireSecretDeliveries(e.ctx, time.Now().Add(-time.Hour), 10); err != nil || len(out) != 0 {
		t.Fatalf("nothing to sweep yet %+v %v", out, err)
	}
	if out, err := e.repo.ExpireSecretDeliveries(e.ctx, time.Now().Add(time.Hour), 1); err != nil || len(out) != 1 || out[0].ID != v6.ID {
		t.Fatalf("first batch %+v %v", out, err)
	}
	// revoking drops what is left, only for its organization
	if out, err := e.repo.DropSecretDeliveries(e.ctx, other, c.ID); err != nil || len(out) != 0 {
		t.Fatalf("cross-tenant drop %+v %v", out, err)
	}
	if out, err := e.repo.DropSecretDeliveries(e.ctx, org, c.ID); err != nil || len(out) != 1 || out[0].ID != v7.ID {
		t.Fatalf("drop %+v %v", out, err)
	}
}

// TestSecretFailures: a failure stays until a newer delivery of the secret
// or a stored version clears it; an older failure never replaces a newer
// one; another organization neither sees nor clears it.
func TestSecretFailures(t *testing.T) {
	e := setup(t)
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")
	environment := project.NewEnvironmentID()
	failure := func(version int64, why string) fleet.SecretFailure {
		return fleet.SecretFailure{Organization: org, Environment: environment, Cluster: c.ID, App: "web", Name: "TOKEN", Version: version, Error: why}
	}
	list := func(org identity.OrganizationID) []fleet.SecretFailure {
		t.Helper()
		out, err := e.repo.ListSecretFailures(e.ctx, org, environment)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(org); got == nil || len(got) != 0 {
		t.Fatalf("empty %+v", got)
	}
	if err := e.repo.RecordSecretFailure(e.ctx, failure(2, "disco lleno")); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RecordSecretFailure(e.ctx, failure(1, "older")); err != nil {
		t.Fatal(err)
	}
	if got := list(org); len(got) != 1 || got[0].Version != 2 || got[0].Error != "disco lleno" || got[0].App != "web" || got[0].FailedAt.IsZero() {
		t.Fatalf("recorded %+v", got)
	}
	if err := e.repo.RecordSecretFailure(e.ctx, fleet.SecretFailure{Organization: org, Environment: environment, Cluster: c.ID,
		App: "web", Name: "TOKEN", Version: 3}); err == nil {
		t.Fatal("a failure without words was stored")
	}
	// tenant isolation
	if got := list(other); len(got) != 0 {
		t.Fatalf("cross-tenant list %+v", got)
	}
	if err := e.repo.ClearSecretFailure(e.ctx, other, environment, "web", "TOKEN", 9); err != nil {
		t.Fatal(err)
	}
	// an older stored version does not clear it; the same one does
	if err := e.repo.ClearSecretFailure(e.ctx, org, environment, "web", "TOKEN", 1); err != nil {
		t.Fatal(err)
	}
	if got := list(org); len(got) != 1 {
		t.Fatalf("cleared by another organization or an older version %+v", got)
	}
	if err := e.repo.ClearSecretFailure(e.ctx, org, environment, "web", "TOKEN", 2); err != nil {
		t.Fatal(err)
	}
	if got := list(org); len(got) != 0 {
		t.Fatalf("not cleared %+v", got)
	}
	// a newer delivery clears it too
	if err := e.repo.RecordSecretFailure(e.ctx, failure(4, "expiró")); err != nil {
		t.Fatal(err)
	}
	d := fleet.SecretDelivery{ID: fleet.NewSecretDeliveryID(), Organization: org, Project: project.NewID(), Environment: environment, Cluster: c.ID,
		App: "web", Name: "TOKEN", Version: 5, KeyID: "k3y", Sealed: bytes.Repeat([]byte{5}, 64), CreatedBy: e.user, CreatedAt: time.Now().UTC()}
	if ok, err := e.repo.PutSecretDelivery(e.ctx, d); err != nil || !ok {
		t.Fatalf("put %v %v", ok, err)
	}
	if got := list(org); len(got) != 0 {
		t.Fatalf("a newer delivery left the failure %+v", got)
	}
}
