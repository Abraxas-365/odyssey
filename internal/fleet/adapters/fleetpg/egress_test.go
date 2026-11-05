package fleetpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetpg"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
)

func TestEgress(t *testing.T) {
	e := setup(t)
	ctx, repo := e.ctx, e.repo
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")

	// a new cluster has the default policy at generation 0, nothing acknowledged
	got, err := repo.FindEgress(ctx, org, c.ID)
	if err != nil || got.Mode != fleet.EgressInternet || got.Allow == nil || len(got.Allow) != 0 || got.Generation != 0 || got.Acknowledged != nil {
		t.Fatalf("default %+v %v", got, err)
	}
	_, err = repo.FindEgress(ctx, other, c.ID)
	notFound(t, "cross-tenant find", err)

	// tenant isolation: another organization changes nothing
	_, err = repo.PutEgress(ctx, other, c.ID, fleet.Egress{Mode: fleet.EgressRestricted, Allow: []string{}})
	notFound(t, "cross-tenant put", err)
	if got, _ := repo.FindEgress(ctx, org, c.ID); got.Generation != 0 || got.Mode != fleet.EgressInternet {
		t.Fatalf("a foreign put changed the policy: %+v", got)
	}

	// every change raises the generation
	want := fleet.Egress{Mode: fleet.EgressRestricted, Allow: []string{"203.0.113.0/24"}}
	if g, err := repo.PutEgress(ctx, org, c.ID, want); err != nil || g != 1 {
		t.Fatalf("put %d %v", g, err)
	}
	if g, err := repo.PutEgress(ctx, org, c.ID, want); err != nil || g != 2 {
		t.Fatalf("put again %d %v", g, err)
	}
	got, _ = repo.FindEgress(ctx, org, c.ID)
	if !got.Same(want) || got.Generation != 2 {
		t.Fatalf("stored %+v", got)
	}

	// answers: only while the session is the tunnel, of a generation that exists,
	// never going back
	s1, s2 := fleet.NewSession(), fleet.NewSession()
	if err := repo.SetSession(ctx, org, c.ID, s1, fleet.Agent{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ObservePolicy(ctx, org, c.ID, s2, fleet.PolicyAck{Generation: 2}); ok || err != nil {
		t.Fatalf("a stale session's ack counted: %v %v", ok, err)
	}
	if ok, _ := repo.ObservePolicy(ctx, other, c.ID, s1, fleet.PolicyAck{Generation: 2}); ok {
		t.Fatal("cross-tenant ack counted")
	}
	if ok, _ := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 3}); ok {
		t.Fatal("an ack of a generation never sent counted")
	}
	if ok, err := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 1, Error: "old"}); !ok || err != nil {
		t.Fatalf("ack 1: %v %v", ok, err)
	}
	if ok, err := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 2}); !ok || err != nil {
		t.Fatalf("ack 2: %v %v", ok, err)
	}
	if ok, _ := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 1, Error: "late"}); ok {
		t.Fatal("an older ack replaced a newer one")
	}
	got, _ = repo.FindEgress(ctx, org, c.ID)
	if got.Acknowledged == nil || *got.Acknowledged != 2 || got.Error != "" || got.AcknowledgedAt == nil {
		t.Fatalf("acknowledged %+v", got)
	}
	got.Supported = true
	if got.Status().State != fleet.EgressApplied {
		t.Fatalf("state %s", got.Status().State)
	}
	// warnings are stored with the ack, replaced by the next one (the agent
	// repeats its state), and shown only while the policy is applied
	if ok, _ := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 2, Warnings: []string{"10.0.0.0/8 incluye el nodo 10.0.0.4"}}); !ok {
		t.Fatal("a repeated ack of the same generation was refused")
	}
	if got, _ = repo.FindEgress(ctx, org, c.ID); len(got.Warnings) != 1 || got.Warnings[0] != "10.0.0.0/8 incluye el nodo 10.0.0.4" {
		t.Fatalf("warnings %+v", got.Warnings)
	}
	got.Supported = true
	if st := got.Status(); st.State != fleet.EgressApplied || len(st.Warnings) != 1 {
		t.Fatalf("applied status %+v", st)
	}
	if ok, _ := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 2}); !ok {
		t.Fatal("ack without warnings")
	}
	if got, _ = repo.FindEgress(ctx, org, c.ID); got.Warnings == nil || len(got.Warnings) != 0 {
		t.Fatalf("warnings did not clear: %#v", got.Warnings)
	}
	// an error is kept and shown
	if ok, _ := repo.ObservePolicy(ctx, org, c.ID, s1, fleet.PolicyAck{Generation: 2, Error: "forbidden"}); !ok {
		t.Fatal("the agent's error was not stored")
	}
	if got, _ = repo.FindEgress(ctx, org, c.ID); got.Error != "forbidden" {
		t.Fatalf("error %+v", got)
	}

	// a revoked cluster keeps its policy but takes no change
	if err := repo.Revoke(ctx, org, c.ID, e.user); err != nil {
		t.Fatal(err)
	}
	_, err = repo.PutEgress(ctx, org, c.ID, fleet.DefaultEgress())
	notFound(t, "put on a revoked cluster", err)
	if got, err := repo.FindEgress(ctx, org, c.ID); err != nil || got.Mode != fleet.EgressRestricted {
		t.Fatalf("revoked %+v %v", got, err)
	}
}

func TestEgressSignalsTheGateway(t *testing.T) {
	e := setup(t)
	var name string
	if err := e.db.Get(&name, `SELECT current_database()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	got := make(chan fleet.Signal, 16)
	done := make(chan error, 1)
	go func() {
		done <- fleetpg.NewSignals(pgtest.URL(t, name)).Listen(ctx, func(s fleet.Signal) { got <- s }, func() {})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	org := identity.NewOrganizationID()
	c := e.create(org, "lima")
	e.enrol(c, "s1")
	// LISTEN is set up asynchronously: repeat the write until heard
	for i := 0; i < 50; i++ {
		if _, err := e.repo.PutEgress(e.ctx, org, c.ID, fleet.DefaultEgress()); err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if s.Policy && s.Cluster == c.ID && s.Organization == org {
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("no policy signal")
}
