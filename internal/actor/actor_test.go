package actor_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

func TestActorTravelsOnTheContext(t *testing.T) {
	if got := actor.From(context.Background()); !got.User.IsZero() || got.Can("x") {
		t.Fatalf("no actor must be the zero value: %+v", got)
	}
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	ctx := actor.With(context.Background(), actor.Actor{User: user, Permissions: []string{"a", "b"}})
	got := actor.From(ctx)
	if got.User != user || !got.Can("b") || got.Can("c") {
		t.Fatalf("got %+v", got)
	}
}
