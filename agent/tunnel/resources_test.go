package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
)

type fakeResources struct {
	mu       sync.Mutex
	applied  []*fleetv1.Resource
	err      error
	statuses map[string]*fleetv1.ResourceStatus
	reported []*fleetv1.ResourceStatus
}

func (f *fakeResources) ApplyResource(_ context.Context, r *fleetv1.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, r)
	return f.err
}

func (f *fakeResources) ResourceStatus(_ context.Context, _, name string) (*fleetv1.ResourceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[name], nil
}

func (f *fakeResources) ResourceStatuses(context.Context) ([]*fleetv1.ResourceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fleetv1.ResourceStatus
	for _, s := range f.statuses {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeResources) ResourceReported(_ context.Context, s *fleetv1.ResourceStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = append(f.reported, s)
	return nil
}

func resClient(r Resources) *Client {
	return New(Options{Store: NewMemoryStore(Enrollment{}), Resources: r, Log: logr.Discard(),
		IsRejected: func(err error) bool { return err.Error() == "refused" },
		RejectedResource: func(r *fleetv1.Resource, err error) *fleetv1.ResourceStatus {
			return &fleetv1.ResourceStatus{EnvironmentId: r.GetEnvironmentId(), Name: r.GetName(), ObservedGeneration: r.GetGeneration(), Phase: "failed", Message: err.Error()}
		}})
}

func ctlResource(gen int64) *fleetv1.ControlMessage {
	return &fleetv1.ControlMessage{Message: &fleetv1.ControlMessage_Resource{Resource: &fleetv1.Resource{EnvironmentId: "e1", Name: "main", Generation: gen}}}
}

func TestReceiveResourceKeepsTheNewestAndQueues(t *testing.T) {
	c := resClient(&fakeResources{})
	if c.receiveResource(&fleetv1.ControlMessage{}) {
		t.Fatal("not a resource")
	}
	if !c.receiveResource(ctlResource(3)) || !c.receiveResource(ctlResource(2)) {
		t.Fatal("resource messages are consumed")
	}
	if got := c.resources[key{"e1", "main"}]; got.GetGeneration() != 3 {
		t.Errorf("older generation replaced the newer: %v", got)
	}
	if c.resourceQueue.Len() != 1 {
		t.Errorf("queue: %d", c.resourceQueue.Len())
	}
	// An agent without Resources swallows the message.
	if !resClient(nil).receiveResource(ctlResource(1)) {
		t.Error("consumed without a handler")
	}
}

func TestWorkResourcesAppliesAndReportsRefusals(t *testing.T) {
	f := &fakeResources{err: errors.New("refused")}
	c := resClient(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.workResources(ctx)
	c.receiveResource(ctlResource(1))
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		rej := c.rejectedResources[key{"e1", "main"}]
		c.mu.Unlock()
		if rej != nil {
			if rej.GetPhase() != "failed" || rej.GetObservedGeneration() != 1 || rej.GetMessage() != "refused" {
				t.Fatalf("rejected: %v", rej)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refusal was never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A later success clears the refusal; the stream is never involved.
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	c.receiveResource(ctlResource(2))
	for {
		c.mu.Lock()
		n := len(c.rejectedResources)
		c.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refusal was never cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestResourceReports(t *testing.T) {
	ctx := context.Background()
	f := &fakeResources{statuses: map[string]*fleetv1.ResourceStatus{
		"main":  {EnvironmentId: "e1", Name: "main", ObservedGeneration: 1, Phase: "ready"},
		"cache": {EnvironmentId: "e1", Name: "cache", ObservedGeneration: 2, Phase: "deleted"},
	}}
	c := resClient(f)
	var sent []*fleetv1.AgentMessage
	send := func(m *fleetv1.AgentMessage) error { sent = append(sent, m); return nil }

	// Full report after Hello, even when empty (the control plane forgets what is missing).
	if err := c.reportResourcesAll(ctx, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || !sent[0].GetResourceReport().GetFull() || len(sent[0].GetResourceReport().GetResources()) != 2 {
		t.Fatalf("full: %+v", sent)
	}
	if len(f.reported) != 2 {
		t.Errorf("the deleted resource must be marked as told: %+v", f.reported)
	}

	// Deltas carry only what changed.
	sent = nil
	if err := c.reportResourcesChanged(ctx, send); err != nil || len(sent) != 0 {
		t.Fatalf("nothing changed: %v %+v", err, sent)
	}
	c.ResourceChanged("e1", "main")
	if err := c.reportResourcesChanged(ctx, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].GetResourceReport().GetFull() || len(sent[0].GetResourceReport().GetResources()) != 1 || sent[0].GetResourceReport().GetResources()[0].GetName() != "main" {
		t.Fatalf("delta: %+v", sent)
	}

	// A refusal of a newer generation wins over what the cluster has, in both.
	c.rejectedResources[key{"e1", "main"}] = &fleetv1.ResourceStatus{EnvironmentId: "e1", Name: "main", ObservedGeneration: 5, Phase: "failed", Message: "no"}
	sent = nil
	c.ResourceChanged("e1", "main")
	_ = c.reportResourcesChanged(ctx, send)
	if got := sent[0].GetResourceReport().GetResources()[0]; got.GetPhase() != "failed" || got.GetObservedGeneration() != 5 {
		t.Errorf("delta: %v", got)
	}
	sent = nil
	_ = c.reportResourcesAll(ctx, send)
	for _, s := range sent[0].GetResourceReport().GetResources() {
		if s.GetName() == "main" && s.GetPhase() != "failed" {
			t.Errorf("full: %v", s)
		}
	}

	// Without Resources, nothing is sent.
	sent = nil
	if err := resClient(nil).reportResourcesAll(ctx, send); err != nil || len(sent) != 0 {
		t.Errorf("no resources: %v %+v", err, sent)
	}

	// A broken stream is the caller's.
	if err := c.reportResourcesAll(ctx, func(*fleetv1.AgentMessage) error { return errors.New("closed") }); err == nil {
		t.Error("send errors end the stream")
	}

	// A set too big for one message goes in partial chunks: a "full" chunk
	// would make the control plane forget the statuses of the others.
	many := make([]*fleetv1.ResourceStatus, maxReportResources+1)
	for i := range many {
		many[i] = &fleetv1.ResourceStatus{EnvironmentId: "e1", Name: fmt.Sprintf("r%d", i), ObservedGeneration: 1, Phase: "ready"}
	}
	sent = nil
	if err := c.sendResources(ctx, send, true, many); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("chunks: %d", len(sent))
	}
	for _, m := range sent {
		if m.GetResourceReport().GetFull() {
			t.Error("a chunk must not claim to be the full set")
		}
	}
}
