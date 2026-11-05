package fleetsvc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestSetEgress(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")

	st, err := e.svc.Egress(ctx, e.org, agent.Cluster)
	if err != nil || st.Mode != fleet.EgressInternet || st.State != fleet.EgressUnsupported || st.Enforced {
		t.Fatalf("default %+v %v", st, err)
	}

	st, err = e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Mode: fleet.EgressRestricted, Allow: []string{"203.0.113.9/24", "203.0.113.0/24"}, ChangedBy: e.user})
	if err != nil || st.Mode != fleet.EgressRestricted || len(st.Allow) != 1 || st.Allow[0] != "203.0.113.0/24" || st.Generation != 1 {
		t.Fatalf("set %+v %v", st, err)
	}
	if got := e.events.Types(); len(got) != 3 || got[2] != fleet.EventEgressChanged { // created, enrolled, changed
		t.Fatalf("events %v", got)
	}

	// the same setting again changes nothing and says nothing
	st, err = e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Mode: fleet.EgressRestricted, Allow: []string{"203.0.113.0/24"}, ChangedBy: e.user})
	if err != nil || st.Generation != 1 || len(e.events.Types()) != 3 {
		t.Fatalf("repeat %+v %v %v", st, err, e.events.Types())
	}

	// the agent connects (old: no capability), then answers
	session := fleet.NewSession()
	if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{APIs: []string{fleet.CapabilityEgress}}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.DesiredPolicy(ctx, agent)
	if err != nil || p.Generation != 1 || p.Egress.Mode != fleet.EgressRestricted {
		t.Fatalf("desired %+v %v", p, err)
	}
	if st, _ = e.svc.Egress(ctx, e.org, agent.Cluster); st.State != fleet.EgressPending {
		t.Fatalf("before the ack: %s", st.State)
	}
	if err := e.svc.AckPolicy(ctx, agent, session, fleet.PolicyAck{Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if st, _ = e.svc.Egress(ctx, e.org, agent.Cluster); st.State != fleet.EgressApplied || !st.Enforced {
		t.Fatalf("after the ack: %+v", st)
	}
	if got := e.events.Types(); len(got) != 4 || got[3] != fleet.EventConnected { // an ack is an observation
		t.Fatalf("events %v", got)
	}

	// a new generation is pending again; a hostile error text is bounded
	if st, _ = e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{ChangedBy: e.user}); st.State != fleet.EgressPending || st.Generation != 2 {
		t.Fatalf("second change %+v", st)
	}
	if err := e.svc.AckPolicy(ctx, agent, session, fleet.PolicyAck{Generation: 2, Error: strings.Repeat("x", 5000)}); err != nil {
		t.Fatal(err)
	}
	if st, _ = e.svc.Egress(ctx, e.org, agent.Cluster); st.State != fleet.EgressFailed || len(st.Error) > fleet.MaxAckError {
		t.Fatalf("failed %+v", st)
	}
}

func TestSetEgressRefusals(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	if _, err := e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Allow: []string{"0.0.0.0/0"}, ChangedBy: e.user}); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("0.0.0.0/0: %v", err)
	}
	if _, err := e.svc.SetEgress(ctx, identity.NewOrganizationID(), agent.Cluster, fleet.SetEgress{ChangedBy: e.user}); !errx.IsType(err, errx.TypeNotFound) {
		t.Errorf("another organization's cluster: %v", err)
	}
	if _, err := e.svc.Egress(ctx, identity.NewOrganizationID(), agent.Cluster); !errx.IsType(err, errx.TypeNotFound) {
		t.Errorf("another organization reads: %v", err)
	}
	if err := e.svc.Revoke(ctx, e.org, agent.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{ChangedBy: e.user}); code(err) != fleet.CodeClusterRevoked {
		t.Errorf("revoked: %v", err)
	}
}

func TestAckWarningsAreCleanedAndShownWhileApplied(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := e.enrolled(t, "lima")
	session := fleet.NewSession()
	if err := e.svc.Connect(ctx, agent, session, fleet.Agent{Version: "v1", Capabilities: fleet.Capabilities{APIs: []string{fleet.CapabilityEgress}}}, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Mode: fleet.EgressRestricted, Allow: []string{"10.0.0.0/8"}, ChangedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", fleet.MaxAckError+1)
	many := []string{"ok one", "", "  ", long, "bell\a", "ok two"}
	for i := range 20 {
		many = append(many, "w"+strings.Repeat("y", i))
	}
	if err := e.svc.AckPolicy(ctx, agent, session, fleet.PolicyAck{Generation: 1, Warnings: many}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.Egress(ctx, e.org, agent.Cluster)
	if st.State != fleet.EgressApplied || len(st.Warnings) != fleet.MaxPolicyWarnings || st.Warnings[0] != "ok one" || st.Warnings[1] != "ok two" {
		t.Fatalf("warnings %q (state %s)", st.Warnings, st.State)
	}
	// a new generation is pending: the old warnings are not shown for it
	if st, _ = e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Mode: fleet.EgressRestricted, Allow: []string{"10.0.0.0/8", "198.51.100.0/24"}, ChangedBy: e.user}); st.State != fleet.EgressPending || len(st.Warnings) != 0 || st.Warnings == nil {
		t.Errorf("pending %+v", st)
	}
}

// An environment's egress target is its cluster's policy (the placement, else
// the default); with no cluster, or a revoked one, there is none.
func TestEgressTarget(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	env := project.NewEnvironmentID()

	if tg, err := e.svc.EgressTarget(ctx, e.org, env); err != nil || !tg.Cluster.IsZero() {
		t.Fatalf("no cluster: %+v %v", tg, err)
	}
	agent := e.enrolled(t, "lima")
	tg, err := e.svc.EgressTarget(ctx, e.org, env)
	if err != nil || tg.Cluster != agent.Cluster || tg.Status.Mode != fleet.EgressInternet || tg.Status.State != fleet.EgressUnsupported || tg.Status.Enforced {
		t.Fatalf("default cluster: %+v %v", tg, err)
	}
	if _, err := e.svc.SetEgress(ctx, e.org, agent.Cluster, fleet.SetEgress{Mode: fleet.EgressRestricted, Allow: []string{"203.0.113.0/24"}, ChangedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if tg, err = e.svc.EgressTarget(ctx, e.org, env); err != nil || tg.Status.Mode != fleet.EgressRestricted || tg.Status.Generation != 1 {
		t.Fatalf("restricted: %+v %v", tg, err)
	}
	// another organization sees none of it
	if tg, err = e.svc.EgressTarget(ctx, identity.NewOrganizationID(), env); err != nil || !tg.Cluster.IsZero() {
		t.Fatalf("other tenant: %+v %v", tg, err)
	}
	if err := e.svc.Revoke(ctx, e.org, agent.Cluster, fleet.Revoke{RevokedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if tg, err = e.svc.EgressTarget(ctx, e.org, env); err != nil || !tg.Cluster.IsZero() {
		t.Fatalf("revoked: %+v %v", tg, err)
	}
}
