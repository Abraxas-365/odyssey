package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
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

// check is authorize's verdict alone (nothing is held): for an operation
// another domain judges as a whole (JudgeConnect).
func (s *Service) check(ctx context.Context, p project.Project, e project.Environment, request gate.Request) (gate.Verdict, error) {
	if s.policy == nil {
		return gate.Verdict{}, errx.Internal("IT's rules are not wired")
	}
	request.Tier, request.ProjectSlug, request.Environment = string(p.Tier), p.Slug, e.Slug
	return s.policy.Check(ctx, p.Organization, request)
}

// denyOnly is the cheap check a deploy or rollback gets when it is asked
// for: a refusal by IT's rules answers 403 at once (so a refused deploy
// uploads nothing), while a rule that wants approval lets the request in: the
// deployment is created and its workflow's authorize step holds it (the
// deployment waits, visibly, for IT). The same rules are judged again there,
// as the requester, so a rule that changed meanwhile applies.
func (s *Service) denyOnly(ctx context.Context, p project.Project, e project.Environment, action string, next brings) error {
	request, err := s.withServices(ctx, p.Organization, e.ID,
		gate.Request{Action: action, Tier: string(p.Tier), ProjectSlug: p.Slug, Environment: e.Slug}, next)
	if err != nil {
		return err
	}
	return gate.DenyOnly(ctx, p.Organization, s.policy, request)
}

// denyOnlySource is denyOnly for a deployment from source: what it will
// build is the app's services as they are now (the layout).
func (s *Service) denyOnlySource(ctx context.Context, p project.Project, e project.Environment) error {
	topology, err := s.projects.Services(ctx, p.Organization, p.ID)
	if err != nil {
		return err
	}
	return s.denyOnly(ctx, p, e, gate.ActionDeploy, brings{layout: topology})
}

// brings is what a deployment puts online: a version's services (an image
// deploy, a rollback, a source deployment once built) or, before a build,
// only the layout of the services it will build.
type brings struct {
	version []release.Service
	layout  []project.Service
}

// judgedRequest is a deployment as IT's rules see it, from what it records.
func (s *Service) judgedRequest(ctx context.Context, d release.Deployment, base gate.Request) (gate.Request, error) {
	var next brings
	if d.Release != nil {
		r, err := s.repo.FindRelease(ctx, d.Organization, *d.Release)
		if err != nil {
			return gate.Request{}, err
		}
		next.version = r.Services
	} else {
		for _, b := range d.Builds {
			next.layout = append(next.layout, b.Service())
		}
		if len(next.layout) == 0 { // a deployment from before services: the app's own
			topology, err := s.projects.Services(ctx, d.Organization, d.Project)
			if err != nil {
				return gate.Request{}, err
			}
			next.layout = topology
		}
	}
	return s.withServices(ctx, d.Organization, d.Environment, base, next)
}

// withServices fills a deploy's or rollback's request with the services it
// brings and what changes against the release the environment serves
// (MS-3c). A version is compared image by image; a layout (a source
// deployment before its build) only by how its services are laid out: a
// change of code joins `changed` at the second judgment (authorize_scan),
// once the images exist. An app of one service reads exactly like one of
// several.
func (s *Service) withServices(ctx context.Context, org identity.OrganizationID, env project.EnvironmentID, base gate.Request, next brings) (gate.Request, error) {
	state, err := s.repo.FindState(ctx, org, env)
	if err != nil {
		return gate.Request{}, err
	}
	var serving *release.Release
	if state.Current != nil {
		r, err := s.repo.FindRelease(ctx, org, *state.Current)
		if err != nil {
			return gate.Request{}, err
		}
		serving = &r
	}
	if next.version != nil {
		return release.Compare(next.version, serving).Judged(base, release.GateServices(next.version)), nil
	}
	return release.CompareLayout(next.layout, serving).Judged(base, project.GateServices(next.layout)), nil
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
