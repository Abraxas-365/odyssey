package releasesvc

import (
	"context"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// racingRepo answers the settings reads in turn: the first is the read before
// the transaction, the later ones are under the lock.
type racingRepo struct {
	release.Repository
	modes []release.AccessMode
	reads int
}

func (r *racingRepo) FindSettings(_ context.Context, org identity.OrganizationID, env project.EnvironmentID) (release.Settings, error) {
	s := release.DefaultSettings(org, env)
	s.Access.Mode = r.modes[min(r.reads, len(r.modes)-1)]
	r.reads++
	return s, nil
}

func (r *racingRepo) LockState(_ context.Context, state release.EnvironmentState) (release.EnvironmentState, error) {
	return state, nil
}

type directTx struct{}

func (directTx) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// denyPublic refuses set_access to public and counts what it was asked.
type denyPublic struct{ asked int }

func (d *denyPublic) Check(_ context.Context, _ identity.OrganizationID, r gate.Request) (gate.Verdict, error) {
	d.asked++
	if r.Action == gate.ActionSetAccess && r.AccessMode == string(release.AccessPublic) {
		return gate.Verdict{Effect: gate.Deny, Rules: []gate.Rule{{ID: "r1", Name: "Nada público"}}}, nil
	}
	return gate.Verdict{Effect: gate.Allow}, nil
}

// The mode read as the target before the transaction (a no-op, so nothing is
// judged) but moved away from it before the lock: the change is judged after
// all, and IT's rule refuses it. No path applies a mode nobody judged.
func TestSetAccessJudgesAChangeThatAppearedUnderTheLock(t *testing.T) {
	org := identity.NewOrganizationID()
	e := project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}
	p := project.Project{ID: project.NewID(), Organization: org, Slug: "pagos"}
	repo := &racingRepo{modes: []release.AccessMode{release.AccessPublic, release.AccessTeam}}
	policy := &denyPublic{}
	svc := &Service{repo: repo, projects: &usesProjects{p: p, e: e}, tx: directTx{}, policy: policy}

	_, err := svc.SetAccess(context.Background(), org, p.ID, e.ID, release.SetAccess{Mode: release.AccessPublic, RequestedBy: identity.MustParseUserID("0193b5a0-0000-7000-8000-000000000001")})
	var refused *errx.Error
	if !errx.As(err, &refused) || refused.Code != gate.CodeDenied {
		t.Fatalf("want POLICY_DENIED, got %v", err)
	}
	if policy.asked != 1 {
		t.Fatalf("the change should be judged once, was asked %d times", policy.asked)
	}
}
