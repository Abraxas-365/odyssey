package event

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Handler reacts to one event. It runs inside the delivery transaction (the
// repositories it calls join it through txn.Q), so its writes and the
// "delivered" mark commit together. It must be idempotent: a delivery can be
// retried after a failure the database did not see (a crash, a network call).
type Handler func(ctx context.Context, e Event) error

// Subscription is one named reaction.
type Subscription struct {
	// Name is unique and stable ("audit", "release.deploy_on_build"): it keys
	// the delivery rows, so renaming it drops pending deliveries.
	Name string
	// Types are the event types received; empty means every type.
	Types []string
	// Ordered delivers one organization's events strictly in publish order
	// (the hash-chained audit trail). A failing event holds back the rest of
	// that organization until it succeeds; other organizations continue.
	Ordered bool
	Handle  Handler
}

func (s Subscription) receives(typ string) bool {
	return len(s.Types) == 0 || slices.Contains(s.Types, typ)
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Bus is the subscription registry. The composition root fills it at startup
// and seals it; the outbox reads it to create one delivery per subscriber.
type Bus struct {
	mu     sync.RWMutex
	subs   []Subscription
	sealed bool
}

// NewBus returns an empty registry.
func NewBus() *Bus { return &Bus{} }

// Add registers s. It panics on a programming error: after Seal, an invalid
// or duplicated name, an unknown type, or a nil handler.
func (b *Bus) Add(s Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.sealed:
		panic("event: subscription " + s.Name + " added after Seal")
	case !namePattern.MatchString(s.Name):
		panic(fmt.Sprintf("event: invalid subscription name %q", s.Name))
	case s.Handle == nil:
		panic("event: subscription " + s.Name + " has no handler")
	}
	for _, typ := range s.Types {
		if !ValidType(typ) {
			panic(fmt.Sprintf("event: subscription %s: invalid type %q", s.Name, typ))
		}
	}
	for _, have := range b.subs {
		if have.Name == s.Name {
			panic("event: duplicate subscription " + s.Name)
		}
	}
	b.subs = append(b.subs, s)
}

// Seal forbids further subscriptions; call it before dispatching.
func (b *Bus) Seal() {
	b.mu.Lock()
	b.sealed = true
	b.mu.Unlock()
}

// For lists the subscriptions that receive typ.
func (b *Bus) For(typ string) []Subscription {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []Subscription
	for _, s := range b.subs {
		if s.receives(typ) {
			out = append(out, s)
		}
	}
	return out
}

// All lists every subscription, sorted by name.
func (b *Bus) All() []Subscription {
	b.mu.RLock()
	out := slices.Clone(b.subs)
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Catalog maps every subscribed type to its subscribers ("*" for the
// subscriptions that receive everything), for docs and a golden test.
func (b *Bus) Catalog() map[string][]string {
	out := map[string][]string{}
	for _, s := range b.All() {
		if len(s.Types) == 0 {
			out["*"] = append(out["*"], s.Name)
		}
		for _, typ := range s.Types {
			out[typ] = append(out[typ], s.Name)
		}
	}
	return out
}

// Subscribe registers a typed handler for one event type and payload
// version; events of another version are acknowledged and skipped.
func Subscribe[P any](b *Bus, typ string, version int, name string, h func(ctx context.Context, e Event, p P) error) {
	b.Add(Subscription{Name: name, Types: []string{typ}, Handle: func(ctx context.Context, e Event) error {
		if e.Version != version {
			return nil
		}
		p, err := Decode[P](e)
		if err != nil {
			return errx.Wrap(err, fmt.Sprintf("decode %s v%d", e.Type, e.Version), errx.TypeInternal)
		}
		return h(ctx, e, p)
	}})
}
