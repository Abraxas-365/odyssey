// Package eventtest has test doubles for the event package: a recording
// publisher for service unit tests and golden contract files for payloads.
package eventtest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
)

// Recorder is a Publisher that keeps what was published (stamped like the
// real outbox). It does not require a transaction.
type Recorder struct {
	mu     sync.Mutex
	events []event.Event
	// Err, when set, is returned by Publish (to test rollback paths).
	Err error
}

var _ event.Publisher = (*Recorder)(nil)

func (r *Recorder) Publish(ctx context.Context, events ...event.Event) error {
	if r.Err != nil {
		return r.Err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range events {
		event.Stamp(ctx, &e)
		r.events = append(r.events, e)
	}
	return nil
}

// Events returns everything published so far.
func (r *Recorder) Events() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.events...)
}

// Types lists the published event types, in order.
func (r *Recorder) Types() []string {
	var out []string
	for _, e := range r.Events() {
		out = append(out, e.Type)
	}
	return out
}

// Reset forgets what was published.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

// Golden compares e's payload with testdata/events/<type>.v<version>.json
// (the public contract consumers rely on). Run with UPDATE_GOLDEN=1 to write
// it. A change to an existing file is a breaking change: bump the version.
func Golden(t *testing.T, e event.Event) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, e.Payload, "", "  "); err != nil {
		t.Fatalf("payload of %s is not JSON: %v", e.Type, err)
	}
	pretty.WriteByte('\n')
	file := filepath.Join("testdata", "events", e.Type+".v"+itoa(e.Version)+".json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("missing contract %s (run with UPDATE_GOLDEN=1 to create it): %v", file, err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Fatalf("payload of %s v%d changed (breaking for consumers: publish a new version instead)\nwant:\n%s\ngot:\n%s",
			e.Type, e.Version, want, pretty.Bytes())
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
