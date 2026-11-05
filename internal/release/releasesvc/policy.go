package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// UsePolicy wires IT's rules (the policyrelease adapter). Until it is wired
// every governed action is refused: nothing is allowed by accident.
func (s *Service) UsePolicy(p release.Policy) { s.policy = p }

// UseApprovals wires where approvals are held (the approvalrelease adapter).
// Without it an action a rule wants approved fails closed.
func (s *Service) UseApprovals(a release.Approvals) { s.approvals = a }

// authorize is the one seam where release asks IT's rules before a DIRECT
// action (set_access, connect_resource): nil, nil lets it go on; a Pending
// means a rule wants IT's approval, the request was stored and nothing may
// change (the caller answers 202); an error is POLICY_DENIED (403) or a
// failure. The check runs before anything is stored, as the actor on ctx.
//
// When the approval comes, the action runs again as the requester with the
// approval's mark on ctx (gate.WithApproval) and passes here by redeeming it
// (gate.Enforce): an approval is good for one use, for exactly this action.
//
// Deploys and rollbacks are not judged here but by the deploy workflow's
// authorize step (authorizeStep), which holds the deployment where it is.
func (s *Service) authorize(ctx context.Context, p project.Project, e project.Environment, request gate.Request, hold gate.Hold) (*gate.Pending, error) {
	request.Tier, request.ProjectSlug, request.Environment = string(p.Tier), p.Slug, e.Slug
	hold.Project, hold.Environment = p.ID.String(), e.ID.String()
	return gate.Enforce(ctx, p.Organization, s.policy, s.approvals, request, hold)
}

// denyOnly is the cheap check a deploy or rollback gets when it is asked
// for: a refusal by IT's rules answers 403 at once (so a refused deploy
// uploads nothing), while a rule that wants approval lets the request in: the
// deployment is created and its workflow's authorize step holds it (the
// deployment waits, visibly, for IT). The same rules are judged again there,
// as the requester, so a rule that changed meanwhile applies.
func (s *Service) denyOnly(ctx context.Context, p project.Project, e project.Environment, action string) error {
	return gate.DenyOnly(ctx, p.Organization, s.policy, gate.Request{Action: action, Tier: string(p.Tier), ProjectSlug: p.Slug, Environment: e.Slug})
}

func where(p project.Project, e project.Environment) string {
	return "«" + p.Name + "» (" + e.Slug + ")"
}

// accessSummary is the sentence IT reads for a held set_access.
func accessSummary(p project.Project, e project.Environment, mode release.AccessMode) string {
	switch mode {
	case release.AccessPublic:
		return "Hacer pública " + where(p, e) + ": cualquiera con el enlace podrá abrirla"
	case release.AccessOrganization:
		return "Abrir " + where(p, e) + " a toda la empresa"
	}
	return "Dejar " + where(p, e) + " solo para su equipo"
}

func accessPayload(mode release.AccessMode) release.SetAccessApproval {
	return release.SetAccessApproval{Op: release.OpSetAccess, Mode: mode}
}
