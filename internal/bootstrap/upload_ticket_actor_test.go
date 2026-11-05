package bootstrap_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// TestUploadTicketIsJudgedAsWhoAskedForIt: the redeeming request carries no
// token (the ticket is the credential), yet IT's rules judge the deployment
// as the person who asked for the ticket, with the permissions their token
// held then: a rule on actor.permissions or actor.id sees them, and a refusal
// is the rule's own answer (403 / 409), not a silent allow.
func TestUploadTicketIsJudgedAsWhoAskedForIt(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Runner: buildfake.New(buildfake.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	pid, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: "Visitas", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, pid, project.EnvironmentFilter{})
	if err != nil || len(envs) == 0 {
		t.Fatalf("environments %v %v", envs, err)
	}
	env := envs[0].ID
	cfg := release.Config{Port: 3000, HealthPath: "/"}
	holds := []string{permission.DeploymentsCreate, permission.ProjectsTier}
	issue := func(permissions ...string) release.UploadTicket {
		t.Helper()
		asked := actor.With(ctx, actor.Actor{User: user, Permissions: permissions})
		tk, err := platform.Releases.Commands.IssueUpload(asked, org, pid, env, release.IssueUpload{Name: "visitas", Config: cfg, RequestedBy: user})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	rule := func(name, expr string, effect gate.Effect) {
		t.Helper()
		if _, err := platform.Policy.Commands.Create(ctx, org, policy.Create{Draft: policy.Draft{Name: name, Expression: expr, Effect: effect}, CreatedBy: user}); err != nil {
			t.Fatal(err)
		}
	}
	redeem := func(tk release.UploadTicket) error {
		t.Helper()
		_, err := platform.Releases.Commands.RedeemUpload(ctx, tk.Token, bytes.NewReader(folder(t, "x")))
		return err
	}

	// a rule that needs a permission: the requester's snapshot satisfies it,
	// the redeeming request (no token) would not
	rule("Solo con permiso de nivel", "action == 'deploy' && !('"+permission.ProjectsTier+"' in actor.permissions)", gate.Deny)
	if err := redeem(issue(holds...)); err != nil {
		t.Fatalf("a ticket asked for with the permission was refused: %v", err)
	}
	err = redeem(issue(permission.DeploymentsCreate))
	if errCode(err) != gate.CodeDenied || !strings.Contains(err.Error(), "Solo con permiso de nivel") {
		t.Fatalf("a ticket asked for without it: %v", err)
	}

	// the rule sees who asked
	rule("Alguien en particular necesita aprobación", "action == 'deploy' && actor.id == '"+user.String()+"'", gate.Approval)
	err = redeem(issue(holds...))
	if errCode(err) != gate.CodeApprovalRequired || !strings.Contains(err.Error(), "Alguien en particular") {
		t.Fatalf("approval by actor.id: %v", err)
	}
}

func errCode(err error) string {
	var x *errx.Error
	if errors.As(err, &x) {
		return x.Code
	}
	return ""
}
