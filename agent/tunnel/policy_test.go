package tunnel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
)

type fakePolicy struct {
	got []*fleetv1.ClusterPolicy
	err error
	// status is what Status answers.
	gen      int64
	failure  string
	warnings []string
}

func (f *fakePolicy) ReceivePolicy(_ context.Context, p *fleetv1.ClusterPolicy) error {
	f.got = append(f.got, p)
	return f.err
}

func (f *fakePolicy) Status() (int64, string, []string) { return f.gen, f.failure, f.warnings }

func policyMsg(gen int64) *fleetv1.ControlMessage {
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_ClusterPolicy{ClusterPolicy: &fleetv1.ClusterPolicy{
		Generation: gen, Egress: &fleetv1.EgressPolicy{Mode: "restricted", Allow: []string{"10.0.0.0/24"}}}}}
}

func TestPolicyIsAppliedAndAcknowledged(t *testing.T) {
	f := &fakePolicy{}
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Policy: f, Log: logr.Discard()})
	var sent []*fleetv1.AgentMessage
	send := func(m *fleetv1.AgentMessage) error { sent = append(sent, m); return nil }

	if handled, err := c.receivePolicy(context.Background(), &fleetv1.ControlMessage{}, send); handled || err != nil {
		t.Fatal("not a policy")
	}
	if handled, err := c.receivePolicy(context.Background(), policyMsg(7), send); !handled || err != nil {
		t.Fatalf("handled %v %v", handled, err)
	}
	if len(f.got) != 1 || f.got[0].GetGeneration() != 7 || len(sent) != 1 {
		t.Fatalf("applied %v, sent %v", f.got, sent)
	}
	if ack := sent[0].GetPolicyAck(); ack.GetGeneration() != 7 || ack.GetError() != "" {
		t.Errorf("ack %v", ack)
	}

	// a refusal is the ack's error; the stream goes on
	f.err = errors.New(strings.Repeat("é", maxAckError+10))
	if _, err := c.receivePolicy(context.Background(), policyMsg(8), send); err != nil {
		t.Fatal(err)
	}
	if ack := sent[1].GetPolicyAck(); ack.GetGeneration() != 8 || len([]rune(ack.GetError())) != maxAckError {
		t.Errorf("ack %d %d", ack.GetGeneration(), len([]rune(ack.GetError())))
	}

	// a send that fails ends the stream
	boom := errors.New("down")
	if _, err := c.receivePolicy(context.Background(), policyMsg(9), func(*fleetv1.AgentMessage) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("send error %v", err)
	}
}

func TestAgentWithoutPolicyAnswersWithAnError(t *testing.T) {
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Log: logr.Discard()})
	var sent []*fleetv1.AgentMessage
	if _, err := c.receivePolicy(context.Background(), policyMsg(1), func(m *fleetv1.AgentMessage) error { sent = append(sent, m); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].GetPolicyAck().GetError() == "" {
		t.Errorf("an agent that cannot enforce must say so: %v", sent)
	}
}

func TestEgressCapabilityOnlyWhenTheAgentEnforces(t *testing.T) {
	with := New(Options{Store: NewMemoryStore(Enrollment{}), Policy: &fakePolicy{}, Log: logr.Discard()})
	got := with.withEgress(&fleetv1.Capabilities{Apis: []string{"traefik.io"}})
	if !slices.Equal(got.GetApis(), []string{CapabilityEgress, "traefik.io"}) {
		t.Errorf("apis %v", got.GetApis())
	}
	if again := with.withEgress(got); len(again.GetApis()) != 2 {
		t.Errorf("listed twice: %v", again.GetApis())
	}
	without := New(Options{Store: NewMemoryStore(Enrollment{}), Log: logr.Discard()})
	if got := without.withEgress(&fleetv1.Capabilities{Apis: []string{"traefik.io"}}); slices.Contains(got.GetApis(), CapabilityEgress) {
		t.Error("an agent that does not enforce lists the capability")
	}
}

func TestAckCarriesWarningsAndStatusIsReportedAgain(t *testing.T) {
	f := &fakePolicy{gen: 7, warnings: []string{"10.0.0.0/24 includes node 10.0.0.5"}}
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Policy: f, Log: logr.Discard()})
	var sent []*fleetv1.AgentMessage
	send := func(m *fleetv1.AgentMessage) error { sent = append(sent, m); return nil }
	if _, err := c.receivePolicy(context.Background(), policyMsg(7), send); err != nil {
		t.Fatal(err)
	}
	if ack := sent[0].GetPolicyAck(); !slices.Equal(ack.GetWarnings(), f.warnings) {
		t.Errorf("warnings %v", ack.GetWarnings())
	}
	// the periodic report repeats the state, failure included
	f.failure = "namespace odyssey-a: forbidden"
	if err := c.reportPolicy(send); err != nil {
		t.Fatal(err)
	}
	if ack := sent[1].GetPolicyAck(); ack.GetGeneration() != 7 || ack.GetError() != f.failure || len(ack.GetWarnings()) != 1 {
		t.Errorf("report %v", ack)
	}
	// nothing is said before a policy arrived
	f.gen = 0
	n := len(sent)
	if err := c.reportPolicy(send); err != nil || len(sent) != n {
		t.Errorf("reported without a policy: %v %d", err, len(sent)-n)
	}
	// bounded
	long := make([]string, 30)
	for i := range long {
		long[i] = strings.Repeat("é", 1000)
	}
	f.gen, f.warnings = 7, long
	if err := c.reportPolicy(send); err != nil {
		t.Fatal(err)
	}
	last := sent[len(sent)-1].GetPolicyAck().GetWarnings()
	if len(last) != maxWarnings || len([]rune(last[0])) != maxWarning {
		t.Errorf("unbounded: %d x %d", len(last), len([]rune(last[0])))
	}
}
