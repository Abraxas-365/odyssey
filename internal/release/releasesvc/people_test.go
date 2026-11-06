package releasesvc

import (
	"context"
	"errors"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// namesRepo is approvalRepo plus the name it was told.
type namesRepo struct {
	approvalRepo
	missing bool
	named   []string
}

func (r *namesRepo) FindDeployment(ctx context.Context, o identity.OrganizationID, id release.DeploymentID) (release.Deployment, error) {
	if r.missing {
		return release.Deployment{}, errx.NotFound("deployment")
	}
	return r.approvalRepo.FindDeployment(ctx, o, id)
}

func (r *namesRepo) ObserveRequesterName(_ context.Context, _ identity.OrganizationID, _ release.DeploymentID, name string) error {
	r.named = append(r.named, name)
	return nil
}

// directory answers Name with name (or err) and counts the questions.
type directory struct {
	name  string
	err   error
	asked int
}

func (d *directory) Name(context.Context, identity.OrganizationID, identity.UserID) (string, error) {
	d.asked++
	return d.name, d.err
}

func TestNameRequester(t *testing.T) {
	org := identity.NewOrganizationID()
	person := release.Deployment{ID: release.NewDeploymentID(), Organization: org, RequestedBy: requester}
	cases := []struct {
		name      string
		dep       release.Deployment
		missing   bool
		dir       *directory
		wantAsked int
		wantNamed []string
		wantErr   bool
	}{
		{name: "a person is named once", dep: person, dir: &directory{name: "Ana Torres"}, wantAsked: 1, wantNamed: []string{"Ana Torres"}},
		{name: "the platform is nobody", dep: release.Deployment{ID: person.ID}, dir: &directory{name: "x"}},
		{name: "a named deployment keeps its snapshot", dep: func() release.Deployment { d := person; d.RequestedByName = "Ana"; return d }(),
			dir: &directory{name: "Ana T."}},
		{name: "an unknown person keeps no name", dep: person, dir: &directory{}, wantAsked: 1},
		{name: "a directory error is retried", dep: person, dir: &directory{err: errors.New("down")}, wantAsked: 1, wantErr: true},
		{name: "a deleted deployment is ignored", dep: person, missing: true, dir: &directory{name: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &namesRepo{approvalRepo: approvalRepo{dep: c.dep}, missing: c.missing}
			s := &Service{repo: repo}
			s.UsePeople(c.dir)
			err := s.NameRequester(context.Background(), org, person.ID)
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v", err)
			}
			if c.dir.asked != c.wantAsked || len(repo.named) != len(c.wantNamed) || (len(c.wantNamed) == 1 && repo.named[0] != c.wantNamed[0]) {
				t.Fatalf("asked %d, named %v", c.dir.asked, repo.named)
			}
		})
	}
	// without a directory nothing is asked or written
	repo := &namesRepo{approvalRepo: approvalRepo{dep: person}}
	if err := (&Service{repo: repo}).NameRequester(context.Background(), org, person.ID); err != nil || len(repo.named) != 0 {
		t.Fatalf("unwired: %v %v", err, repo.named)
	}
}
