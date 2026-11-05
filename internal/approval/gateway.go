package approval

import (
	"context"
	"encoding/json"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// ViewerOf is who is asking, from the actor on the context (the REST
// middleware and the MCP gateway put the token's user and permissions there).
func ViewerOf(ctx context.Context) Viewer {
	a := actor.From(ctx)
	return Viewer{User: a.User, Permissions: a.Permissions}
}

// The three governed domains hold and redeem through the same two functions;
// each domain's adapter (approvalrelease, approvalresource, approvalai) only
// fits them to the domain's own Approvals port.

// Hold opens the request for a held action as the actor on ctx.
func Hold(ctx context.Context, c Commands, org identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	pid, err := project.ParseID(h.Project)
	if err != nil {
		return gate.Pending{}, err
	}
	eid, err := project.ParseEnvironmentID(h.Environment)
	if err != nil {
		return gate.Pending{}, err
	}
	a := actor.From(ctx)
	rules := make([]Rule, len(h.Verdict.Rules))
	for i, r := range h.Verdict.Rules {
		rules[i] = Rule{ID: r.ID, Name: r.Name}
	}
	r, err := c.Open(ctx, org, Open{Action: h.Action, Project: pid, Environment: eid, Summary: h.Summary,
		Payload: h.Payload, Reason: ReasonOf(rules), Rules: rules, Approvers: h.Verdict.Approvers,
		Run: h.Run, RunStep: h.RunStep,
		Requester: Requester{User: a.User, System: a.System, Permissions: a.Permissions}})
	if err != nil {
		return gate.Pending{}, err
	}
	return gate.NewPending(r.ID.String()), nil
}

// Redeem presents the grant of an approved request.
func Redeem(ctx context.Context, c Commands, org identity.OrganizationID, request string, g gate.Grant) error {
	id, err := ParseID(request)
	if err != nil {
		return ErrNotUsable()
	}
	pid, err := project.ParseID(g.Project)
	if err != nil {
		return err
	}
	eid, err := project.ParseEnvironmentID(g.Environment)
	if err != nil {
		return err
	}
	return c.Redeem(ctx, org, id, Grant{Action: g.Action, Project: pid, Environment: eid, Payload: g.Payload})
}

// OpOf is the "op" the payload of r names ("" when it names none): which
// action of its domain to run again.
func OpOf(r Request) string {
	var p struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(r.Payload, &p) != nil {
		return ""
	}
	return p.Op
}

// ConsumeRequest uses up the approved request (for a run released by it).
func ConsumeRequest(ctx context.Context, c Commands, org identity.OrganizationID, request string) error {
	id, err := ParseID(request)
	if err != nil {
		return ErrNotUsable()
	}
	return c.Consume(ctx, org, id)
}
