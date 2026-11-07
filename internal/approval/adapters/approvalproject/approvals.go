// Package approvalproject is the project.Approvals adapter on the approval
// domain: project holds and redeems approvals (a change of the app's
// services) through its own port and never imports approval. Executor
// re-runs what project held, as the requester, once it is approved.
package approvalproject

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

type Approvals struct{ commands approval.Commands }

func New(commands approval.Commands) *Approvals { return &Approvals{commands: commands} }

var _ project.Approvals = (*Approvals)(nil)

func (a *Approvals) Hold(ctx context.Context, org identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	return approval.Hold(ctx, a.commands, org, h)
}

func (a *Approvals) Redeem(ctx context.Context, org identity.OrganizationID, request string, g gate.Grant) error {
	return approval.Redeem(ctx, a.commands, org, request, g)
}

// Executor re-runs the action project holds.
type Executor struct{ projects project.Commands }

func NewExecutor(commands project.Commands) *Executor { return &Executor{projects: commands} }

var _ approval.Executor = (*Executor)(nil)

func (e *Executor) Handles(r approval.Request) bool { return approval.OpOf(r) == project.OpSetServices }

// Execute stores the approved services as the requester (the reactor put
// them and the approval's mark on ctx): the service redeems the approval
// where it would have held the change.
func (e *Executor) Execute(ctx context.Context, r approval.Request) error {
	if approval.OpOf(r) != project.OpSetServices {
		return fmt.Errorf("approvalproject: %q is not an action project re-runs", approval.OpOf(r))
	}
	var p project.SetServicesApproval
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return err
	}
	out, err := e.projects.SetServices(ctx, r.Organization, r.Project, project.SetServices{Items: p.Items, UpdatedBy: r.RequestedBy})
	if err == nil && out.Held() {
		return fmt.Errorf("the approved action was held for approval again")
	}
	return err
}
