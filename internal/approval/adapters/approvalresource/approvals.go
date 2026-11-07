// Package approvalresource is the resource.Approvals adapter on the approval
// domain: resource holds and redeems approvals through its own port and
// never imports approval. Executor re-runs what resource held, as the
// requester, once it is approved.
package approvalresource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

type Approvals struct{ commands approval.Commands }

func New(commands approval.Commands) *Approvals { return &Approvals{commands: commands} }

var _ resource.Approvals = (*Approvals)(nil)

func (a *Approvals) Hold(ctx context.Context, org identity.OrganizationID, h gate.Hold) (gate.Pending, error) {
	return approval.Hold(ctx, a.commands, org, h)
}

func (a *Approvals) Redeem(ctx context.Context, org identity.OrganizationID, request string, g gate.Grant) error {
	return approval.Redeem(ctx, a.commands, org, request, g)
}

// Executor re-runs the actions resource holds.
type Executor struct{ resource resource.Commands }

func NewExecutor(commands resource.Commands) *Executor { return &Executor{resource: commands} }

var _ approval.Executor = (*Executor)(nil)

func (e *Executor) Handles(r approval.Request) bool {
	switch approval.OpOf(r) {
	case resource.OpDelete, resource.OpConnect, resource.OpCreate:
		return true
	}
	return false
}

// Execute runs the approved action as the requester (the reactor put them and
// the approval's mark on ctx): the service redeems the approval where it
// would have held the action.
func (e *Executor) Execute(ctx context.Context, r approval.Request) error {
	switch approval.OpOf(r) {
	case resource.OpDelete:
		var p resource.DeleteApproval
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		out, err := e.resource.Delete(ctx, r.Organization, r.Project, r.Environment, p.Name, resource.Delete{RequestedBy: r.RequestedBy})
		return finished(out.Outcome, err)
	case resource.OpConnect:
		var p resource.ConnectApproval
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		out, err := e.resource.Connect(ctx, r.Organization, r.Project, r.Environment,
			resource.Connect{Name: p.Name, Prefix: p.Prefix, Services: p.Services, RequestedBy: r.RequestedBy})
		return finished(out.Outcome, err)
	case resource.OpCreate:
		var p resource.CreateApproval
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return err
		}
		out, err := e.resource.Create(ctx, r.Organization, r.Project, r.Environment, resource.Create{Name: p.Name,
			Kind: resourcekind.Kind(p.Kind), StorageGi: p.StorageGi, Connect: &resource.ConnectOptions{Prefix: p.Prefix, Services: p.Services},
			RequestedBy: r.RequestedBy})
		return finished(out.Outcome, err)
	}
	return fmt.Errorf("approvalresource: %q is not an action resource re-runs", approval.OpOf(r))
}

// finished fails an approved action that held itself again: the approval did
// not cover it, which would otherwise loop.
func finished(o gate.Outcome, err error) error {
	if err == nil && o.Held() {
		return fmt.Errorf("the approved action was held for approval again")
	}
	return err
}
