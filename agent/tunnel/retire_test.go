package tunnel

import (
	"context"
	"testing"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
)

// fakeDesired runs nothing: the cluster has no app left to read a status of.
type fakeDesired struct{}

func (fakeDesired) Apply(context.Context, *fleetv1.App) error { return nil }
func (fakeDesired) Status(context.Context, string, string) (*fleetv1.AppStatus, error) {
	return nil, nil
}
func (fakeDesired) Statuses(context.Context) ([]*fleetv1.AppStatus, error) { return nil, nil }
func (fakeDesired) Capabilities(context.Context) *fleetv1.Capabilities {
	return &fleetv1.Capabilities{}
}

func TestARetiredAppIsReportedDeletedOnceSent(t *testing.T) {
	ctx := context.Background()
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Desired: fakeDesired{}, Log: logr.Discard()})
	k := key{"e1", "portal"}
	retired := &fleetv1.App{EnvironmentId: "e1", Name: "portal", Revision: 7, Deleted: true}
	c.applied(k, retired, nil)

	var sent []*fleetv1.AppStatus
	send := func(m *fleetv1.AgentMessage) error { sent = append(sent, m.GetReport().GetApps()...); return nil }
	if err := c.reportChanged(ctx, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].GetPhase() != PhaseDeleted || sent[0].GetObservedRevision() != 7 || sent[0].GetName() != "portal" {
		t.Fatalf("the retirement must be reported deleted at its revision: %+v", sent)
	}
	// Sent: forgotten (the control plane forgets the app on it; a lost report
	// is made again from the retirement it resends on the next tunnel).
	sent = nil
	if err := c.reportAll(ctx, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Errorf("a sent retirement must not be reported again: %+v", sent)
	}

	// A failed send keeps it for the next report.
	c.applied(k, retired, nil)
	failing := func(*fleetv1.AgentMessage) error { return context.Canceled }
	if err := c.reportChanged(ctx, failing); err == nil {
		t.Fatal("the send error must be returned")
	}
	sent = nil
	if err := c.reportAll(ctx, send); err != nil || len(sent) != 1 || sent[0].GetPhase() != PhaseDeleted {
		t.Errorf("an unsent retirement must stay reported: %v %+v", err, sent)
	}

	// A newer revision applied afterwards replaces it.
	c.applied(k, retired, nil)
	c.applied(k, &fleetv1.App{EnvironmentId: "e1", Name: "portal", Revision: 8}, nil)
	if c.rejected[k] != nil {
		t.Errorf("a newer revision must clear the retirement: %v", c.rejected[k])
	}
}
