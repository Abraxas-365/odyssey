package releasesvc

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// UsePolicy wires IT's rules (the policyrelease adapter). Until it is wired
// every governed action is refused: nothing is allowed by accident.
func (s *Service) UsePolicy(p release.Policy) { s.policy = p }

// authorize is the one seam where release asks IT's rules before an action
// starts: nil lets it go on, deny is 403 POLICY_DENIED and approval is 409
// APPROVAL_REQUIRED (gate.Verdict.Err). The check runs before anything is
// stored, uploaded or built, as the actor on ctx. It covers every way of
// publishing (image, folder, git commit), a rollback (an older version goes
// online again), the access mode and connecting a resource.
//
// Phase 3b replaces the approval branch here (and only here) with "create the
// approval request and answer 202"; 3b/3c also move the deploy check into a
// workflow `authorize` step that runs after the scan, with scan.known true
// (today a deploy is judged at its start, with no scan).
func (s *Service) authorize(ctx context.Context, p project.Project, e project.Environment, request gate.Request) error {
	if s.policy == nil {
		return errx.Internal("IT's rules are not wired")
	}
	request.Tier, request.ProjectSlug, request.Environment = string(p.Tier), p.Slug, e.Slug
	verdict, err := s.policy.Check(ctx, p.Organization, request)
	if err != nil {
		return err
	}
	return verdict.Err()
}
