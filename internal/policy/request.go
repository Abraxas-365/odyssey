package policy

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// FromRequest is the Input a governed domain's Request stands for: who acts
// is the actor on the context (the token's permissions), and a nil Scan is
// "no scan yet" (scan.known false).
func FromRequest(a actor.Actor, r gate.Request) Input {
	in := Input{
		Action:   r.Action,
		Project:  ProjectInput{Tier: project.Tier(r.Tier), Slug: r.ProjectSlug},
		Env:      EnvInput{Name: r.Environment},
		Access:   AccessInput{Mode: r.AccessMode},
		Actor:    ActorInput{ID: a.ID(), Permissions: a.Permissions},
		Resource: ResourceInput{Kind: r.ResourceKind},
	}
	if r.Scan != nil {
		in.Scan = ScanInput{Known: true, Checked: true, Critical: r.Scan.Critical, High: r.Scan.High}
	} else {
		in.Scan.Checked = r.ScanChecked
	}
	return in
}

// VerdictOf words a Decision for the governed domains.
func VerdictOf(d Decision) gate.Verdict {
	v := gate.Verdict{Effect: d.Effect, Approvers: d.Approvers}
	for _, r := range d.Rules {
		v.Rules = append(v.Rules, gate.Rule{ID: r.ID.String(), Name: r.Name})
	}
	return v
}

// Ask is what every governed domain's policy adapter does: ask the
// evaluator about the request, as the actor on ctx. The adapters differ only
// in the port they implement (each domain owns its own).
func Ask(ctx context.Context, e Evaluator, organization identity.OrganizationID, r gate.Request) (gate.Verdict, error) {
	d, err := e.Evaluate(ctx, organization, FromRequest(actor.From(ctx), r))
	if err != nil {
		return gate.Verdict{}, err
	}
	return VerdictOf(d), nil
}
