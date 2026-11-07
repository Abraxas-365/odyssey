// Package approvalrelease is the release.Approvals adapter on the approval
// domain: release holds and redeems approvals through its own port and never
// imports approval. Executor re-runs what release held, as the requester,
// once it is approved.
package approvalrelease

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Approvals struct{ commands approval.Commands }

func New(commands approval.Commands) *Approvals { return &Approvals{commands: commands} }

var _ release.Approvals = (*Approvals)(nil)

func (a *Approvals) Hold(ctx context.Context, org identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	return approval.Hold(ctx, a.commands, org, h)
}

func (a *Approvals) Redeem(ctx context.Context, org identity.OrganizationID, request string, g gate.Grant) error {
	return approval.Redeem(ctx, a.commands, org, request, g)
}

func (a *Approvals) Consume(ctx context.Context, org identity.OrganizationID, request string) error {
	return approval.ConsumeRequest(ctx, a.commands, org, request)
}

func (a *Approvals) CancelRun(ctx context.Context, org identity.OrganizationID, run, step string) error {
	return a.commands.CancelRun(ctx, org, run, step)
}

// Executor re-runs the direct actions release holds. A held deployment is not
// re-run: its workflow waits for the decision and carries on by itself.
type Executor struct{ release release.Commands }

func NewExecutor(commands release.Commands) *Executor { return &Executor{release: commands} }

var _ approval.Executor = (*Executor)(nil)

func (e *Executor) Handles(r approval.Request) bool {
	switch approval.OpOf(r) {
	case release.OpSetAccess, release.OpConnectResource:
		return true
	}
	return false
}

// Execute runs the approved action as the requester (the reactor put them and
// the approval's mark on ctx): the service redeems the approval where it
// would have held the action.
func (e *Executor) Execute(ctx context.Context, r approval.Request) error {
	switch approval.OpOf(r) {
	case release.OpSetAccess:
		var p release.SetAccessApproval
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		out, err := e.release.SetAccess(ctx, r.Organization, r.Project, r.Environment, release.SetAccess{Mode: p.Mode, RequestedBy: r.RequestedBy})
		return finished(out.Outcome, err)
	case release.OpConnectResource:
		var p release.ConnectResourceApproval
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		out, err := e.release.ConnectResource(ctx, r.Organization, r.Project, r.Environment,
			release.ConnectResource{Name: p.Name, Prefix: p.Prefix, Services: p.Services, RequestedBy: r.RequestedBy})
		return finished(out.Outcome, err)
	}
	return fmt.Errorf("approvalrelease: %q is not an action release re-runs", approval.OpOf(r))
}

// finished fails an approved action that held itself again: the approval did
// not cover it, which would otherwise loop.
func finished(o gate.Outcome, err error) error {
	if err == nil && o.Held() {
		return fmt.Errorf("the approved action was held for approval again")
	}
	return err
}
