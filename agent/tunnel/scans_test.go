package tunnel

import (
	"context"
	"sync"
	"testing"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/go-logr/logr"
)

// scanBuilds is Builds (doing nothing) and Scans.
type scanBuilds struct {
	mu    sync.Mutex
	scans map[string]*fleetv1.ScanStatus
}

func (*scanBuilds) Start(context.Context, *fleetv1.Build) (*fleetv1.BuildStatus, error) {
	return nil, nil
}
func (*scanBuilds) Cancel(context.Context, string) error { return nil }
func (*scanBuilds) Status(context.Context, string) (*fleetv1.BuildStatus, error) {
	return nil, nil
}
func (*scanBuilds) Statuses(context.Context) ([]*fleetv1.BuildStatus, error) { return nil, nil }
func (*scanBuilds) FollowLogs(context.Context, string, func([]*fleetv1.LogLine) error) error {
	return nil
}
func (f *scanBuilds) Scan(_ context.Context, id string) (*fleetv1.ScanStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans[id], nil
}
func (f *scanBuilds) Scans(context.Context) ([]*fleetv1.ScanStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fleetv1.ScanStatus
	for _, s := range f.scans {
		out = append(out, s)
	}
	return out, nil
}

func TestReportScans(t *testing.T) {
	ctx := context.Background()
	b := &scanBuilds{scans: map[string]*fleetv1.ScanStatus{}}
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Builds: b, Log: logr.Discard()})
	var sent []*fleetv1.ScanStatus
	send := func(m *fleetv1.AgentMessage) error {
		sent = append(sent, m.GetScanReport().GetScans()...)
		return nil
	}

	// Nothing ended: nothing is sent.
	c.ScanChanged("b1")
	if err := c.reportScans(ctx, send, false); err != nil || len(sent) != 0 {
		t.Fatalf("%v %v", sent, err)
	}

	// It ends: sent once, even if marked changed again.
	b.scans["b1"] = &fleetv1.ScanStatus{BuildId: "b1", ImageDigest: "sha256:x", Critical: 1}
	c.ScanChanged("b1")
	for range 2 {
		if err := c.reportScans(ctx, send, false); err != nil {
			t.Fatal(err)
		}
		c.ScanChanged("b1")
	}
	if len(sent) != 1 || sent[0].GetCritical() != 1 {
		t.Fatalf("sent %v", sent)
	}

	// A fresh tunnel (all) sends every scan the cluster still holds.
	b.scans["b2"] = &fleetv1.ScanStatus{BuildId: "b2", Error: "no network"}
	sent = nil
	if err := c.reportScans(ctx, send, true); err != nil || len(sent) != 2 {
		t.Fatalf("all: %v %v", sent, err)
	}
	// ... and what changed since is compared with what that sent.
	sent = nil
	c.ScanChanged("b2")
	if err := c.reportScans(ctx, send, false); err != nil || len(sent) != 0 {
		t.Fatalf("unchanged: %v %v", sent, err)
	}
}

// An agent whose Builds does not scan reports no scans.
func TestNoScansWithoutTheInterface(t *testing.T) {
	var plain struct{ Builds }
	c := New(Options{Store: NewMemoryStore(Enrollment{}), Builds: plain, Log: logr.Discard()})
	called := false
	if err := c.reportScans(context.Background(), func(*fleetv1.AgentMessage) error { called = true; return nil }, true); err != nil || called {
		t.Fatalf("%v %v", called, err)
	}
}
