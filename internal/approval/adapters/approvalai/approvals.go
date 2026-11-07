// Package approvalai is the ai.Approvals adapter on the approval domain: ai
// holds and redeems approvals through its own port and never imports
// approval. Executor re-runs what ai held, as the requester, once it is
// approved.
package approvalai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

type Approvals struct{ commands approval.Commands }

func New(commands approval.Commands) *Approvals { return &Approvals{commands: commands} }

var _ ai.Approvals = (*Approvals)(nil)

func (a *Approvals) Hold(ctx context.Context, org identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	return approval.Hold(ctx, a.commands, org, h)
}

func (a *Approvals) Redeem(ctx context.Context, org identity.OrganizationID, request string, g gate.Grant) error {
	return approval.Redeem(ctx, a.commands, org, request, g)
}

// Executor re-runs the action ai holds.
type Executor struct{ ai ai.Commands }

func NewExecutor(commands ai.Commands) *Executor { return &Executor{ai: commands} }

var _ approval.Executor = (*Executor)(nil)

func (e *Executor) Handles(r approval.Request) bool { return approval.OpOf(r) == ai.OpEnable }

// Execute turns the AI on as the requester (the reactor put them and the
// approval's mark on ctx): the service redeems the approval where it would
// have held the action.
func (e *Executor) Execute(ctx context.Context, r approval.Request) error {
	if approval.OpOf(r) != ai.OpEnable {
		return fmt.Errorf("approvalai: %q is not an action ai re-runs", approval.OpOf(r))
	}
	var p ai.EnableApproval
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return err
	}
	out, err := e.ai.Enable(ctx, r.Organization, r.Project, r.Environment, ai.Enable{Services: p.Services, RequestedBy: r.RequestedBy})
	if err == nil && out.Held() {
		return fmt.Errorf("the approved action was held for approval again")
	}
	return err
}
