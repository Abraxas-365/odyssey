package approval_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

var (
	org   = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user  = identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	admin = identity.MustParseUserID("00000000-0000-4000-8000-000000000003")
)

func sample() approval.Request {
	return approval.Request{ID: approval.MustParseID("00000000-0000-4000-8000-0000000000aa"), Organization: org,
		Action: "set_access", Project: project.MustParseID("00000000-0000-4000-8000-0000000000bb"),
		Environment: project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000cc"),
		Summary:     "Hacer pública «tienda» (producción)", RequestedBy: user, Reason: "La regla de TI «Acceso público» pide aprobación.",
		Rules:     []approval.Rule{{ID: "00000000-0000-4000-8000-0000000000dd", Name: "Acceso público"}},
		Approvers: []string{"odyssey:approvals:decide"}, Status: approval.StatusPending,
		ExpiresAt: time.Date(2026, 1, 8, 10, 0, 0, 0, time.UTC), Run: "run-1"}
}

// The payloads are the public contract: see testdata/events.
func TestEventContracts(t *testing.T) {
	r := sample()
	for _, e := range []event.Event{
		approval.NewRequested(r),
		approval.NewApproved(r, admin, "va"),
		approval.NewRejected(r, admin, "no por ahora"),
		approval.NewExpired(r),
		approval.NewCancelled(r, user),
		approval.NewConsumed(r),
		approval.NewExecutionFailed(r, "El recurso ya no existe."),
	} {
		eventtest.Golden(t, e)
	}
}

func TestEventsNameTheSystemRequesterAndTheReplacement(t *testing.T) {
	r := sample()
	r.RequestedBy, r.RequestedSystem = identity.UserID{}, "github:ana"
	var p approval.Requested
	if err := json.Unmarshal(approval.NewRequested(r).Payload, &p); err != nil || p.RequestedBy != "github:ana" {
		t.Fatalf("%+v %v", p, err)
	}
	if e := approval.NewCancelled(r, identity.UserID{}); e.Summary != "Se canceló la solicitud (la reemplazó otra): "+r.Summary {
		t.Fatalf("summary %q", e.Summary)
	}
}

func TestViewerRules(t *testing.T) {
	r := sample()
	approver := approval.Viewer{User: admin, Permissions: []string{"odyssey:approvals:decide"}}
	if !approver.Can(r) || !approver.Sees(r) {
		t.Fatal("an approver decides and sees")
	}
	self := approval.Viewer{User: user, Permissions: []string{"odyssey:approvals:decide"}}
	if self.Can(r) || !self.Sees(r) {
		t.Fatal("the requester sees their request but never decides it")
	}
	other := approval.Viewer{User: admin}
	if other.Can(r) || other.Sees(r) {
		t.Fatal("a plain user neither sees nor decides another's request")
	}
	if (approval.Viewer{Permissions: []string{"odyssey:approvals:decide"}}).Can(r) {
		t.Fatal("no user, no decision")
	}
}

func TestOpenDefaultsApproversAndKeysAreStable(t *testing.T) {
	o := approval.Open{Action: "x"}.Normalized()
	if len(o.Approvers) != 1 || o.Approvers[0] != "odyssey:approvals:decide" {
		t.Fatalf("approvers %v", o.Approvers)
	}
	a, _ := approval.Canonical(map[string]any{"b": 1, "a": 2})
	b, _ := approval.Canonical(json.RawMessage(`{"a": 2, "b": 1}`))
	if string(a) != string(b) || !approval.SamePayload(a, b) {
		t.Fatalf("%s %s", a, b)
	}
	in := approval.Open{Action: "set_access", Environment: sample().Environment, Requester: approval.Requester{User: user}}
	if approval.KeyOf(in, a) != approval.KeyOf(in, b) || approval.KeyOf(in, a) == approval.KeyOf(approval.Open{Action: "set_access", Environment: sample().Environment, Requester: approval.Requester{User: admin}}, a) {
		t.Fatal("key must follow who, what and where")
	}
	in.Run, in.RunStep = "r", "authorize"
	if approval.KeyOf(in, a) != "run:r/authorize" {
		t.Fatalf("run key %q", approval.KeyOf(in, a))
	}
}
