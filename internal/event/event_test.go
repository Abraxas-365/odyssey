package event_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/google/uuid"
)

type created struct {
	Name string `json:"name"`
}

func TestStampCarriesProvenance(t *testing.T) {
	org := identity.NewOrganizationID()
	e := event.New("widget.created", 1, org, identity.UserID{}, "widget/1", "Widget creado", created{Name: "a"})

	alone := e
	event.Stamp(context.Background(), &alone)
	if alone.CorrelationID != e.ID.String() || alone.CausationID != uuid.Nil {
		t.Fatalf("an event without context starts its own chain: %+v", alone.Meta)
	}

	ctx := event.WithMeta(context.Background(), event.Meta{CorrelationID: "req-1", Client: "claude-code"})
	first := e
	event.Stamp(ctx, &first)
	if first.CorrelationID != "req-1" || first.Client != "claude-code" {
		t.Fatalf("request meta: %+v", first.Meta)
	}

	reaction := event.New("other.reacted", 1, org, identity.UserID{}, "other/1", "Reacción", created{})
	event.Stamp(event.WithCause(context.Background(), first), &reaction)
	if reaction.CorrelationID != "req-1" || reaction.CausationID != first.ID || reaction.Client != "claude-code" {
		t.Fatalf("caused event: %+v", reaction.Meta)
	}
}

func TestBusRoutesAndRejectsMistakes(t *testing.T) {
	b := event.NewBus()
	var got []string
	event.Subscribe(b, "widget.created", 1, "audit_like.widgets", func(_ context.Context, _ event.Event, p created) error {
		got = append(got, p.Name)
		return nil
	})
	b.Add(event.Subscription{Name: "everything", Handle: func(context.Context, event.Event) error { return nil }})

	if n := len(b.For("widget.created")); n != 2 {
		t.Fatalf("widget.created goes to %d subscribers", n)
	}
	if n := len(b.For("widget.deleted")); n != 1 {
		t.Fatalf("widget.deleted goes to %d subscribers", n)
	}
	org := identity.NewOrganizationID()
	sub := b.For("widget.created")[0]
	_ = sub.Handle(context.Background(), event.New("widget.created", 1, org, identity.UserID{}, "w/1", "s", created{Name: "v1"}))
	_ = sub.Handle(context.Background(), event.New("widget.created", 2, org, identity.UserID{}, "w/1", "s", created{Name: "v2"}))
	if len(got) != 1 || got[0] != "v1" {
		t.Fatalf("typed subscriber must only see its version: %v", got)
	}
	if c := b.Catalog(); len(c["*"]) != 1 || len(c["widget.created"]) != 1 {
		t.Fatalf("catalog %v", c)
	}

	mustPanic(t, "duplicate", func() { b.Add(event.Subscription{Name: "everything", Handle: sub.Handle}) })
	mustPanic(t, "bad name", func() { b.Add(event.Subscription{Name: "Bad Name", Handle: sub.Handle}) })
	mustPanic(t, "bad type", func() { b.Add(event.Subscription{Name: "x", Types: []string{"nodot"}, Handle: sub.Handle}) })
	mustPanic(t, "nil handler", func() { b.Add(event.Subscription{Name: "y"}) })
	b.Seal()
	mustPanic(t, "sealed", func() { b.Add(event.Subscription{Name: "z", Handle: sub.Handle}) })
	mustPanic(t, "bad event type", func() { event.New("Bad", 1, org, identity.UserID{}, "", "", created{}) })
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	fn()
}
